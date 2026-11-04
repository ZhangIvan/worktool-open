// Package scheduler 实现统一命令管线（docs/02-ARCHITECTURE.md §4 scheduler 行；
// 任务卡 T2.3：按 protocol/state-transitions.csv 表驱动状态机调度指令生命周期）。
//
// 崩溃恢复语义：RecoverInflight() 在启动时把 dispatched/acked/running
// 无持久结果证据的指令经 E06/E07/E08 迁入 execution_unknown（只登记对账，
// 禁止超时推断成败——三原则①，docs/02 §5.1）。
package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// 风险等级（docs/01 FR-53「分级裁决（2026-11-03，CR-R1-0）」权威口径：L0/L1/L2，
// 原 L3 命名废止——L3 ≡ 裁决前的"L0 普通"代码别称，统一为 L0；未登记类型按 L2
// fail-closed（裁决②），登记后方可降级）。
const (
	RiskL0 = "L0" // 普通（已实现指令基线，裁决③）
	RiskL1 = "L1" // 批量发送/转发、建群、拉人（含加好友）
	RiskL2 = "L2" // 解散群、删联系人、清队列；未登记类型 fail-closed 兜底
)

// 边 ID 常量（CSV 权威件 ID 引用；证据留痕判定用，不复述边表语义）。
const (
	edgeTransportAck = "E05"
	edgeExecStart    = "E09"
)

// 错误语义。
var (
	// ErrIdempotencyConflict：同幂等键变参（state-policy#idempotency.same_key_diff_params）。
	ErrIdempotencyConflict = errors.New("scheduler: 幂等键冲突：同键变参（409 IDEMPOTENCY_CONFLICT）")
	// ErrRiskFrozen：FR-53 冻结——同风险级存在 pending execution_unknown。
	ErrRiskFrozen = errors.New("scheduler: 指令冻结：同风险级存在待核 execution_unknown（FR-53）")
	// ErrOrphanResult：结果到达时指令无可迁移路径（如 queued 未下发即回执；仅审计）。
	ErrOrphanResult = errors.New("scheduler: 孤儿结果：指令无可迁移路径（仅审计留痕）")
)

// Service 为统一命令管线（单连接 SQLite：所有写路径经 withTx 串行）。
type Service struct {
	db      *sqlite.DB
	m       *statemachine.Machine
	guard   *guard
	breaker *Breaker
	// Now 为可注入时钟（TTL 扫描/幂等窗/墓碑保留期测试用）。
	Now func() time.Time
}

// New 创建管线服务。
func New(db *sqlite.DB, m *statemachine.Machine) *Service {
	s := &Service{
		db:      db,
		m:       m,
		guard:   newGuard(DefaultGuardConfig()),
		breaker: NewBreaker(DefaultBreakerConfig()),
		Now:     time.Now,
	}
	s.breaker.SetClock(func() time.Time { return s.Now().UTC() })
	return s
}

// Machine 返回底层状态机。
func (s *Service) Machine() *statemachine.Machine { return s.m }

// Breaker 透出全局熔断器（状态查询/人工复位；FR-53）。
func (s *Service) Breaker() *Breaker { return s.breaker }

// SubmitRequest 为指令提交（E01 enqueue / E02 reject 入口；REST 兼容层 T3.2 接线）。
type SubmitRequest struct {
	Tenant      string
	Actor       string
	Operation   string
	Key         string // 幂等四元组 key 段（空 = 不启用幂等）
	Target      string // v2 deviceId 或 v1:robotId
	CommandType int
	Payload     string // list[0] JSON 对象原文
	RiskClass   string // 空 = 按类型分级（ClassifyRisk：已实现=L0 基线、未登记→L2 fail-closed）
	// ApprovalID 为 L2 指令携带的审批号（空 = 登记待审批并返回 APPROVAL_REQUIRED；FR-53）。
	ApprovalID string
	// DryRun 为干跑标记（FR-54：不下发设备，服务端沿同一状态机产出报告）。
	DryRun bool
	// ParamsFingerprint 参与幂等“同键变参”判定的参数指纹（默认取 Payload）。
	ParamsFingerprint string
}

// SubmitResult 为提交结果。
type SubmitResult struct {
	CommandID  string
	State      string
	Replay     bool   // 幂等命中（同键同参返回原响应，重试安全）
	ApprovalID string // State=awaiting_approval 时携带（L2 待审批）
}

