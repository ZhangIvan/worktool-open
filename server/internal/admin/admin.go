package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// adminScopeRobotID 为 api_tokens.robot_id 的保留值，编码 admin scope。
// v2 deviceId pattern ^[A-Za-z0-9._-]{8,64}$（spec §5.1.2）不含 5 字符值，
// 与真实设备主体无碰撞；admin 令牌不得用于 /openapi/v1 业务面——该约束不再是
// README 告知而是服务端强制：openapi 签发/鉴权两侧硬拒保留名（CR ADM-1，常量
// 单一来源 = sqlite.ReservedAdminScopeRobotID，两包共享防语义漂移）。
const adminScopeRobotID = sqlite.ReservedAdminScopeRobotID

// pairingCodeTTL 配对码有效期（spec §5.1.1 冻结：10 分钟，不可配）。
const pairingCodeTTL = 10 * time.Minute

// Logger 为最小日志接口（标准 log 与测试桩皆可注入；零遥测，G3）。
type Logger interface {
	Printf(format string, args ...any)
}

// Deps 为装配依赖（DB 必填；Pipeline 审批决议必填；其余可选）。
type Deps struct {
	DB *sqlite.DB
	// Pipeline 审批决议服务（scheduler.DecideApproval 承载 FR-53 独立 approver
	// 语义：自批拒绝、仅 pending 可决议、决议入审计 SR-12）。
	Pipeline *scheduler.Service
	Now      func() time.Time
	Log      Logger
	// Online 为在线探测（target → 是否有活连接）。nil = 网关探测未接线，
	// 设备列表 online 恒 false（诚实降级，不猜）。生产接线建议：网关车道为
	// Hub 增设只读快照方法（如 OnlineTargets）后注入闭包——见 admin/README。
	Online func(target string) bool
	// UIDir 为单页 UI 目录（仓库 admin/）。空 = 不伺服 UI（仅 API）。
	UIDir string
	// AllowNonLoopbackCallbacks 放行外网回调绑定/探测（默认 false：仅 loopback，
	// 与 api.Server.AllowNonLoopbackCallbacks 同纪律，SR-09；部署侧须 TLS，SR-01）。
	AllowNonLoopbackCallbacks bool
}

// Server 为管理端服务（v0.1 最小面 + W-P0 完整管理后台数据面 console.go）。
type Server struct {
	db                        *sqlite.DB
	pipeline                  *scheduler.Service
	now                       func() time.Time
	log                       Logger
	online                    func(target string) bool
	uiDir                     string
	allowNonLoopbackCallbacks bool
}

// New 构建管理端服务（缺省 Now/Log 注入默认值；自治设置表幂等建表——失败
// 即 panic，与 api.NewServeMux ensureOpenAPISchema 同 fail-fast 纪律）。
func New(d Deps) *Server {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	lg := d.Log
	if lg == nil {
		lg = log.Default()
	}
	online := d.Online
	if online == nil {
		online = func(string) bool { return false }
	}
	s := &Server{
		db: d.DB, pipeline: d.Pipeline, now: now, log: lg, online: online,
		uiDir: d.UIDir, allowNonLoopbackCallbacks: d.AllowNonLoopbackCallbacks,
	}
	if err := s.ensureSchema(); err != nil {
		panic(err)
	}
	return s
}

