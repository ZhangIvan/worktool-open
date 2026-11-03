// FR-53 误发保护门（任务卡 T3.2；分级口径 = docs/01 FR-53「分级裁决（2026-11-03，
// CR-R1-0）」权威段，L0/L1/L2 三级、原 L3 代码别称已废止）：
//   - 风险分级：L1（批量发送/转发、建群、拉人/加好友）默认限速+接收人白名单；
//     L2（解散群、删联系人、清队列）默认需独立 approver 确认；
//     已实现普通指令 L0；未登记类型按 L2 fail-closed（裁决②，登记后方可降级）。
//   - L1 限速：按目标键滚动窗口计数（默认 10 条/分钟/目标，可配）；
//   - L1 白名单：目标配置了白名单即强制（未配置不拦截——策略可配 WhitelistDefaultDeny）；
//   - L2 审批：独立 approver（≠申请主体）决议留痕，审批与参数指纹绑定。
//
// 审计语义：所有门拒绝均走 E02（仅审计留痕，不入队不下发）或显式待审批状态。
package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// 风险分级表（FR-53；type → 风险级。分级表基线 = docs/01 FR-53 裁决③：
// L1={206,207,213,220,227,228,229,230}、L2={219,234,304}，其余已实现指令=L0、
// 未登记类型→L2 fail-closed〔ClassifyRisk〕；docs/10 新增指令须随实现同步登记本表）。
//
// 一致性约束（CR-t31-T31-R3 / CR-R1-0）：本表与端侧静态兜底表
// （android core:task-engine Intents.kt RiskLevels）**同指令同级**——
// 载荷级动态分级以执行器侧为单一事实源（如 207 含拉人 L1 / 仅公告备注 L0），
// 本表仅在执行器不可得时兜底，两表不得同指令不同级。
var riskClassByType = map[int]string{
	// L1：批量发送/转发、建群、拉人（FR-53 L1 定义）
	206: RiskL1, // INIT_GROUP 建群
	207: RiskL1, // UPDATE_GROUP 改群配置（含拉人/踢人）
	213: RiskL1, // ADD_FRIEND_BY_PHONE 手机号加好友（拉人/加好友类，docs/10 B2 规划 L1）
	220: RiskL1, // ADD_FRIEND_BY_GROUP 群内加好友（拉人/加好友类，docs/10 B2 规划 L1）
	227: RiskL1, // RELAY_MULTI_MESSAGE 批量转发
	228: RiskL1, // RELAY_MERGE_MESSAGE 合并转发
	229: RiskL1, // SEND_MULTI_MESSAGE 批量发送
	230: RiskL1, // SEND_MERGE_MESSAGE 合并发送
	// L2：解散群、删联系人、清队列（FR-53 L2 定义）
	219: RiskL2, // DISMISS_GROUP 解散群
	234: RiskL2, // DELETE_CONTACT 删联系人
	304: RiskL2, // ROBOT_QUEUE_CLEAR 清空指令队列
}

// ClassifyRisk 按指令类型返回默认风险级（恒返回 L0/L1/L2，REST 兼容层与 cron 共用）：
// docs/01 FR-53「分级裁决（2026-11-03，CR-R1-0）」③基线——分级表内 → 对应级；
// 其余**已实现**指令（plannedActionNames 名单 = openapi 注册表 ∪ {212 端侧本地动作，REST 不收}，AUD-WM3-09 修正）→ L0；
// 未登记类型 → L2 fail-closed（裁决②：防新指令未分级裸奔，登记后方可降级）。
func ClassifyRisk(commandType int) string {
	if r, ok := riskClassByType[commandType]; ok {
		return r
	}
	if riskUnregistered(commandType) {
		return RiskL2
	}
	return RiskL0
}

// riskUnregistered 报告类型是否未登记（不在分级表且不在已实现名单 → L2 fail-closed；
// 提交侧据此以「未登记」为理由标记审计与审批行，docs/01 FR-53 裁决②）。
func riskUnregistered(commandType int) bool {
	if _, ok := riskClassByType[commandType]; ok {
		return false
	}
	_, implemented := plannedActionNames[commandType]
	return !implemented
}

// GuardConfig 为误发保护配置（默认值见 DefaultGuardConfig；FR-53"分级策略可配置"）。
type GuardConfig struct {
	// L1 每目标限速：窗口内最多 MaxPerTarget 条 L1 指令（默认 10/min）。
	L1Window   time.Duration
	L1MaxPerTx int
	// WhitelistDefaultDeny：目标未配置白名单时对 L1 默认拒收（false=未配置不拦截，
	// 配置即强制）。默认 false——白名单为显式配置的保护，未配置时靠限速兜底。
	WhitelistDefaultDeny bool
}