// Submit 落一条指令：E01 enqueue（幂等键占用 + 事件/审计同事务）或 E02 reject（仅审计）。
// FR-53 门链（T3.2）：熔断 → L2 审批 → L1 白名单/限速 → 风险冻结。
func (s *Service) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	now := s.Now().UTC()
	risk := req.RiskClass
	unregistered := false // 未登记类型按 L2 fail-closed（docs/01 FR-53 裁决②，CR-R1-0 收尾）
	if risk == "" {
		risk = ClassifyRisk(req.CommandType) // 恒返回 L0/L1/L2（未登记→L2）
		unregistered = riskUnregistered(req.CommandType)
	}
	// 静态校验（E02 guard：schema/约束失败即拒绝，不下发不入队）
	if req.Target == "" {
		return s.reject(ctx, now, "target 为空")
	}
	if err := validatePayload(req.CommandType, req.Payload); err != nil {
		return s.reject(ctx, now, err.Error())
	}

	idemKey := idempotencyKey(req)
	params := req.ParamsFingerprint
	if params == "" {
		params = req.Payload
	}
	paramsHash := hashHex(params)
	window := time.Duration(s.m.Policy().Idempotency.WindowHours) * time.Hour

	// 幂等重放判定先于 FR-53 门链（CR-t32-T3.2-R2，spec §5.6.1「同键同参→返回原
	// 响应」重试安全）：合法重试不得被熔断/审批消费/白名单收紧/限速计数拦截——
	// 只有**新提交**才走门链。并发窗口内的漏网者仍由下方事务内重查兜底。
	if idemKey != "" {
		have, prevCmd, _, createdAt, ok, err := s.db.IdempotencyGet(ctx, nil, idemKey)
		if err != nil {
			return SubmitResult{}, err
		}
		if ok && s.Now().UTC().Sub(createdAt) <= window {
			if have != paramsHash {
				return SubmitResult{}, fmt.Errorf("%w：key=%s", ErrIdempotencyConflict, idemKey)
			}
			return SubmitResult{CommandID: prevCmd, State: "queued", Replay: true}, nil
		}
	}

	// FR-53 全局熔断：open 期间自动暂停——新提交一律 E02（仅审计），冷却后 half-open 放行试探
	if ok, retryAfter := s.breaker.Allow(); !ok {
		return s.rejectGuard(ctx, now, &GuardError{Code: ErrCodeCircuitOpen,
			Msg: fmt.Sprintf("全局熔断开启（错误率阈值触发自动暂停，FR-53），冷却剩余 %s", retryAfter.Truncate(time.Second))})
	}

	// FR-53 L2 审批门：无审批 → 登记 pending 审批并返回 APPROVAL_REQUIRED（不产生指令）
	if risk == RiskL2 && req.ApprovalID == "" {
		reason := "L2 危险指令（FR-53）自动登记待审批"
		if unregistered {
			// 未登记类型按 L2 fail-closed（裁决②）：以「未登记」为理由标记（审计 + 审批行）
			reason = fmt.Sprintf("类型 %d 未登记（docs/01 FR-53 分级裁决②：未登记默认按 L2 fail-closed，分级表登记后方可降级）", req.CommandType)
			if err := s.db.InsertAudit(ctx, nil, "risk_unregistered", req.Target, reason, now); err != nil {
				return SubmitResult{}, err
			}
		}
		a, err := s.RequestApproval(ctx, ApprovalRequestInput{
			Tenant: req.Tenant, Actor: req.Actor, Target: req.Target,
			CommandType: req.CommandType, Payload: req.Payload,
			Reason: reason,
		})
		if err != nil {
			return SubmitResult{}, err
		}
		msg := fmt.Sprintf("L2 指令须独立 approver 确认（FR-53）：审批 %s 已登记待决议", a.ApprovalID)
		if unregistered {
			msg = fmt.Sprintf("L2 指令须独立 approver 确认（FR-53，未登记类型 fail-closed，docs/01 裁决②）：审批 %s 已登记待决议", a.ApprovalID)
		}
		return SubmitResult{State: "awaiting_approval", ApprovalID: a.ApprovalID}, &GuardError{
			Code: ErrCodeApprovalNeeded,
			Msg:  msg,
		}
	}
	var l2Approval sqlite.Approval // L2 携审批提交的审批行（一次性消费在入队事务内，见下）
	if risk == RiskL2 {
		a, err := s.verifyApproval(ctx, req.ApprovalID, req)
		if err != nil {
			var ge *GuardError
			if errors.As(err, &ge) {
				return s.rejectGuard(ctx, now, ge)
			}
			return SubmitResult{}, err
		}
		l2Approval = a
	}

	// FR-53 L1：接收人白名单（配置即强制）+ 每目标限速（默认 10/min）
	if risk == RiskL1 {
		if err := s.checkWhitelist(ctx, req.Target, req.Payload); err != nil {
			var ge *GuardError
			if errors.As(err, &ge) {
				return s.rejectGuard(ctx, now, ge)
			}
			return SubmitResult{}, err
		}
		if !s.guard.allowL1Rate(req.Target, now) {
			return s.rejectGuard(ctx, now, &GuardError{Code: ErrCodeRateLimited,
				Msg: fmt.Sprintf("L1 指令限速：目标 %s 超出 %d 条/%s（FR-53 默认限速）",
					req.Target, s.guard.cfg.L1MaxPerTx, s.guard.cfg.L1Window)})
		}
	}

	// SR-11：外链资源域白名单（fileUrl/fileBase64 中的 URL 域名仅允许显式登记的域，
	// 默认零白名单=全拒；AUD-G 阻断项修复——docs/09 P5 预演发现）。对全部风险级生效。
	if err := s.checkFileURLDomains(ctx, req.Payload); err != nil {
		var ge *GuardError
		if errors.As(err, &ge) {
			return s.rejectGuard(ctx, now, ge)
		}
		return SubmitResult{}, err
	}

	// FR-53 冻结：同风险级存在 pending execution_unknown → 拒绝新提交
	if risk == RiskL1 || risk == RiskL2 {
		pending, err := s.db.PendingUnknownRisk(ctx)
		if err != nil {
			return SubmitResult{}, err
		}
		if pending[risk] > 0 {
			return SubmitResult{}, fmt.Errorf("%w：risk=%s pending=%d", ErrRiskFrozen, risk, pending[risk])
		}
	}

	commandID := wire.NewUUID()
	var replay bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// 并发兜底：早前重放判定与门链之间可能有同键新提交抢先入队
		//（命中即返回原响应，不再消费审批/不重复入队）。
		if idemKey != "" {
			have, prevCmd, _, createdAt, ok, err := s.db.IdempotencyGet(ctx, tx, idemKey)
			if err != nil {
				return err
			}
			if ok && s.Now().UTC().Sub(createdAt) <= window {
				if have != paramsHash {
					return ErrIdempotencyConflict
				}
				replay = true // 同键同参 → 返回原响应（重试安全）
				commandID = prevCmd
				return nil
			}
		}
		cmd := sqlite.Command{
			CommandID:   commandID,
			Target:      req.Target,
			CommandType: req.CommandType,
			Payload:     req.Payload,
			State:       "created",
			RiskClass:   risk,
			DryRun:      req.DryRun,
			CreatedAt:   now,
		}
		if err := s.db.InsertCommand(ctx, tx, cmd); err != nil {
			return err
		}
		edge, ok := s.m.EdgeFor("created", "enqueue")
		if !ok {
			return fmt.Errorf("scheduler: 边集缺 E01（created→queued）")
		}
		if err := s.applyEdge(ctx, tx, &cmd, edge, "提交入队", now); err != nil {
			return err
		}
		if idemKey != "" {
			if err := s.db.IdempotencyPut(ctx, tx, idemKey, paramsHash, commandID, "", now); err != nil {
				return err
			}
		}
		// 一次性审批消费与 E01 入队**同事务**（CR-t32-T3.2-R3）：此前独立 UPDATE 在
		// 入队前执行，后续任一步失败（同键变参/风险冻结/DB 错误）会把审批烧掉而指令
		// 未入队。入队成功才消费；整体回滚即不消费。并发抢先消费（0 行生效）fail-closed。
		if risk == RiskL2 && req.ApprovalID != "" && !l2Approval.Recurring {
			consumed, err := s.db.ConsumeApprovalTx(ctx, tx, req.ApprovalID, now)
			if err != nil {
				return err
			}
			if !consumed {
				return &GuardError{Code: ErrCodeApprovalMismatch,
					Msg: fmt.Sprintf("审批 %s 已被并发消费（FR-53 一次性审批与单次提交绑定）", req.ApprovalID)}
			}
		}
		commandID = cmd.CommandID
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return SubmitResult{}, fmt.Errorf("%w：key=%s", ErrIdempotencyConflict, idemKey)
		}
		return SubmitResult{}, err
	}
	return SubmitResult{CommandID: commandID, State: "queued", Replay: replay}, nil
}