// Register 把管理端路由挂到 mux（Go 1.22 方法+路径模式）。
// 集成方在 server 主装配处调用一次；未挂载时本包端点不可达（默认不暴露）。
func (s *Server) Register(mux *http.ServeMux) {
	// —— v0.1 最小面（原四端点 + 审批/令牌生命周期，行为不变）——
	mux.HandleFunc("POST /api/v1/admin/pairing-codes", s.handlePairingCodes)
	mux.HandleFunc("GET /api/v1/admin/devices", s.handleDevices)
	mux.HandleFunc("GET /api/v1/admin/commands", s.handleCommands)
	mux.HandleFunc("GET /api/v1/admin/approvals", s.handleApprovals)
	mux.HandleFunc("POST /api/v1/admin/approvals/{approvalId}/decision", s.handleApprovalDecision)
	mux.HandleFunc("/api/v1/admin/robots/{robotId}/openapi-tokens", s.handleOpenAPITokens)
	// —— W-P0 完整管理后台数据面（console.go）——
	mux.HandleFunc("GET /api/v1/admin/overview", s.handleOverview)
	mux.HandleFunc("POST /api/v1/admin/devices/{deviceId}/{action}", s.handleDeviceAction)
	mux.HandleFunc("POST /api/v1/admin/commands", s.handleCommandSubmit)
	mux.HandleFunc("GET /api/v1/admin/commands/{commandId}", s.handleCommandDetail)
	mux.HandleFunc("GET /api/v1/admin/queue", s.handleQueueList)
	mux.HandleFunc("DELETE /api/v1/admin/queue", s.handleQueueClear)
	mux.HandleFunc("DELETE /api/v1/admin/queue/{commandId}", s.handleQueueRemove)
	mux.HandleFunc("POST /api/v1/admin/queue/{commandId}/priority", s.handleQueuePriority)
	mux.HandleFunc("/api/v1/admin/callbacks", s.handleCallbacks)
	mux.HandleFunc("GET /api/v1/admin/catalog", s.handleCatalog)
	mux.HandleFunc("POST /api/v1/admin/callbacks/test", s.handleCallbackTest)
	mux.HandleFunc("GET /api/v1/admin/callbacks/logs", s.handleCallbackLogs)
	mux.HandleFunc("GET /api/v1/admin/logs/executions", s.handleExecutionLogs)
	mux.HandleFunc("GET /api/v1/admin/logs/audit", s.handleAuditLogs)
	mux.HandleFunc("GET /api/v1/admin/settings", s.handleSettings)
	mux.HandleFunc("PUT /api/v1/admin/settings/guard", s.handleGuardSettings)
	mux.HandleFunc("PUT /api/v1/admin/settings/encryption", s.handleEncryptionSettings)
	mux.HandleFunc("PUT /api/v1/admin/settings/event-callbacks", s.handleEventCallbackSettings)
	mux.HandleFunc("POST /api/v1/admin/breaker/reset", s.handleBreakerReset)
	mux.HandleFunc("GET /api/v1/admin/risk/{type}", s.handleRiskQuery)
	mux.HandleFunc("/api/v1/admin/whitelist", s.handleWhitelist)
	mux.HandleFunc("GET /api/v1/admin/tokens", s.handleTokensList)
	if s.uiDir != "" {
		// GET /admin → /admin/（相对解析）；/admin/ 下静态文件（index.html 等）。
		mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
		})
		mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServer(http.Dir(s.uiDir))))
	}
}

// handleOpenAPITokens 兼容面令牌生命周期路由（方法守卫内派发；W-M5 日常使用
// 闭环：签发 → 只读列表 → 吊销旋转）。
func (s *Server) handleOpenAPITokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleOpenAPITokenIssue(w, r)
	case http.MethodGet:
		s.handleOpenAPITokenList(w, r)
	case http.MethodDelete:
		s.handleOpenAPITokenRevoke(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		(&adminError{http.StatusMethodNotAllowed, "METHOD", "仅 GET/POST/DELETE"}).write(w)
	}
}

// ---- 错误与 JSON 辅助（与 api 包同型，包内独立避免反向依赖） ----

type adminError struct {
	Status  int
	Code    string
	Message string
}

func (e *adminError) write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Code, "message": e.Message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- 鉴权（Bearer admin token，api_tokens 表 + admin scope，仅存 hash） ----