// DefaultGuardConfig 返回默认保护配置。
func DefaultGuardConfig() GuardConfig {
	return GuardConfig{
		L1Window:             time.Minute,
		L1MaxPerTx:           10,
		WhitelistDefaultDeny: false,
	}
}

// GuardError 为门拒绝的错误（REST 层据此映射错误码）。
type GuardError struct {
	Code string // RATE_LIMITED | RECIPIENT_NOT_WHITELISTED | APPROVAL_REQUIRED | APPROVAL_MISMATCH | APPROVER_CONFLICT | CIRCUIT_OPEN
	Msg  string
}

func (e *GuardError) Error() string { return fmt.Sprintf("scheduler/guard: %s: %s", e.Code, e.Msg) }

// 门错误码（REST 层映射见 api/openapi.go）。
const (
	ErrCodeRateLimited      = "RATE_LIMITED"
	ErrCodeNotWhitelisted   = "RECIPIENT_NOT_WHITELISTED"
	ErrCodeApprovalNeeded   = "APPROVAL_REQUIRED"
	ErrCodeApprovalMismatch = "APPROVAL_MISMATCH"
	ErrCodeApproverConflict = "APPROVER_CONFLICT"
	ErrCodeCircuitOpen      = "CIRCUIT_OPEN"
	ErrCodeFileDomain       = "FILE_DOMAIN_NOT_ALLOWED"
)

// guard 为进程内保护状态（限速计数；熔断器独立文件）。
type guard struct {
	mu   sync.Mutex
	cfg  GuardConfig
	hits map[string][]time.Time // 目标键 → L1 提交时刻（滚动窗口）
}

func newGuard(cfg GuardConfig) *guard {
	if cfg.L1Window <= 0 || cfg.L1MaxPerTx <= 0 {
		cfg = DefaultGuardConfig()
	}
	return &guard{cfg: cfg, hits: map[string][]time.Time{}}
}

// allowL1Rate 判定目标键的 L1 限速（命中即登记）。
func (g *guard) allowL1Rate(target string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	cutoff := now.Add(-g.cfg.L1Window)
	kept := g.hits[target][:0]
	for _, t := range g.hits[target] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	g.hits[target] = kept
	if len(kept) >= g.cfg.L1MaxPerTx {
		return false
	}
	g.hits[target] = append(g.hits[target], now)
	return true
}

// ---- L2 审批门 ----

// ApprovalRequestInput 为创建审批请求的输入（参数指纹 = target|type|payload）。
type ApprovalRequestInput struct {
	Tenant      string
	Actor       string // 申请主体
	Target      string
	CommandType int
	Payload     string
	Recurring   bool // cron 周期任务模板用常设审批
	Reason      string
}

// ApprovalDigest 计算审批参数指纹（与实际提交参数绑定，防"审批 A 提交 B"）。
func ApprovalDigest(target string, commandType int, payload string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", target, commandType, payload)))
	return hex.EncodeToString(sum[:])
}

// RequestApproval 登记一条 pending 审批（不产生指令；Submit L2 无审批时自动走此路径）。
func (s *Service) RequestApproval(ctx context.Context, in ApprovalRequestInput) (sqlite.Approval, error) {
	now := s.Now().UTC()
	a := sqlite.Approval{
		ApprovalID:   s.newID(),
		Tenant:       in.Tenant,
		Actor:        in.Actor,
		Target:       in.Target,
		CommandType:  in.CommandType,
		ParamsDigest: ApprovalDigest(in.Target, in.CommandType, in.Payload),
		RiskClass:    RiskL2,
		Status:       "pending",
		Reason:       in.Reason,
		Recurring:    in.Recurring,
		CreatedAt:    now,
	}
	if err := s.db.InsertApproval(ctx, a); err != nil {
		return sqlite.Approval{}, err
	}
	if err := s.db.InsertAudit(ctx, nil, "approval_requested", a.ApprovalID,
		fmt.Sprintf("actor=%s target=%s type=%d recurring=%v reason=%s", in.Actor, in.Target, in.CommandType, in.Recurring, in.Reason), now); err != nil {
		return sqlite.Approval{}, err
	}
	return a, nil
}