// reject 走 E02（created→failed，仅审计，不下发不入队）。
func (s *Service) reject(ctx context.Context, now time.Time, reason string) (SubmitResult, error) {
	detail, _ := json.Marshal(map[string]string{"reason": reason})
	if err := s.db.InsertAudit(ctx, nil, "command_reject", "", string(detail), now); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{}, fmt.Errorf("scheduler: 指令被拒绝（E02）: %s", reason)
}

// rejectGuard 为 FR-53 门拒绝的 E02 留痕（仅审计，不入队；类型化错误供 REST 层映射）。
func (s *Service) rejectGuard(ctx context.Context, now time.Time, ge *GuardError) (SubmitResult, error) {
	detail, _ := json.Marshal(map[string]string{"gate": ge.Code, "reason": ge.Msg})
	if err := s.db.InsertAudit(ctx, nil, "command_reject", "", string(detail), now); err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{}, ge
}

func validatePayload(commandType int, payload string) error {
	if commandType < 11 || commandType > 999 {
		return fmt.Errorf("commandType %d 越界（11~999）", commandType)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return fmt.Errorf("payload 非 JSON 对象: %w", err)
	}
	raw, ok := probe["type"]
	if !ok {
		return fmt.Errorf("payload 缺 type 字段")
	}
	var t int
	if err := json.Unmarshal(raw, &t); err != nil || t != commandType {
		return fmt.Errorf("payload.type(%d) 与 commandType(%d) 不一致", t, commandType)
	}
	return nil
}