// requireAdmin 校验 Bearer 令牌：命中 api_tokens 且 status=active 且
// robot_id=adminScopeRobotID（admin scope）。失败 401（统一 INVALID_TOKEN，
// 不区分"不存在/已吊销/非 admin"以防探测面分类）。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *sqlite.APIToken {
	auth := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		(&adminError{http.StatusUnauthorized, "INVALID_TOKEN", "缺少 Authorization: Bearer <token>（管理面强制鉴权）"}).write(w)
		return nil
	}
	t, found, err := s.db.GetAPIToken(r.Context(), sha256Hex(raw))
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return nil
	}
	if !found || t.Status != "active" || t.RobotID != adminScopeRobotID {
		(&adminError{http.StatusUnauthorized, "INVALID_TOKEN", "token 无效、已吊销或非管理面令牌"}).write(w)
		return nil
	}
	_ = s.db.TouchAPIToken(r.Context(), t.TokenHash, s.now().UTC())
	return &t
}

// IssueAdminToken 签发管理面令牌（随机 256-bit hex，仅存 SHA-256 hash，原始值
// 一次返回）。v0.1 无 CLI 面：运维经一次 SQL 落 hash 或由集成方在装配处调用
// （admin/README「首个管理令牌」）；W-M5 RBAC 车道接管完整生命周期。
func (s *Server) IssueAdminToken(ctx context.Context) (string, error) {
	raw, err := wire.RandomHex(32)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if err := s.db.InsertAPIToken(ctx, sha256Hex(raw), adminScopeRobotID, "default", now); err != nil {
		return "", err
	}
	if err := s.db.InsertAudit(ctx, nil, "admin_token_issued", adminScopeRobotID, "管理面令牌签发（仅存 hash）", now); err != nil {
		return "", err
	}
	s.log.Printf("admin: 签发管理面令牌（robot_id=%s，仅存 hash）", adminScopeRobotID)
	return raw, nil
}

// ---- POST /api/v1/admin/pairing-codes ----

type pairingCodeResponse struct {
	PairingCode string `json:"pairingCode"`
	ExpiresAt   int64  `json:"expiresAt"`
	TTLSeconds  int    `json:"ttlSeconds"`
}

// handlePairingCodes 签发配对码（替代 DB 手工 INSERT 引导）。派生与
// api.Server.CreatePairingCode 同式（随机字节 mod 10 → 6 位数字），落
// pairing_codes 表（仅 SHA-256 hash），由 /api/v2/pair 的
// ConsumePairingCode 原子消费（单次有效 + TTL 10min，spec §5.1.1）。
func (s *Server) handlePairingCodes(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	// 请求体无参数（TTL 冻结 10min 不可配）：空体放行；非空须为合法 JSON 对象
	// 且不含未定义字段（空结构体 + DisallowUnknownFields——map 解码不触发
	// 未知字段拒绝，故不用 map）。
	if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10)); len(bytes.TrimSpace(body)) > 0 {
		var extra struct{}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&extra); err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为空或无字段 JSON 对象（本端点无参数）"}).write(w)
			return
		}
	}
	// 随机源失败直接透传，绝不兜底固定值（AUD-M2-04 纪律）。
	hexStr, err := wire.RandomHex(6)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "KEYGEN", err.Error()}).write(w)
		return
	}
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "KEYGEN", err.Error()}).write(w)
		return
	}
	digits := make([]byte, 6)
	for i := 0; i < 6; i++ {
		digits[i] = byte('0') + raw[i]%10
	}
	code := string(digits)
	now := s.now().UTC()
	expires := now.Add(pairingCodeTTL)
	// H-1（安全审计 TOP5）：签发新码即把全部未使用旧码置 used（单活动码语义，
	// 压缩爆破面；与 api.Server.CreatePairingCode 同一 store 方法，防语义漂移）。
	if _, err := s.db.InvalidateUnusedPairingCodes(r.Context(), now); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if err := s.db.CreatePairingCode(r.Context(), sha256Hex(code), expires); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if err := s.db.InsertAudit(r.Context(), nil, "admin_pairing_code_issued", adminScopeRobotID,
		fmt.Sprintf("配对码签发（仅存 hash，TTL %s，单次有效；未使用旧码已一并置 used，H-1 单活动码）", pairingCodeTTL), now); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	writeJSON(w, http.StatusCreated, pairingCodeResponse{
		PairingCode: code,
		ExpiresAt:   expires.UnixMilli(),
		TTLSeconds:  int(pairingCodeTTL / time.Second),
	})
}