// DecideApproval 决议审批（FR-53：approver 必须活跃且 ≠ 申请主体——独立 approver，
// 自批拒绝）。approve=false 即拒绝。全部决议入审计（SR-12）。
func (s *Service) DecideApproval(ctx context.Context, approvalID, approver string, approve bool, reason string) error {
	now := s.Now().UTC()
	if approver == "" {
		return &GuardError{Code: ErrCodeApproverConflict, Msg: "approver 不能为空"}
	}
	a, ok, err := s.db.GetApproval(ctx, approvalID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("scheduler: 审批 %s 不存在", approvalID)
	}
	if approver == a.Actor {
		return &GuardError{Code: ErrCodeApproverConflict, Msg: fmt.Sprintf("approver=%s 与申请主体相同（FR-53 独立 approver，自批拒绝）", approver)}
	}
	status := "rejected"
	if approve {
		status = "approved"
	}
	updated, err := s.db.DecideApproval(ctx, approvalID, status, approver, reason, now)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("scheduler: 审批 %s 非 pending（已决议或已消费），拒绝重复决议", approvalID)
	}
	if err := s.db.InsertAudit(ctx, nil, "approval_decided", approvalID,
		fmt.Sprintf("approver=%s decision=%s reason=%s actor=%s", approver, status, reason, a.Actor), now); err != nil {
		return err
	}
	return nil
}

// verifyApproval 校验提交携带的审批（只读）：须 approved、参数指纹一致。
// 一次性审批的消费**不在此时**执行——由 Submit 的入队事务内完成（与 E01 原子，
// 入队失败整体回滚不烧审批，CR-t32-T3.2-R3）；返回审批行供消费侧判断 Recurring。
func (s *Service) verifyApproval(ctx context.Context, approvalID string, req SubmitRequest) (sqlite.Approval, error) {
	a, ok, err := s.db.GetApproval(ctx, approvalID)
	if err != nil {
		return sqlite.Approval{}, err
	}
	if !ok {
		return sqlite.Approval{}, &GuardError{Code: ErrCodeApprovalMismatch, Msg: fmt.Sprintf("审批 %s 不存在", approvalID)}
	}
	if a.Status != "approved" {
		return sqlite.Approval{}, &GuardError{Code: ErrCodeApprovalMismatch, Msg: fmt.Sprintf("审批 %s 状态=%s（须 approved）", approvalID, a.Status)}
	}
	if a.Approver == "" || a.Approver == a.Actor {
		return sqlite.Approval{}, &GuardError{Code: ErrCodeApproverConflict, Msg: "审批记录缺独立 approver"}
	}
	if digest := ApprovalDigest(req.Target, req.CommandType, req.Payload); digest != a.ParamsDigest {
		return sqlite.Approval{}, &GuardError{Code: ErrCodeApprovalMismatch, Msg: "审批参数指纹与提交参数不一致（审批与参数绑定，防审批 A 提交 B）"}
	}
	return a, nil
}

// ApprovalsByStatus 透出审批面（审计/管理查询）。
func (s *Service) ApprovalsByStatus(ctx context.Context, status string) ([]sqlite.Approval, error) {
	return s.db.ApprovalsByStatus(ctx, status)
}

// ---- L1 白名单 ----

// AddWhitelist / RemoveWhitelist / WhitelistForTarget 为白名单管理面。
func (s *Service) AddWhitelist(ctx context.Context, target, recipient, by string) error {
	return s.db.AddRecipientWhitelist(ctx, target, recipient, by, s.Now().UTC())
}

func (s *Service) RemoveWhitelist(ctx context.Context, target, recipient string) error {
	return s.db.RemoveRecipientWhitelist(ctx, target, recipient)
}

func (s *Service) WhitelistFor(ctx context.Context, target string) ([]string, error) {
	return s.db.WhitelistForTarget(ctx, target)
}

// checkWhitelist 校验 L1 载荷的全部接收人（候选字段 targetRoom/receivedName，spec
// §4.4/Q1 冻结前以候选名为准）：
//   - 字符串或字符串数组逐元素判定（批量 L1 指令接收人以数组承载时不得整体逃过
//     白名单，CR-t32-T3.2-R5——任一不在白名单即拒）；
//   - 载荷无接收人字段时不拦截（无法判定目标），审计留痕；
//   - 字段存在但形态不可识别（非字符串/字符串数组）默认拒收（fail-closed，
//     不给批量收件人留逃逸通道）。
func (s *Service) checkWhitelist(ctx context.Context, target, payload string) error {
	recipients, present, recognized := payloadRecipients(payload)
	if !present {
		return s.db.InsertAudit(ctx, nil, "whitelist_skip_no_recipient", target,
			"L1 载荷无接收人字段（targetRoom/receivedName），白名单不判定", s.Now().UTC())
	}
	if present && !recognized {
		return &GuardError{Code: ErrCodeNotWhitelisted,
			Msg: fmt.Sprintf("L1 载荷接收人字段形态不可识别（须字符串或字符串数组，target=%s），默认拒收", target)}
	}
	if len(recipients) == 0 {
		return s.db.InsertAudit(ctx, nil, "whitelist_skip_no_recipient", target,
			"L1 载荷接收人字段为空值（targetRoom/receivedName），白名单不判定", s.Now().UTC())
	}
	listed, err := s.db.WhitelistForTarget(ctx, target)
	if err != nil {
		return err
	}
	if len(listed) == 0 {
		if s.guard.cfg.WhitelistDefaultDeny {
			return &GuardError{Code: ErrCodeNotWhitelisted, Msg: fmt.Sprintf("目标 %s 未配置白名单且策略默认拒收（WhitelistDefaultDeny）", target)}
		}
		return nil // 未配置白名单：不拦截（限速仍生效）
	}
	for _, r := range recipients {
		found := false
		for _, w := range listed {
			if w == r {
				found = true
				break
			}
		}
		if !found {
			return &GuardError{Code: ErrCodeNotWhitelisted,
				Msg: fmt.Sprintf("接收人 %q 不在目标 %s 白名单（%d 条）", r, target, len(listed))}
		}
	}
	return nil
}

