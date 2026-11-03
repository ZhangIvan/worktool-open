// apifox 兼容适配层（T3.6，docs/15-API-COMPAT.md；FR-34 面的文档形态薄层）。
//
// 双面并存纪律：本包不改任何既有 /openapi/v1 handler 与注册表；仅以
// Mount 挂载文档原样端点（当前 = POST /wework/sendRawMessage?robotId=，
// K1/K2），把文档信封 {socketType, list[≤100]} 拆为逐条统一管线 Submit。
// 查询组别名端点（robotInfo/get、listRawMessage、rawMsg/list、qaLog/list、
// callBack CRUD 的 doc-shaped URL）随 B2/B3 字段批落地（alias.go，未建）。
//
// 错误信封沿用上游码族（101011/101002/101004/101014/501011/501012 等），
// 信封写入函数为本包自持副本（不动 openapi.go；见 writeCompatData 族）。
package compat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// Logger 最小日志接口（与 api.Logger/gateway.Logger 同型；结构化匹配免导入）。
type Logger interface {
	Printf(format string, args ...any)
}

// Err 为桥接层错误（HTTP 状态 + 兼容面错误码 + 人读信息；由 api 包桥把
// 私有 apiError 映射过来——compat 不反向依赖 api 包，避免 import cycle）。
type Err struct {
	Status  int
	Code    int
	Message string
}

func (e *Err) Error() string { return e.Message }

// Deps 为兼容层最小依赖（窄接口注入；api.Server 经 compatDeps() 构造）。
type Deps struct {
	DB       *sqlite.DB
	Pipeline *scheduler.Service
	Now      func() time.Time
	Log      Logger
	// Authn 鉴权桥（Bearer token + robotId 归属，501011/501012 语义映射保留；
	// 安全底线不降级：文档无鉴权是旧面缺陷，不复刻）。
	Authn func(ctx context.Context, r *http.Request, robotID string) (tenant string, err *Err)
	// ResolveTarget 目标解析桥（robotId → deviceId 或 v1:robotId）。
	ResolveTarget func(ctx context.Context, robotID string) (target string, err *Err)
	// RobotRateAllow per-robot 60QPM（认证后计数；1 个 HTTP 请求计 1 次，list
	// 条数不计——上游批量规避限流的官方定位）。
	RobotRateAllow func(robotID string, now time.Time) bool
	// RateLimitPerMinute / RateWindow 供限流错误信息（与 per-robot 限流器同源）。
	RateLimitPerMinute int
	RateWindow         time.Duration
	// OpenAPIIntent 注册表查询（WTO-004：未注册 type → 101002 拒绝）。
	OpenAPIIntent func(commandType int) string
}

// Mount 挂载兼容层端点（api.NewServeMux 一行调用；模式守卫由 Go 1.22
// ServeMux 承担——非 POST 自动 405）。
func Mount(mux *http.ServeMux, deps Deps) {
	rt := &router{deps: deps, ips: newIPLimiter()}
	mux.HandleFunc("POST /wework/sendRawMessage", rt.handleSendRawMessage)
}

// ---- 兼容面错误码（上游码族镜像；与 openapi_registry.go 同值，本包自持副本） ----

const (
	codeSuccess        = 0
	codeInvalidBody    = 101001
	codeUnknownCommand = 101002
	codeRateLimited    = 101004
	codeApproval       = 101006
	codeRiskFrozen     = 101008
	codeCircuitOpen    = 101009
	codeUpstreamData   = 101011 // 数据格式错误（字段缺失/类型不符；上游语义）
	codeUpstreamRepeat = 101014 // 指令重复（入队去重命中）
	codeRobotMismatch  = 501011
	codeInvalidToken   = 501012
)

const (
	compatMaxList       = 100                         // 批量 list 上限（api-147612959）
	compatMaxBody       = 512 << 10                   // 请求体上限（与 openapiMaxBody 同型）
	compatQueueSnapshot = 512                         // up 插队时的队列快照上限
	auditKind           = "compat_send_raw"           // 提交面审计前缀
	auditKindPriority   = "openapi_v1_queue_priority" // 与 REST 面插队审计同 kind（同源留痕）
)

type router struct {
	deps Deps
	ips  *ipLimiter
}