func idempotencyKey(req SubmitRequest) string {
	if req.Key == "" {
		return ""
	}
	return strings.Join([]string{req.Tenant, req.Actor, req.Operation, req.Key}, "|")
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// QueuedForTarget 取目标待下发指令（gateway 在会话就绪后拉取）。
func (s *Service) QueuedForTarget(ctx context.Context, target string, limit int) ([]sqlite.Command, error) {
	return s.db.QueuedForTarget(ctx, target, limit)
}

// MarkDispatched 落 E03（queued→dispatched）：网关已实际发送帧后调用
// （先发送后留痕：发送后崩溃 → 指令仍 queued → 新会话补发，at-least-once）。
func (s *Service) MarkDispatched(ctx context.Context, commandID string, epoch, seq int64, eventID string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（dispatch）", commandID)
		}
		edge, ok := s.m.EdgeFor(cmd.State, "dispatch")
		if !ok {
			return statemachine.NewIllegalTransitionError(cmd.State, "dispatch", "MarkDispatched")
		}
		ttl := time.Duration(s.m.Policy().AckTTLSeconds) * time.Second
		cmd.Epoch = epoch
		cmd.Seq = seq
		cmd.EventID = eventID
		cmd.DispatchedAt = sql.NullTime{Time: now, Valid: true}
		cmd.AckDeadline = sql.NullTime{Time: now.Add(ttl), Valid: true}
		return s.applyEdge(ctx, tx, &cmd, edge,
			fmt.Sprintf("epoch=%d seq=%d eventId=%s ttl=%ds", epoch, seq, eventID, s.m.Policy().AckTTLSeconds), now)
	})
}

// HandleTransportAck 落 E05（dispatched→acked；transport ack ≠ 执行许可）。
func (s *Service) HandleTransportAck(ctx context.Context, commandID, evidence string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（transport_ack）", commandID)
		}
		edge, ok := s.m.EdgeFor(cmd.State, "transport_ack")
		if !ok {
			if cmd.State == "acked" {
				return nil // 幂等：重复确认无害
			}
			return statemachine.NewIllegalTransitionError(cmd.State, "transport_ack", "HandleTransportAck")
		}
		return s.applyEdge(ctx, tx, &cmd, edge, "evidence="+evidence, now)
	})
}

// HandleExecStart 落 E09（acked→running；证据=设备 journal 启动记录）。
func (s *Service) HandleExecStart(ctx context.Context, commandID, evidence string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（exec_start）", commandID)
		}
		edge, ok := s.m.EdgeFor(cmd.State, "exec_start")
		if !ok {
			if cmd.State == "running" {
				return nil // 幂等
			}
			return statemachine.NewIllegalTransitionError(cmd.State, "exec_start", "HandleExecStart")
		}
		return s.applyEdge(ctx, tx, &cmd, edge, "evidence="+evidence, now)
	})
}

// HandleResult 处理执行结果（socketType=3 / 结果载荷）：
//   - dispatched：E05（结果帧即传输证据）→ E09（回执即启动证据）→ E10/E11
//   - acked：E09 → E10/E11
//   - running：直接 E10/E11
//   - execution_unknown：E12（errorCode=0）/ E14（≠0）——结果即持久证据对账离出
//   - 终态：同内容重复回执 → 幂等回执原确认，不重复副作用（§5.4.3）
//   - queued：孤儿结果（未下发先回执）→ 仅审计（ErrOrphanResult）
//
// 返回迁移后状态。
func (s *Service) HandleResult(ctx context.Context, commandID string, r *wire.ExecResult) (string, error) {
	var state string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = s.handleResultTx(ctx, tx, commandID, r)
		return err
	})
	return state, err
}