// ---- GET /api/v1/admin/devices ----

type deviceItem struct {
	DeviceID      string `json:"deviceId"`
	RobotID       string `json:"robotId"`
	DeviceName    string `json:"deviceName"`
	Status        string `json:"status"`
	TokenVersion  int64  `json:"tokenVersion"`
	Epoch         int64  `json:"epoch"`
	PairedAtMs    int64  `json:"pairedAtMs"`
	UpdatedAtMs   int64  `json:"updatedAtMs"`
	Online        bool   `json:"online"`
	LastSessionMs int64  `json:"lastSessionMs"`
}

// handleDevices 设备列表 + 在线状态（online 经 Deps.Online 探测；nil = 未接线
// 恒 false）。robotId 为空 = 配对面绑定被重配对清空（W-M3R 运维注意 1），
// 如实展示不代填。lastSessionMs = 最近一次会话痕迹（device_sessions 的
// opened_at/created_at 最大值，≈最近 hello）——devices 表无逐帧心跳列，
// 此为最近活跃证据的诚实口径（型号/系统版本字段配对面未上报，UI 如实标未上报）。
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	rows, err := s.db.QueryDump(r.Context(), `
SELECT device_id, COALESCE(robot_id,''), device_name, status, token_version, epoch, paired_at, updated_at
FROM devices ORDER BY paired_at`)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]deviceItem, 0, len(rows))
	for _, row := range rows {
		d := deviceItem{
			DeviceID:      row[0],
			RobotID:       row[1],
			DeviceName:    row[2],
			Status:        row[3],
			Online:        s.online(row[0]),
			LastSessionMs: s.lastSessionMs(r.Context(), row[0]),
		}
		d.TokenVersion, _ = strconv.ParseInt(row[4], 10, 64)
		d.Epoch, _ = strconv.ParseInt(row[5], 10, 64)
		d.PairedAtMs, _ = strconv.ParseInt(row[6], 10, 64)
		d.UpdatedAtMs, _ = strconv.ParseInt(row[7], 10, 64)
		items = append(items, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": items, "count": len(items)})
}

// ---- GET /api/v1/admin/commands（指令审计流，含 dryRun 标记） ----

type commandItem struct {
	CommandID     string `json:"commandId"`
	Target        string `json:"target"`
	CommandType   int    `json:"type"`
	State         string `json:"state"`
	RiskClass     string `json:"riskClass"`
	DryRun        bool   `json:"dryRun"`
	CreatedAtMs   int64  `json:"createdAtMs"`
	TerminalAtMs  int64  `json:"terminalAtMs"`
	TerminalState string `json:"terminalState"`
}

// handleCommands 指令审计流（commands 表只读投影，时间倒序）。
// 查询参数：limit（默认 50，1..500）、offset（分页，默认 0）、state（可选精确
// 过滤）、type（可选精确过滤）、target（可选）。dryRun=FR-54 干跑标记：同引擎
// 同路径仅产出报告，绝不下发设备。
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			(&adminError{http.StatusBadRequest, "INVALID_LIMIT", "limit 须为 1..500 整数"}).write(w)
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			(&adminError{http.StatusBadRequest, "INVALID_OFFSET", "offset 须为 ≥0 整数"}).write(w)
			return
		}
		offset = n
	}
	var conds []string
	var args []any
	if st := q.Get("state"); st != "" {
		conds = append(conds, "state = ?")
		args = append(args, st)
	}
	if tg := q.Get("target"); tg != "" {
		conds = append(conds, "target = ?")
		args = append(args, tg)
	}
	if tv := q.Get("type"); tv != "" {
		n, err := strconv.Atoi(tv)
		if err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_TYPE", "type 须为整数"}).write(w)
			return
		}
		conds = append(conds, "command_type = ?")
		args = append(args, n)
	}
	where := ""
	if len(conds) > 0 {
		where = ` WHERE ` + strings.Join(conds, " AND ")
	}
	total := 0
	if trows, err := s.db.QueryDump(r.Context(), `SELECT COUNT(*) FROM commands`+where, args...); err == nil && len(trows) == 1 {
		total, _ = strconv.Atoi(trows[0][0])
	}
	query := `
SELECT command_id, target, command_type, state, risk_class, dry_run, created_at, terminal_at, terminal_state
FROM commands` + where + ` ORDER BY created_at DESC, command_id LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryDump(r.Context(), query, args...)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]commandItem, 0, len(rows))
	for _, row := range rows {
		c := commandItem{CommandID: row[0], Target: row[1], State: row[3], RiskClass: row[4], DryRun: row[5] == "1"}
		c.CommandType, _ = strconv.Atoi(row[2])
		c.CreatedAtMs, _ = strconv.ParseInt(row[6], 10, 64)
		c.TerminalAtMs, _ = strconv.ParseInt(row[7], 10, 64) // NULL → "" → 0
		c.TerminalState = row[8]
		items = append(items, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": items, "count": len(items), "total": total, "limit": limit, "offset": offset})
}

// ---- GET /api/v1/admin/approvals ----

type approvalItem struct {
	ApprovalID  string `json:"approvalId"`
	Tenant      string `json:"tenant"`
	Actor       string `json:"actor"`
	Target      string `json:"target"`
	CommandType int    `json:"type"`
	RiskClass   string `json:"riskClass"`
	Status      string `json:"status"`
	Approver    string `json:"approver"`
	Reason      string `json:"reason"`
	Recurring   bool   `json:"recurring"`
	CreatedAtMs int64  `json:"createdAtMs"`
	DecidedAtMs int64  `json:"decidedAtMs"`
}

// handleApprovals 审批列表（默认 pending；?status= 过滤，pending|approved|rejected|consumed）。
// 支撑 UI 审批按钮的数据面（四端点之外的最小必要增量，README 记录理由）。
func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	switch status {
	case "pending", "approved", "rejected", "consumed":
	default:
		(&adminError{http.StatusBadRequest, "INVALID_STATUS", "status 须为 pending|approved|rejected|consumed"}).write(w)
		return
	}
	list, err := s.db.ApprovalsByStatus(r.Context(), status)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]approvalItem, 0, len(list))
	for _, a := range list {
		items = append(items, approvalItem{
			ApprovalID: a.ApprovalID, Tenant: a.Tenant, Actor: a.Actor, Target: a.Target,
			CommandType: a.CommandType, RiskClass: a.RiskClass, Status: a.Status,
			Approver: a.Approver, Reason: a.Reason, Recurring: a.Recurring,
			CreatedAtMs: a.CreatedAt.UnixMilli(),
			DecidedAtMs: decidedMs(a.DecidedAt),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": items, "status": status, "count": len(items)})
}

// decidedMs 把 DecidedAt（sql.NullTime）转为毫秒；未决议（NULL）→ 0。
func decidedMs(t sql.NullTime) int64 {
	if !t.Valid {
		return 0
	}
	return t.Time.UnixMilli()
}

// ---- POST /api/v1/admin/approvals/{approvalId}/decision ----

type decisionRequest struct {
	Approver string `json:"approver"`
	Decision string `json:"decision"` // approve | reject
	Reason   string `json:"reason"`
}

// handleApprovalDecision 审批决议（L2 approver 面）。决议语义全部复用
// scheduler.DecideApproval（guard.go）：approver 必填且 ≠ actor（FR-53 独立
// approver，自批拒绝 403）、仅 pending 可决议（重复决议 409，本 handler 预检
// 映射 + 服务端原子 UPDATE 兜底）、决议入审计（SR-12）。
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	approvalID := r.PathValue("approvalId")
	if approvalID == "" {
		(&adminError{http.StatusBadRequest, "INVALID_APPROVAL_ID", "approvalId 不能为空"}).write(w)
		return
	}
	var req decisionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体解析失败/含未定义字段（须为 {approver, decision, reason}）"}).write(w)
		return
	}
	req.Approver = strings.TrimSpace(req.Approver)
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Approver == "" || len(req.Approver) > 64 {
		(&adminError{http.StatusBadRequest, "INVALID_APPROVER", "approver 必填且 ≤64 字符"}).write(w)
		return
	}
	if len(req.Reason) > 512 {
		(&adminError{http.StatusBadRequest, "INVALID_REASON", "reason ≤512 字符"}).write(w)
		return
	}
	var approve bool
	switch req.Decision {
	case "approve":
		approve = true
	case "reject":
		approve = false
	default:
		(&adminError{http.StatusBadRequest, "INVALID_DECISION", "decision 须为 approve|reject"}).write(w)
		return
	}
	// 预检映射 404/409（决议原子性仍由 DecideApproval 的条件 UPDATE 保证）。
	a, ok, err := s.db.GetApproval(r.Context(), approvalID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if !ok {
		(&adminError{http.StatusNotFound, "APPROVAL_NOT_FOUND", "审批 " + approvalID + " 不存在"}).write(w)
		return
	}
	if a.Status != "pending" {
		(&adminError{http.StatusConflict, "APPROVAL_ALREADY_DECIDED",
			fmt.Sprintf("审批 %s 状态=%s（仅 pending 可决议；重复决议拒绝）", approvalID, a.Status)}).write(w)
		return
	}
	if err := s.pipeline.DecideApproval(r.Context(), approvalID, req.Approver, approve, req.Reason); err != nil {
		var ge *scheduler.GuardError
		if errors.As(err, &ge) {
			// 自批拒绝（APPROVER_CONFLICT）等门冲突 → 403。
			(&adminError{http.StatusForbidden, ge.Code, ge.Msg}).write(w)
			return
		}
		(&adminError{http.StatusInternalServerError, "DECIDE", err.Error()}).write(w)
		return
	}
	status := "rejected"
	if approve {
		status = "approved"
	}
	s.log.Printf("admin: 审批 %s 决议 %s（approver=%s actor=%s）", approvalID, status, req.Approver, a.Actor)
	writeJSON(w, http.StatusOK, map[string]any{
		"approvalId": approvalID, "status": status, "approver": req.Approver, "actor": a.Actor,
	})
}

// ---- /api/v1/admin/robots/{robotId}/openapi-tokens（兼容面令牌生命周期） ----
//
// 管理后台日常使用闭环（W-M5）：配对设备 → 此处签发 robot 兼容面令牌 → 用户持
// Bearer token 打 /openapi/v1/* 全量面。签发纪律与 api.IssueOpenAPIToken 同源：
// 随机 256-bit hex、仅存 SHA-256 hash、原始值一次返回；保留名 admin 硬拒
//（CR ADM-1：不得为名叫 admin 的 v1 robot 铸出第二个 admin-scope 令牌）。

type openapiTokenResponse struct {
	Token     string `json:"token"` // 原始值仅此一次返回（仅存 hash）
	RobotID   string `json:"robotId"`
	Tenant    string `json:"tenant"`
	CreatedMs int64  `json:"createdMs"`
}

// handleOpenAPITokenIssue POST /api/v1/admin/robots/{robotId}/openapi-tokens
// body 可选 {"tenant":"..."}；空体 / {} / tenant 留空 → 回落 default（CR ADM-1：
// UI 留空占位符「default」即默认租户，不再 400）。仅体解析失败才 400。
func (s *Server) handleOpenAPITokenIssue(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	robotID := r.PathValue("robotId")
	if robotID == "" || robotID == adminScopeRobotID {
		(&adminError{http.StatusForbidden, "ROBOT_FORBIDDEN",
			"robotId 为空或管理面保留名 admin——不得签发兼容面令牌（CR ADM-1）"}).write(w)
		return
	}
	tenant := "default"
	if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10)); len(bytes.TrimSpace(body)) > 0 {
		var req struct {
			Tenant string `json:"tenant"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			(&adminError{http.StatusBadRequest, "INVALID_BODY", "请求体须为 JSON 对象 {tenant}（tenant 留空 = 默认租户 default）"}).write(w)
			return
		}
		if t := strings.TrimSpace(req.Tenant); t != "" {
			tenant = t
		}
	}
	raw, err := wire.RandomHex(32)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "KEYGEN", err.Error()}).write(w)
		return
	}
	now := s.now().UTC()
	if err := s.db.InsertAPIToken(r.Context(), sha256Hex(raw), robotID, tenant, now); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if err := s.db.InsertAudit(r.Context(), nil, "admin_openapi_token_issued", robotID,
		fmt.Sprintf("兼容面令牌签发 tenant=%s（仅存 hash，原始值一次返回）", tenant), now); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	s.log.Printf("admin: 签发兼容面令牌 robot=%s tenant=%s（仅存 hash）", robotID, tenant)
	writeJSON(w, http.StatusCreated, openapiTokenResponse{Token: raw, RobotID: robotID, Tenant: tenant, CreatedMs: now.UnixMilli()})
}