// payloadRecipients 提取载荷接收人（targetRoom/receivedName/groupMembers/addMembers 候选字段；spec §9 Q1 待冻结；
// 成员名单字段=W-M3 CR 收口 B1 修复：206/207 被拉入者同样受 L1 白名单约束，防"白名单群拉任意人"绕过）。
// 返回（接收人列表, 字段是否存在, 形态是否可识别）：
//   - 字符串 → 单元素；字符串数组 → 逐元素（批量收件人）；
//   - 空字符串/空数组 → 存在但列表为空；
//   - 其他 JSON 形态（对象/数字/嵌套数组等）→ present=true, recognized=false。
func payloadRecipients(payload string) (recipients []string, present, recognized bool) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return nil, false, false // 载荷非 JSON 对象（Submit 静态校验兜底，此处防御）
	}
	fields := []json.RawMessage{probe["targetRoom"], probe["receivedName"], probe["groupMembers"], probe["addMembers"]}
	out := []string{}
	present, recognized = false, true
	for _, raw := range fields {
		if len(raw) == 0 {
			continue // 字段缺失
		}
		present = true
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s != "" {
				out = append(out, s)
			}
			continue
		}
		var arr []string
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, e := range arr {
				if e != "" {
					out = append(out, e)
				}
			}
			continue
		}
		recognized = false // 存在但形态不可识别（fail-closed 由调用方拒收）
	}
	return out, present, recognized
}

// GetApprovalRow 透出单条审批（REST/测试）。
func (s *Service) GetApprovalRow(ctx context.Context, approvalID string) (sqlite.Approval, bool, error) {
	return s.db.GetApproval(ctx, approvalID)
}

// SetGuardConfig 供测试与未来 admin 面注入分级策略配置（FR-53"分级策略可配置"）。
func (s *Service) SetGuardConfig(cfg GuardConfig) {
	s.guard.mu.Lock()
	s.guard.cfg = cfg
	s.guard.mu.Unlock()
}

// AddAllowedFileDomain / AllowedFileDomains：SR-11 外链资源域白名单管理面
// （G11 阻断项修复——默认零白名单=任何含 fileUrl 的指令拒收；admin 面/配置注入登记）。
func (s *Service) AddAllowedFileDomain(ctx context.Context, domain, by string) error {
	return s.db.AddAllowedFileDomain(ctx, domain, by, s.Now())
}

// checkFileURLDomains 校验载荷中全部 fileUrl 值的域名 ∈ 白名单（SR-11；默认零域=全拒）。
// fileBase64（内嵌内容）不受域约束；仅 URL 形态需过白名单。
func (s *Service) checkFileURLDomains(ctx context.Context, payload string) error {
	var probe struct {
		FileURL string `json:"fileUrl"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil || probe.FileURL == "" {
		return nil // 无 fileUrl 字段（或非 JSON 对象——静态校验兜底）→ 不适用
	}
	u, err := url.Parse(probe.FileURL)
	if err != nil || u.Host == "" {
		return &GuardError{Code: ErrCodeFileDomain,
			Msg: fmt.Sprintf("fileUrl %q 不可解析（SR-11 fail-closed）", probe.FileURL)}
	}
	allowed, err := s.db.AllowedFileDomains(ctx)
	if err != nil {
		return err
	}
	for _, d := range allowed {
		if u.Host == d || strings.HasSuffix(u.Host, "."+d) {
			return nil // 精确或子域匹配
		}
	}
	return &GuardError{Code: ErrCodeFileDomain,
		Msg: fmt.Sprintf("fileUrl 域名 %q 不在 SR-11 白名单（默认拒收，登记后方可放行）", u.Host)}
}