// handleResultTx 为 HandleResult 的事务内实现（ReconcileJournal 复用，禁止嵌套开事务）。
func (s *Service) handleResultTx(ctx context.Context, tx *sql.Tx, commandID string, r *wire.ExecResult) (string, error) {
	now := s.Now().UTC()
	cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("scheduler: 结果对应指令 %s 不存在", commandID)
	}
	switch cmd.State {
	case "succeeded", "failed", "cancelled":
		return cmd.State, nil // 幂等：重复回执不重复副作用
	case "queued":
		if err := s.db.InsertAudit(ctx, tx, "orphan_result", commandID,
			fmt.Sprintf("queued 指令收到结果 errorCode=%d（无迁移路径，仅审计）", r.ErrorCode), now); err != nil {
			return "", err
		}
		return cmd.State, ErrOrphanResult
	}

	// 证据链：结果帧蕴含传输确认（E05）与启动证据（E09）
	if cmd.State == "dispatched" {
		if e, ok := s.m.EdgeFor("dispatched", "transport_ack"); ok {
			if err := s.applyEdge(ctx, tx, &cmd, e, "result-frame-as-transport-evidence", now); err != nil {
				return "", err
			}
		}
	}
	if cmd.State == "acked" {
		if e, ok := s.m.EdgeFor("acked", "exec_start"); ok {
			if err := s.applyEdge(ctx, tx, &cmd, e, "result-frame-as-start-evidence", now); err != nil {
				return "", err
			}
		}
	}

	event := "exec_result"
	if cmd.State == "execution_unknown" {
		event = "reconcile_evidence" // 结果即持久证据（E12/E14）
	}
	targetState := "failed"
	if r.ErrorCode == 0 {
		targetState = "succeeded"
	}
	edge, ok := s.m.EdgeTo(cmd.State, event, targetState)
	if !ok {
		return "", statemachine.NewIllegalTransitionError(cmd.State, event, "HandleResult→"+targetState)
	}
	detail := fmt.Sprintf("errorCode=%d reason=%q timeCost=%d", r.ErrorCode, r.ErrorReason, r.TimeCost)
	if cmd.DryRun {
		detail = "dry-run " + detail
	}
	cmd.TerminalState = sql.NullString{String: edge.To, Valid: true}
	_, err = s.applyEdgeWithEffect(ctx, tx, &cmd, edge, detail, now, outboxEffect(s.db, &cmd, r, edge, now))
	if err != nil {
		return "", err
	}
	// FR-53 全局熔断：真实执行终态计入样本（干跑不计，防污染风控统计）；
	// 翻开/闭合迁移落审计事件（SR-12）。
	if !cmd.DryRun {
		if prev, next, tripped := s.breaker.Record(now, targetState == "succeeded"); tripped {
			if err := s.db.InsertAudit(ctx, tx, "circuit_breaker_open", cmd.CommandID,
				fmt.Sprintf("%s→%s（错误率阈值触发自动暂停，FR-53；结果 errorCode=%d）", prev, next, r.ErrorCode), now); err != nil {
				return "", err
			}
		} else if prev == BreakerHalfOpen && next == BreakerClosed {
			if err := s.db.InsertAudit(ctx, tx, "circuit_breaker_closed", cmd.CommandID,
				"half_open→closed（试探期连续成功，自动恢复）", now); err != nil {
				return "", err
			}
		}
	}
	return edge.To, nil
}

// outboxEffect 为终态结果的回调 outbox 入队（与迁移同事务，不变量②；
// 仅元数据，不 echo 消息正文，docs/02 §6.1）。
func outboxEffect(db *sqlite.DB, cmd *sqlite.Command, r *wire.ExecResult, edge statemachine.Edge, now time.Time) func(*sql.Tx) error {
	return func(tx *sql.Tx) error {
		payload := map[string]any{
			"type":        "exec_result",
			"commandId":   cmd.CommandID,
			"commandType": cmd.CommandType,
			"errorCode":   r.ErrorCode,
			"errorReason": r.ErrorReason,
			"timeCost":    r.TimeCost,
			"successList": r.SuccessList,
			"failList":    r.FailList,
			"finalState":  edge.To,
			"dryRun":      cmd.DryRun,
			"at":          now.UnixMilli(),
		}
		return db.EnqueueOutbox(context.Background(), tx, "exec_result", cmd.CommandID, payload, now)
	}
}

