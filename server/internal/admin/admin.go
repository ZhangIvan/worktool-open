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
}

// Server 为管理端最小面服务。
type Server struct {
	db       *sqlite.DB
	pipeline *scheduler.Service
	now      func() time.Time
	log      Logger
	online   func(target string) bool
	uiDir    string
}

// New 构建管理端服务（缺省 Now/Log 注入默认值）。
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
	return &Server{db: d.DB, pipeline: d.Pipeline, now: now, log: lg, online: online, uiDir: d.UIDir}
}

// Register 把管理端路由挂到 mux（Go 1.22 方法+路径模式）。
// 集成方在 server 主装配处调用一次；未挂载时本包端点不可达（默认不暴露）。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/pairing-codes", s.handlePairingCodes)
	mux.HandleFunc("GET /api/v1/admin/devices", s.handleDevices)
	mux.HandleFunc("GET /api/v1/admin/commands", s.handleCommands)
	mux.HandleFunc("GET /api/v1/admin/approvals", s.handleApprovals)
	mux.HandleFunc("POST /api/v1/admin/approvals/{approvalId}/decision", s.handleApprovalDecision)
	if s.uiDir != "" {
		// GET /admin → /admin/（相对解析）；/admin/ 下静态文件（index.html 等）。
		mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
		})
		mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServer(http.Dir(s.uiDir))))
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
	if err := s.db.CreatePairingCode(r.Context(), sha256Hex(code), expires); err != nil {
		(&adminError{http.StatusInternalServerError, "STORE", err.Error()}).write(w)
		return
	}
	if err := s.db.InsertAudit(r.Context(), nil, "admin_pairing_code_issued", adminScopeRobotID,
		fmt.Sprintf("配对码签发（仅存 hash，TTL %s，单次有效）", pairingCodeTTL), now); err != nil {
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
	DeviceID     string `json:"deviceId"`
	RobotID      string `json:"robotId"`
	DeviceName   string `json:"deviceName"`
	Status       string `json:"status"`
	TokenVersion int64  `json:"tokenVersion"`
	Epoch        int64  `json:"epoch"`
	PairedAtMs   int64  `json:"pairedAtMs"`
	UpdatedAtMs  int64  `json:"updatedAtMs"`
	Online       bool   `json:"online"`
}

// handleDevices 设备列表 + 在线状态（online 经 Deps.Online 探测；nil = 未接线
// 恒 false）。robotId 为空 = 配对面绑定被重配对清空（W-M3R 运维注意 1），
// 如实展示不代填。
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
			DeviceID:   row[0],
			RobotID:    row[1],
			DeviceName: row[2],
			Status:     row[3],
			Online:     s.online(row[0]),
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
// 查询参数：limit（默认 50，1..500）、state（可选精确过滤）、target（可选）。
// dryRun=FR-54 干跑标记：同引擎同路径仅产出报告，绝不下发设备。
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
	query := `
SELECT command_id, target, command_type, state, risk_class, dry_run, created_at, terminal_at, terminal_state
FROM commands`
	var args []any
	if st := q.Get("state"); st != "" {
		query += ` WHERE state = ?`
		args = append(args, st)
		if tg := q.Get("target"); tg != "" {
			query += ` AND target = ?`
			args = append(args, tg)
		}
	} else if tg := q.Get("target"); tg != "" {
		query += ` WHERE target = ?`
		args = append(args, tg)
	}
	query += ` ORDER BY created_at DESC, command_id LIMIT ?`
	args = append(args, limit)
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
	writeJSON(w, http.StatusOK, map[string]any{"commands": items, "count": len(items)})
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