// envelope 为兼容面响应信封（K2）：成功 {code:0, data:<string>, message:"success",
// detail:{...}}；错误 {code, message[, detail]}。data 的 string 化仅指令提交面
// （文档仅此处 data=string messageId）；别名查询端点（B2/B3）按文档各自形态。
type envelope struct {
	Code    int    `json:"code"`
	Data    any    `json:"data,omitempty"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

func writeCompatData(w http.ResponseWriter, data string, detail any) {
	writeJSON(w, http.StatusOK, envelope{Code: codeSuccess, Data: data, Message: "success", Detail: detail})
}

func writeCompatErr(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, envelope{Code: code, Message: msg})
}

func writeCompatErrData(w http.ResponseWriter, status, code int, msg string, detail any) {
	writeJSON(w, status, envelope{Code: code, Message: msg, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// itemOutcome 为 detail.list 逐条对账项（K1 第 7 步聚合响应）。
type itemOutcome struct {
	Index     int    `json:"index"`
	MessageID string `json:"messageId"`
	CommandID string `json:"commandId"`
	Status    string `json:"status"`
	Up        bool   `json:"up,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleSendRawMessage 处理 POST /wework/sendRawMessage?robotId=（K1）。
//
// 流程：per-IP 限流（K6，认证前）→ 鉴权 → per-robot 60QPM → 信封解析
// （{socketType, list 1..100}）→ 逐条预检（注册表 101002 + 必填表 101011 +
// 入队去重 101014；原子拒绝，任一不过整单 4xx 不入队）→ 逐条 Submit（内部
// 随机幂等键；文档调用方无 Idempotency-Key，同请求重放由 101014 兜底）→
// up:true 条目抬队列序键 → 聚合响应（K2：data=首条 messageId，detail 逐条）。
func (rt *router) handleSendRawMessage(w http.ResponseWriter, r *http.Request) {
	deps := rt.deps
	ctx := r.Context()
	now := deps.Now().UTC()

	// K6 per-IP 限流（认证前，防未认证洪泛消耗下游）
	if ip := ipKey(r.RemoteAddr); ip != "" {
		if allowed, banned := rt.ips.allow(ip, now); !allowed {
			if banned {
				writeCompatErr(w, http.StatusTooManyRequests, codeRateLimited,
					fmt.Sprintf("IP %s 连续超限已被封禁，冷却 %s 内直接拒绝（20QPS，docs/15 K6）", ip, PerIPBanDuration))
				return
			}
			writeCompatErr(w, http.StatusTooManyRequests, codeRateLimited,
				fmt.Sprintf("请求超出 per-IP 限流 %d 次/%s（FAQ 20QPS 口径）", PerIPRateQPS, PerIPWindow))
			return
		}
	}

	// 鉴权（Bearer + robotId 归属；robotId = query 必填，文档寻址形态）
	robotID := strings.TrimSpace(r.URL.Query().Get("robotId"))
	if robotID == "" {
		writeCompatErr(w, http.StatusBadRequest, codeUpstreamData, "缺 query 参数 robotId（文档全部端点 robotId 必填）")
		return
	}
	token, ae := deps.Authn(ctx, r, robotID)
	if ae != nil {
		writeCompatErr(w, ae.Status, ae.Code, ae.Message)
		return
	}

	// per-robot 60QPM（认证后；REV-SRV-04：1 请求 1 计数，list 条数不计）
	if !deps.RobotRateAllow(robotID, now) {
		writeCompatErr(w, http.StatusTooManyRequests, codeRateLimited,
			fmt.Sprintf("请求超出限流 %d 次/%s（60QPM，对齐 apifox 限制值）", deps.RateLimitPerMinute, deps.RateWindow))
		return
	}

	// 信封解析：{socketType(缺省容 2), list(1..100)}
	var env struct {
		SocketType *int              `json:"socketType"`
		List       []json.RawMessage `json:"list"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, compatMaxBody))
	if err := dec.Decode(&env); err != nil {
		writeCompatErr(w, http.StatusBadRequest, codeUpstreamData,
			fmt.Sprintf("请求体须为 JSON 对象 {socketType, list}（%v）", err))
		return
	}
	if env.SocketType == nil {
		env.SocketType = new(int) // 缺省容 2（方案 K1 第 3 步）
		*env.SocketType = 2
	}
	if *env.SocketType != 2 {
		writeCompatErr(w, http.StatusBadRequest, codeUpstreamData,
			fmt.Sprintf("socketType 须为固定值 2（收到 %d；缺省容 2）", *env.SocketType))
		return
	}
	if len(env.List) == 0 {
		writeCompatErr(w, http.StatusBadRequest, codeUpstreamData, "list 必填且至少 1 条指令")
		return
	}
	if len(env.List) > compatMaxList {
		writeCompatErr(w, http.StatusBadRequest, codeUpstreamData,
			fmt.Sprintf("list 超上限 %d（api-147612959：单次调用最多合并 %d 条指令）", len(env.List), compatMaxList))
		return
	}

	// 目标解析（robotId → 管线目标键）
	target, ae := deps.ResolveTarget(ctx, robotID)
	if ae != nil {
		writeCompatErr(w, ae.Status, ae.Code, ae.Message)
		return
	}

	// ---- 逐条预检（原子拒绝）：注册表 + 必填表 + 入队去重 ----
	type parsed struct {
		raw         map[string]json.RawMessage
		commandType int
		up          bool
	}
	items := make([]parsed, 0, len(env.List))
	for i, raw := range env.List {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			writeCompatErrData(w, http.StatusBadRequest, codeUpstreamData,
				fmt.Sprintf("list[%d] 须为 JSON 对象（指令项）", i), map[string]any{"index": i})
			return
		}
		rawType, ok := m["type"]
		if !ok {
			writeCompatErrData(w, http.StatusBadRequest, codeUpstreamData,
				fmt.Sprintf("list[%d] 缺 type 字段（消息类型）", i), map[string]any{"index": i})
			return
		}
		var commandType int
		if err := json.Unmarshal(rawType, &commandType); err != nil {
			writeCompatErrData(w, http.StatusBadRequest, codeUpstreamData,
				fmt.Sprintf("list[%d] type 须为整数", i), map[string]any{"index": i})
			return
		}
		if deps.OpenAPIIntent(commandType) == "" {
			writeCompatErrData(w, http.StatusBadRequest, codeUnknownCommand,
				fmt.Sprintf("list[%d] 指令 type=%d 不在兼容面注册表（上报/心跳/未定义类型不可提交）", i, commandType),
				map[string]any{"index": i, "type": commandType})
			return
		}
		if err := validateRequired(commandType, m); err != nil {
			writeCompatErrData(w, http.StatusBadRequest, codeUpstreamData,
				fmt.Sprintf("list[%d] %v（101011 数据格式错误，整单拒绝）", i, err), map[string]any{"index": i})
			return
		}
		p := parsed{raw: m, commandType: commandType}
		if upRaw, ok := m["up"]; ok {
			_ = json.Unmarshal(upRaw, &p.up) // 非布尔容忍为 false（文档 up:boolean）
		}
		items = append(items, p)
	}
	// 入队去重（上游 101014 语义；原子：任一条命中 → 整单拒绝）
	for i, p := range items {
		payload, err := json.Marshal(p.raw)
		if err != nil {
			writeCompatErr(w, http.StatusInternalServerError, codeInvalidBody, err.Error())
			return
		}
		if dup, derr := rt.queuedDuplicate(ctx, target, p.commandType, string(payload)); derr != nil {
			writeCompatErr(w, http.StatusInternalServerError, codeInvalidBody, derr.Error())
			return
		} else if dup != "" {
			writeCompatErrData(w, http.StatusBadRequest, codeUpstreamRepeat,
				fmt.Sprintf("list[%d] 指令重复（101014）：同目标同 type 同载荷指令仍在队列中（上游入队去重语义）", i),
				map[string]any{"index": i, "queuedCommandId": dup})
			return
		}
	}

	// ---- 逐条 Submit（随机幂等键：文档调用方无 Idempotency-Key；重放由 101014 兜底） ----
	approvalID := strings.TrimSpace(r.Header.Get("X-WT-Approval-Id")) // L2 指令审批号（FR-53 迁移差异）
	outcomes := make([]itemOutcome, 0, len(items))
	failed := false
	var lastSubmitErr error
	for i, p := range items {
		payload, _ := json.Marshal(p.raw) // 原文回写（键序规范化，与 REST 面同型 → 跨面去重可比对）
		key, err := wire.RandomHex(16)
		if err != nil {
			writeCompatErr(w, http.StatusInternalServerError, codeInvalidBody, err.Error())
			return
		}
		res, serr := deps.Pipeline.Submit(ctx, scheduler.SubmitRequest{
			Tenant:      token,
			Actor:       "compat:" + robotID,
			Operation:   deps.OpenAPIIntent(p.commandType),
			Key:         key,
			Target:      target,
			CommandType: p.commandType,
			Payload:     string(payload),
			ApprovalID:  approvalID,
		})
		oc := itemOutcome{Index: i, MessageID: res.CommandID, CommandID: res.CommandID, Up: p.up}
		if serr != nil {
			oc.Status = "error"
			oc.Error = serr.Error()
			oc.MessageID, oc.CommandID = "", ""
			outcomes = append(outcomes, oc)
			failed = true
			lastSubmitErr = serr
			// 中断整单：余下条目不再提交（部分已入队条目在 detail 逐条可对账，
			// 状态透明；上游无此失败面——审批/熔断为本服务安全增强的迁移差异）
			break
		}
		oc.Status = res.State
		outcomes = append(outcomes, oc)
		if p.up && res.CommandID != "" && res.State == "queued" {
			if err := rt.bumpQueuePriority(ctx, robotID, target, res.CommandID); err != nil {
				deps.Log.Printf("compat: up 插队失败 commandId=%s: %v", res.CommandID, err)
			}
		}
	}

	// 审计（提交面聚合留痕：robot + 条数 + 首条 commandId）
	rt.audit(ctx, robotID, fmt.Sprintf("list=%d first=%s", len(outcomes), outcomes[0].CommandID), now)

	if failed {
		last := outcomes[len(outcomes)-1]
		status, code, msg := mapSubmitError(lastSubmitErr)
		writeCompatErrData(w, status, code, msg, map[string]any{
			"count": len(outcomes), "list": outcomes, "failedIndex": last.Index,
		})
		return
	}
	first := outcomes[0].CommandID
	writeCompatData(w, first, map[string]any{"count": len(outcomes), "list": outcomes})
}

// mapSubmitError 把 Submit/门错误映射到兼容面信封（映射表与 openapi.go
// writeSubmitError 同族；compat 自持副本，不动 openapi.go）。
func mapSubmitError(err error) (int, int, string) {
	var ge *scheduler.GuardError
	switch {
	case errors.As(err, &ge):
		switch ge.Code {
		case scheduler.ErrCodeApprovalNeeded:
			return http.StatusAccepted, codeApproval, ge.Msg
		case scheduler.ErrCodeRateLimited:
			return http.StatusTooManyRequests, codeRateLimited, ge.Msg
		case scheduler.ErrCodeNotWhitelisted:
			return http.StatusForbidden, codeInvalidBody, ge.Msg
		case scheduler.ErrCodeCircuitOpen:
			return http.StatusServiceUnavailable, codeCircuitOpen, ge.Msg
		case scheduler.ErrCodeApprovalMismatch, scheduler.ErrCodeApproverConflict:
			return http.StatusForbidden, codeApproval, ge.Msg
		default:
			return http.StatusBadRequest, codeUpstreamData, ge.Msg
		}
	case errors.Is(err, scheduler.ErrIdempotencyConflict):
		return http.StatusConflict, codeInvalidBody, err.Error()
	case errors.Is(err, scheduler.ErrRiskFrozen):
		return http.StatusForbidden, codeRiskFrozen, err.Error()
	default:
		return http.StatusBadRequest, codeUpstreamData, err.Error()
	}
}

// queuedDuplicate 查同目标同 type 同载荷的排队指令（上游 101014 入队去重；
// SQL 与 openapi_rest.go queuedDuplicate 同型——本包无法复用私有方法，注释互指）。
func (rt *router) queuedDuplicate(ctx context.Context, target string, commandType int, payload string) (string, error) {
	rows, err := rt.deps.DB.QueryDump(ctx, `
SELECT command_id FROM commands
WHERE target = ? AND command_type = ? AND payload = ? AND state = 'queued' AND dry_run = 0
LIMIT 1`, target, commandType, payload)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		return row[0], nil
	}
	return "", nil
}

// bumpQueuePriority 把 queued 指令的队列序键（commands.created_at）抬到队首
// 之前（up:true；docs/15 缺失清单第 9 条）。SQL 语义与 REST 面
// HandleOpenAPIQueuePriority 同型（抬序键 + 审计留痕原序键值；本包不重复
// 实现门/校验——目标指令为本请求刚提交的 commandId，存在性由提交保证）。
func (rt *router) bumpQueuePriority(ctx context.Context, robotID, target, commandID string) error {
	queued, err := rt.deps.Pipeline.QueuedForTarget(ctx, target, compatQueueSnapshot)
	if err != nil {
		return err
	}
	prev, origMS := 0, int64(0)
	for i, c := range queued {
		if c.CommandID == commandID {
			prev, origMS = i+1, c.CreatedAt.UnixMilli()
			break
		}
	}
	if prev == 0 {
		return errors.New("指令不在队列中（可能已被下发）")
	}
	if prev == 1 {
		return nil // 已在队首
	}
	head := queued[0].CreatedAt.UnixMilli()
	if _, err := rt.deps.DB.Exec(ctx,
		`UPDATE commands SET created_at = ? WHERE command_id = ? AND state = 'queued'`, head-1, commandID); err != nil {
		return err
	}
	_ = rt.deps.DB.InsertAudit(ctx, nil, auditKindPriority, robotID,
		fmt.Sprintf("commandId=%s position %d→1（up:true 队列序键抬至队首前，原序键 %d 留痕于此）",
			commandID, prev, origMS), rt.deps.Now().UTC())
	return nil
}

// audit 提交面审计（失败不阻断响应；kind=compat_send_raw）。
func (rt *router) audit(ctx context.Context, robotID, detail string, at time.Time) {
	_ = rt.deps.DB.InsertAudit(ctx, nil, auditKind, robotID, detail, at)
}