// JournalEntry 为 904 JOURNAL_REPLAY 补报条目（端侧持久证据，spec §5.4.5）。
type JournalEntry struct {
	CommandID string `json:"commandId"`
	Epoch     int64  `json:"epoch"`
	Started   bool   `json:"started"`
	Outcome   string `json:"outcome"` // "" | success | failure | never_started
	Source    string `json:"source"`  // local（离线合并）/ server
}

// ReconcileJournal 处理端侧 journal 补报（E09/E12/E13/E14，重连触发对账入口）。
func (s *Service) ReconcileJournal(ctx context.Context, entries []JournalEntry) error {
	for _, ent := range entries {
		if err := s.reconcileOne(ctx, ent); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reconcileOne(ctx context.Context, ent JournalEntry) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, ent.CommandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: journal 指令 %s 不存在", ent.CommandID)
		}
		// epoch 证据（端侧拒旧：旧 epoch 补报不迁移）
		if ent.Epoch > 0 && cmd.Epoch > 0 && ent.Epoch < cmd.Epoch {
			return s.db.InsertAudit(ctx, tx, "journal_stale_epoch", ent.CommandID,
				fmt.Sprintf("epoch=%d < 已分配=%d，拒旧", ent.Epoch, cmd.Epoch), now)
		}
		switch {
		case ent.Outcome == "never_started":
			// E13：journal 证明从未启动（epoch 证据）；只凭持久证据，禁止超时推断
			edge, ok := s.m.EdgeTo(cmd.State, "reconcile_unstarted", "failed")
			if !ok {
				if s.m.IsTerminal(cmd.State) || cmd.State == "running" || cmd.State == "queued" {
					return s.db.InsertAudit(ctx, tx, "journal_unstarted_ignored", ent.CommandID,
						"state="+cmd.State+" 无 reconcile_unstarted 边（证据与状态矛盾，留痕人工核）", now)
				}
				return statemachine.NewIllegalTransitionError(cmd.State, "reconcile_unstarted", "ReconcileJournal")
			}
			cmd.TerminalState = sql.NullString{String: "failed", Valid: true}
			return s.applyEdge(ctx, tx, &cmd, edge, "never_started（journal epoch 证据）", now)
		case ent.Outcome == "success" || ent.Outcome == "failure":
			r := &wire.ExecResult{Type: cmd.CommandType, ErrorCode: 1, ErrorReason: "reconcile: journal failure evidence"}
			if ent.Outcome == "success" {
				r.ErrorCode = 0
				r.ErrorReason = ""
			}
			_, err := s.handleResultTx(ctx, tx, ent.CommandID, r)
			if errors.Is(err, ErrOrphanResult) {
				return nil
			}
			return err
		case ent.Started:
			// 仅启动证据：E05→E09 走齐（若可行），不停留在 dispatched/acked
			if cmd.State == "dispatched" {
				if e, ok := s.m.EdgeFor("dispatched", "transport_ack"); ok {
					if err := s.applyEdge(ctx, tx, &cmd, e, "journal=started", now); err != nil {
						return err
					}
				}
			}
			if cmd.State == "acked" {
				if e, ok := s.m.EdgeFor("acked", "exec_start"); ok {
					if err := s.applyEdge(ctx, tx, &cmd, e, "journal=started（门禁+去重先于副作用的持久记录）", now); err != nil {
						return err
					}
				}
			}
			return nil
		default:
			return s.db.InsertAudit(ctx, tx, "journal_ignored", ent.CommandID, "无启动/结果字段的空条目", now)
		}
	})
}

// ManualResolve 落 E15（人工裁决，仅当持久证据穷尽；裁决记录入审计，SR-12）。
func (s *Service) ManualResolve(ctx context.Context, commandID, resolution, adjudicator, basis string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（manual_resolve）", commandID)
		}
		target := "failed"
		if resolution == "succeeded" {
			target = "succeeded"
		}
		edge, ok := s.m.EdgeTo(cmd.State, "manual_resolve", target)
		if !ok {
			return statemachine.NewIllegalTransitionError(cmd.State, "manual_resolve", "ManualResolve")
		}
		cmd.TerminalState = sql.NullString{String: target, Valid: true}
		return s.applyEdge(ctx, tx, &cmd, edge,
			fmt.Sprintf("裁决=%s 裁决人=%s 依据=%s", resolution, adjudicator, basis), now)
	})
}