// handleOpenAPITokenList GET /api/v1/admin/robots/{robotId}/openapi-tokens
// 只读投影：hash 前缀 12 hex + 状态/时间（原始值不可再生，永不出现在响应）。
func (s *Server) handleOpenAPITokenList(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	robotID := r.PathValue("robotId")
	rows, err := s.db.QueryDump(r.Context(), `
SELECT substr(token_hash,1,12), tenant, status, created_ms, COALESCE(last_used_ms,0)
FROM api_tokens WHERE robot_id = ? ORDER BY created_ms DESC`, robotID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		created, _ := strconv.ParseInt(row[3], 10, 64)
		used, _ := strconv.ParseInt(row[4], 10, 64)
		item := map[string]any{
			"hashPrefix": row[0], "tenant": row[1], "status": row[2], "createdMs": created,
		}
		if used > 0 {
			item["lastUsedMs"] = used
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"robotId": robotID, "tokens": items, "count": len(items)})
}

// handleOpenAPITokenRevoke DELETE /api/v1/admin/robots/{robotId}/openapi-tokens
// 吊销该 robot 全部 active 兼容面令牌（旋转令牌的运维路径；管理面令牌不受影响
// ——其 robot_id 为保留名 admin，不匹配业务 robotId）。
func (s *Server) handleOpenAPITokenRevoke(w http.ResponseWriter, r *http.Request) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	robotID := r.PathValue("robotId")
	if robotID == "" || robotID == adminScopeRobotID {
		(&adminError{http.StatusForbidden, "ROBOT_FORBIDDEN",
			"robotId 为空或管理面保留名 admin——兼容面吊销不适用于管理面令牌"}).write(w)
		return
	}
	res, err := s.db.Exec(r.Context(),
		`UPDATE api_tokens SET status='revoked' WHERE robot_id = ? AND status='active'`, robotID)
	if err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	revoked, _ := res.RowsAffected()
	now := s.now().UTC()
	if err := s.db.InsertAudit(r.Context(), nil, "admin_openapi_token_revoked", robotID,
		fmt.Sprintf("吊销 active 兼容面令牌 %d 枚", revoked), now); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"robotId": robotID, "revoked": revoked})
}