// CancelBeforeDelivery 落 E04（queued→cancelled；写审计+墓碑，终态）。
func (s *Service) CancelBeforeDelivery(ctx context.Context, commandID, by string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（cancel）", commandID)
		}
		edge, ok := s.m.EdgeFor(cmd.State, "cancel_before_delivery")
		if !ok {
			return statemachine.NewIllegalTransitionError(cmd.State, "cancel_before_delivery", "CancelBeforeDelivery")
		}
		cmd.TerminalState = sql.NullString{String: "cancelled", Valid: true}
		return s.applyEdge(ctx, tx, &cmd, edge, "by="+by, now)
	})
}

// UserTakeoverStop 落 E16（running→cancelled；用户接管 FR-52 安全断点停止）。
func (s *Service) UserTakeoverStop(ctx context.Context, commandID string) error {
	now := s.Now().UTC()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		cmd, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("scheduler: 指令 %s 不存在（takeover）", commandID)
		}
		edge, ok := s.m.EdgeFor(cmd.State, "user_takeover_stop")
		if !ok {
			return statemachine.NewIllegalTransitionError(cmd.State, "user_takeover_stop", "UserTakeoverStop")
		}
		cmd.TerminalState = sql.NullString{String: "cancelled", Valid: true}
		return s.applyEdge(ctx, tx, &cmd, edge, "用户接管（FR-52）安全断点停止", now)
	})
}

// RecoverInflight 崩溃恢复：启动时把 dispatched/acked/running（=已投递/已接收/
// 执行中）无持久结果证据的指令经 E06/E07/E08 迁入 execution_unknown。
func (s *Service) RecoverInflight(ctx context.Context) (unknowns []string, err error) {
	now := s.Now().UTC()
	byState, err := s.db.InflightStates(ctx)
	if err != nil {
		return nil, err
	}
	for _, state := range []string{"dispatched", "acked", "running"} {
		for _, c := range byState[state] {
			commandID := c.CommandID
			txErr := s.withTx(ctx, func(tx *sql.Tx) error {
				cur, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
				if err != nil {
					return err
				}
				if !ok || cur.State != state {
					return nil // 期间已被迁移（重复恢复幂等）
				}
				edge, ok := s.m.EdgeFor(state, "mark_unknown")
				if !ok {
					return fmt.Errorf("scheduler: 边集缺 mark_unknown（%s）", state)
				}
				return s.applyEdge(ctx, tx, &cur, edge,
					"服务端重启/会话丢失：无持久结果证据（E06/E07/E08 对账登记，禁止超时推断成败）", now)
			})
			if txErr != nil {
				return unknowns, txErr
			}
			unknowns = append(unknowns, commandID)
		}
	}
	return unknowns, nil
}

// ScanAckTTL 扫描 ACK TTL 到期（E17/E18）：dispatched 无 transport_ack 留痕 /
// acked 无 exec_start 留痕 → execution_unknown（只登记对账，禁止超时定性，C-10）。
func (s *Service) ScanAckTTL(ctx context.Context) (expired []string, err error) {
	now := s.Now().UTC()
	due, err := s.db.DueAckTTL(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, c := range due {
		commandID := c.CommandID
		migrated := false
		txErr := s.withTx(ctx, func(tx *sql.Tx) error {
			cur, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
			if err != nil {
				return err
			}
			if !ok || (cur.State != "dispatched" && cur.State != "acked") {
				return nil // 已迁移（幂等）
			}
			evidenceEdge := edgeTransportAck
			if cur.State == "acked" {
				evidenceEdge = edgeExecStart
			}
			has, err := s.db.HasEventEdge(ctx, tx, cur.CommandID, evidenceEdge)
			if err != nil {
				return err
			}
			if has {
				return nil // 已有持久证据：TTL 到期不构成对账触发（guard 不满足）
			}
			edge, ok := s.m.EdgeFor(cur.State, "ack_ttl_expired")
			if !ok {
				return fmt.Errorf("scheduler: 边集缺 ack_ttl_expired（%s）", cur.State)
			}
			if err := s.applyEdge(ctx, tx, &cur, edge,
				fmt.Sprintf("ACK TTL %ds 到期且无 %s 持久证据（只登记对账，禁止超时定性成败，AUD-03 C-10）",
					s.m.Policy().AckTTLSeconds, evidenceEdge), now); err != nil {
				return err
			}
			migrated = true
			return nil
		})
		if txErr != nil {
			return expired, txErr
		}
		if migrated {
			expired = append(expired, commandID)
		}
	}
	return expired, nil
}

// ScanQueuedTTL 扫描排队超期（E19，2026-11-04 所有者反馈修复）：queued 态超过
// 排队 TTL（默认 24h）仍无投递 → cancelled（离线积压清理；从未下发不涉成败推定）。
// 与 E17/E18 不同：queued 从未出服务端，无对账歧义，可直接终态化。
func (s *Service) ScanQueuedTTL(ctx context.Context, ttl time.Duration) (cancelled []string, err error) {
	now := s.Now().UTC()
	rows, err := s.db.QueryDump(ctx, `SELECT command_id FROM commands WHERE state='queued' AND created_at < ?`,
		now.Add(-ttl).UnixMilli())
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		commandID := r[0]
		migrated := false
		txErr := s.withTx(ctx, func(tx *sql.Tx) error {
			cur, ok, err := s.db.GetCommandForUpdate(ctx, tx, commandID)
			if err != nil {
				return err
			}
			if !ok || cur.State != "queued" {
				return nil // 已迁移（幂等）
			}
			edge, found := s.m.EdgeFor("queued", "queue_ttl_expired")
			if !found {
				return fmt.Errorf("scheduler: 边集缺 queued/queue_ttl_expired（E19）")
			}
			if err := s.applyEdge(ctx, tx, &cur, edge,
				fmt.Sprintf("排队 TTL %s 到期无投递（设备离线积压清理；从未下发，可重新提交）", ttl), now); err != nil {
				return err
			}
			migrated = true
			return nil
		})
		if txErr != nil {
			return cancelled, txErr
		}
		if migrated {
			cancelled = append(cancelled, commandID)
		}
	}
	return cancelled, nil
}

// QueuedTTLDefault 排队超期默认值（24h；部署可配归 config 车道）。
const QueuedTTLDefault = 24 * time.Hour

// RunAckTTLScanner 周期扫描（UNKNOWN 对账任务入口之一：周期扫描；另一入口=重连触发）。
func (s *Service) RunAckTTLScanner(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if _, err := s.ScanAckTTL(ctx); err != nil {
				return err
			}
			if _, err := s.ScanQueuedTTL(ctx, QueuedTTLDefault); err != nil {
				return err
			}
		}
	}
}

// applyEdge 在事务内落一条迁移：状态更新 + command_events 留痕 + 审计 +
// （终态时）墓碑（仅元数据）——不变量①②。
func (s *Service) applyEdge(ctx context.Context, tx *sql.Tx, cmd *sqlite.Command, edge statemachine.Edge, detail string, now time.Time) error {
	_, err := s.applyEdgeWithEffect(ctx, tx, cmd, edge, detail, now, nil)
	return err
}

// applyEdgeWithEffect 为 applyEdge 的带终态副作用版本（副作用与迁移同事务）。
func (s *Service) applyEdgeWithEffect(ctx context.Context, tx *sql.Tx, cmd *sqlite.Command, edge statemachine.Edge,
	detail string, now time.Time, effect func(*sql.Tx) error) (string, error) {
	from := cmd.State
	cmd.State = edge.To
	if s.m.IsTerminal(edge.To) {
		if !cmd.TerminalAt.Valid {
			cmd.TerminalAt = sql.NullTime{Time: now, Valid: true}
		}
		if !cmd.TerminalState.Valid {
			cmd.TerminalState = sql.NullString{String: edge.To, Valid: true}
		}
	}
	if err := s.db.UpdateCommandState(ctx, tx, *cmd); err != nil {
		return "", err
	}
	if err := s.db.InsertEvent(ctx, tx, sqlite.CommandEvent{
		CommandID: cmd.CommandID, EdgeID: edge.ID, FromState: from, ToState: edge.To,
		Event: edge.Event, Detail: detail, At: now,
	}); err != nil {
		return "", err
	}
	if err := s.db.InsertAudit(ctx, tx, "command_transition", cmd.CommandID,
		fmt.Sprintf("%s %s→%s (%s)", edge.ID, from, edge.To, detail), now); err != nil {
		return "", err
	}
	if s.m.IsTerminal(edge.To) {
		if err := s.db.InsertTombstone(ctx, tx, sqlite.Tombstone{
			CommandID:  cmd.CommandID,
			EventID:    cmd.EventID,
			FinalState: edge.To,
			TerminalAt: now,
			PurgeAfter: now.Add(time.Duration(s.m.Policy().TombstoneMinDays) * 24 * time.Hour),
		}); err != nil {
			return "", err
		}
	}
	if effect != nil {
		if err := effect(tx); err != nil {
			return "", err
		}
	}
	return edge.To, nil
}

func (s *Service) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
