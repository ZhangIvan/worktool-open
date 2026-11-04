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

// approverScopeRobotID 为 api_tokens.robot_id 的第二保留值，编码 approver scope
// （P0-3 审批身份车道：决议面独立身份令牌，IssueApproverToken 签发）。决议面
// 只认 approver 令牌——admin 令牌决议=403 APPROVER_SCOPE_REQUIRED，结构性消灭
// 「管理令牌自批」；反向 approver 令牌不得进提交管线（requireAdminSubmit）。
// 保留名硬拒与 admin 同构：兼容面签发/寻址两侧拒 sqlite.ReservedApproverScopeRobotID。
const approverScopeRobotID = sqlite.ReservedApproverScopeRobotID

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

// adminCSPHeader 为管理面响应统一 CSP（L-1）：default-src 'none' 兜底禁外联，
// script/style 放行内联（单页内联形态），connect-src 'self' 放行管理台自身同源
// fetch() 数据面（RC 复核 RELEASE_BLOCKER①：缺 connect-src 会经回落拦截全部
// 同源数据请求，真实浏览器实证 FETCH_BLOCKED）。外联仍全禁（无 http(s) 源）。
const adminCSPHeader = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'"

// withCSP 为管理面统一响应中间件（L-1）：头在写响应前设置，覆盖 200/302/4xx/5xx
// 全部响应。Register 经包内 amux 整体包出，/api/v1/admin/* 与 /admin/ 全覆盖。
func withCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", adminCSPHeader)
		next.ServeHTTP(w, r)
	})
}

// Register 把管理端路由挂到 mux（Go 1.22 方法+路径模式）。
// 集成方在 server 主装配处调用一次；未挂载时本包端点不可达（默认不暴露）。
func (s *Server) Register(mux *http.ServeMux) {
	// L-1：路由先挂包内 amux（pattern 与行为不变），再整体经 withCSP 包出——
	// 管理面所有响应（含 404/405）统一带 CSP，后续新增路由自动继承。
	amux := http.NewServeMux()
	// —— v0.1 最小面（原四端点 + 审批/令牌生命周期，行为不变）——
	amux.HandleFunc("POST /api/v1/admin/pairing-codes", s.handlePairingCodes)
	amux.HandleFunc("GET /api/v1/admin/devices", s.handleDevices)
	amux.HandleFunc("GET /api/v1/admin/commands", s.handleCommands)
	amux.HandleFunc("GET /api/v1/admin/approvals", s.handleApprovals)
	amux.HandleFunc("POST /api/v1/admin/approvals/{approvalId}/decision", s.handleApprovalDecision)
	amux.HandleFunc("/api/v1/admin/robots/{robotId}/openapi-tokens", s.handleOpenAPITokens)
	// —— W-P0 完整管理后台数据面（console.go）——
	amux.HandleFunc("GET /api/v1/admin/overview", s.handleOverview)
	amux.HandleFunc("POST /api/v1/admin/devices/{deviceId}/{action}", s.handleDeviceAction)
	amux.HandleFunc("POST /api/v1/admin/commands", s.handleCommandSubmit)
	amux.HandleFunc("GET /api/v1/admin/commands/{commandId}", s.handleCommandDetail)
	amux.HandleFunc("GET /api/v1/admin/queue", s.handleQueueList)
	amux.HandleFunc("DELETE /api/v1/admin/queue", s.handleQueueClear)
	amux.HandleFunc("DELETE /api/v1/admin/queue/{commandId}", s.handleQueueRemove)
	amux.HandleFunc("POST /api/v1/admin/queue/{commandId}/priority", s.handleQueuePriority)
	amux.HandleFunc("/api/v1/admin/callbacks", s.handleCallbacks)
	amux.HandleFunc("GET /api/v1/admin/catalog", s.handleCatalog)
	amux.HandleFunc("POST /api/v1/admin/callbacks/test", s.handleCallbackTest)
	amux.HandleFunc("GET /api/v1/admin/callbacks/logs", s.handleCallbackLogs)
	amux.HandleFunc("GET /api/v1/admin/logs/executions", s.handleExecutionLogs)
	amux.HandleFunc("GET /api/v1/admin/logs/audit", s.handleAuditLogs)
	amux.HandleFunc("GET /api/v1/admin/settings", s.handleSettings)
	amux.HandleFunc("PUT /api/v1/admin/settings/guard", s.handleGuardSettings)
	amux.HandleFunc("PUT /api/v1/admin/settings/encryption", s.handleEncryptionSettings)
	amux.HandleFunc("PUT /api/v1/admin/settings/event-callbacks", s.handleEventCallbackSettings)
	amux.HandleFunc("POST /api/v1/admin/breaker/reset", s.handleBreakerReset)
	amux.HandleFunc("GET /api/v1/admin/risk/{type}", s.handleRiskQuery)
	amux.HandleFunc("/api/v1/admin/whitelist", s.handleWhitelist)
	amux.HandleFunc("GET /api/v1/admin/tokens", s.handleTokensList)
	// —— W-ops cron 定时任务管理面（cron_admin.go：列表/创建/预览/启停/删除）——
	amux.HandleFunc("GET /api/v1/admin/cron-jobs", s.handleCronJobList)
	amux.HandleFunc("POST /api/v1/admin/cron-jobs", s.handleCronJobCreate)
	amux.HandleFunc("GET /api/v1/admin/cron-jobs/preview", s.handleCronJobPreview)
	amux.HandleFunc("POST /api/v1/admin/cron-jobs/{jobId}/enabled", s.handleCronJobEnabled)
	amux.HandleFunc("DELETE /api/v1/admin/cron-jobs/{jobId}", s.handleCronJobDelete)
	if s.uiDir != "" {
		// GET /admin → /admin/（相对解析）；/admin/ 下静态文件（index.html 等）。
		amux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/", http.StatusFound)
		})
		amux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServer(http.Dir(s.uiDir))))
	}
	mux.Handle("/api/v1/admin/", withCSP(amux))
	mux.Handle("/admin", withCSP(amux))
	mux.Handle("/admin/", withCSP(amux))
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

// storeError 为 STORE 错误统一出口（L-2）：原始 err 只进服务端日志（保留排障
// 细节），对外固定文案——表结构/路径/驱动等存储细节不出网。错误码仍为 STORE
// （运维可分类），仅 message 固定。
func (s *Server) storeError(w http.ResponseWriter, err error) {
	s.log.Printf("admin: STORE 错误（原文仅入日志）: %v", err)
	(&adminError{http.StatusInternalServerError, "STORE", "存储错误（详情见服务端日志）"}).write(w)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- 鉴权（Bearer token，api_tokens 表 + scope=robot_id 保留值，仅存 hash） ----

// requireScope 统一 Bearer scope 鉴权（requireAdmin/requireApprover/
// requireAdminSubmit 单一来源，防三处语义漂移）：
//   - 缺失 / 不存在 / 已吊销 → 401 INVALID_TOKEN（统一不分类，防探测面分类）；
//   - 命中且 active 且 scope 匹配 → 放行（TouchAPIToken 留 last_used）；
//   - 命中且 active 但为他面保留 scope（admin/approver）且 forbiddenCode 非空
//     → 403 forbiddenCode（越权显式化，P0-3 scope 隔离：决议面拒 admin、
//     提交面拒 approver）；业务 robot 令牌维持统一 401（CR ADM-1 口径不变）；
//   - forbiddenCode 为空 = 旧语义（scope 不符并入 401）——既有管理端点不因
//     本车道改行为，仅决议/提交两处严格化。
func (s *Server) requireScope(w http.ResponseWriter, r *http.Request, scope, forbiddenCode, forbiddenMsg string) *sqlite.APIToken {
	auth := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		(&adminError{http.StatusUnauthorized, "INVALID_TOKEN", "缺少 Authorization: Bearer <token>（管理面强制鉴权）"}).write(w)
		return nil
	}
	t, found, err := s.db.GetAPIToken(r.Context(), sha256Hex(raw))
	if err != nil {
		s.storeError(w, err)
		return nil
	}
	if !found || t.Status != "active" {
		(&adminError{http.StatusUnauthorized, "INVALID_TOKEN", "token 无效或已吊销"}).write(w)
		return nil
	}
	if t.RobotID == scope {
		_ = s.db.TouchAPIToken(r.Context(), t.TokenHash, s.now().UTC())
		return &t
	}
	if forbiddenCode != "" && (t.RobotID == adminScopeRobotID || t.RobotID == approverScopeRobotID) {
		(&adminError{http.StatusForbidden, forbiddenCode, forbiddenMsg}).write(w)
		return nil
	}
	(&adminError{http.StatusUnauthorized, "INVALID_TOKEN", "token 无效、已吊销或非本面令牌"}).write(w)
	return nil
}

// requireAdmin 校验 Bearer 令牌为 admin scope（既有端点旧语义：scope 不符并入
// 401，防探测面分类）。
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *sqlite.APIToken {
	return s.requireScope(w, r, adminScopeRobotID, "", "")
}

// requireApprover 校验 Bearer 令牌为 approver scope（P0-3 决议面专用）：命中且
// active 的 admin 令牌 → 403 APPROVER_SCOPE_REQUIRED——管理令牌结构性失去决议
// 权（消灭自批）；决议只认 IssueApproverToken 签发的独立令牌。
func (s *Server) requireApprover(w http.ResponseWriter, r *http.Request) *sqlite.APIToken {
	return s.requireScope(w, r, approverScopeRobotID, "APPROVER_SCOPE_REQUIRED",
		"决议须 approver scope 令牌（-issue-approver-token 签发）；管理面令牌不得决议（P0-3 结构性消灭自批）")
}

// requireAdminSubmit 指令提交面鉴权（P0-3 scope 隔离另一向）：approver 决议
// 令牌 → 403 ADMIN_SCOPE_REQUIRED，决议身份不得操作指令管线；业务 robot 令牌
// 仍 401。
func (s *Server) requireAdminSubmit(w http.ResponseWriter, r *http.Request) *sqlite.APIToken {
	return s.requireScope(w, r, adminScopeRobotID, "ADMIN_SCOPE_REQUIRED",
		"指令提交须管理面令牌；approver 决议令牌不得进入提交管线（P0-3 scope 隔离）")
}

// IssueAdminToken 签发管理面令牌（随机 256-bit hex，仅存 SHA-256 hash，原始值
// 一次返回）。v0.1 无 CLI 面：运维经一次 SQL 落 hash 或由集成方在装配处调用
// （admin/README「首个管理令牌」）；W-M5 RBAC 车道接管完整生命周期。
func (s *Server) IssueAdminToken(ctx context.Context) (string, error) {
	return s.issueScopedToken(ctx, adminScopeRobotID, "admin_token_issued", "管理面令牌签发（仅存 hash）")
}

// IssueApproverToken 签发审批决议令牌（approver scope；P0-3：决议面独立身份，
// 与管理令牌分持——决议人安全身份 = "approver-token:" + hash 前 16 hex，admin
// 令牌在决议面 403 结构性消灭自批）。签发纪律与 IssueAdminToken 同源。
func (s *Server) IssueApproverToken(ctx context.Context) (string, error) {
	return s.issueScopedToken(ctx, approverScopeRobotID, "approver_token_issued", "审批决议令牌签发（approver scope，仅存 hash）")
}

// issueScopedToken 为 scope 令牌签发单一来源（随机 256-bit hex，仅存 SHA-256
// hash，原始值一次返回；签发入审计 SR-12）。
func (s *Server) issueScopedToken(ctx context.Context, scope, auditKind, auditMsg string) (string, error) {
	raw, err := wire.RandomHex(32)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	if err := s.db.InsertAPIToken(ctx, sha256Hex(raw), scope, "default", now); err != nil {
		return "", err
	}
	if err := s.db.InsertAudit(ctx, nil, auditKind, scope, auditMsg, now); err != nil {
		return "", err
	}
	s.log.Printf("admin: 签发 scope 令牌（robot_id=%s，仅存 hash）", scope)
	return raw, nil
}

// scopedIdentity 以令牌 hash 前 16 hex 构造不可伪造的安全身份串（自由文本仅作
// 展示后缀拼入，P0-3：身份 = 令牌，非请求体可声明字段）。hash 不足 16 位（手工
// 落库的畸形行）取全长，不 panic。
func scopedIdentity(prefix, tokenHash, display string) string {
	h := tokenHash
	if len(h) > 16 {
		h = h[:16]
	}
	id := prefix + h
	if display != "" {
		id += " (" + display + ")"
	}
	return id
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
		s.storeError(w, err)
		return
	}
	if err := s.db.CreatePairingCode(r.Context(), sha256Hex(code), expires); err != nil {
		s.storeError(w, err)
		return
	}
	if err := s.db.InsertAudit(r.Context(), nil, "admin_pairing_code_issued", adminScopeRobotID,
		fmt.Sprintf("配对码签发（仅存 hash，TTL %s，单次有效；未使用旧码已一并置 used，H-1 单活动码）", pairingCodeTTL), now); err != nil {
		s.storeError(w, err)
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
		s.storeError(w, err)
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
		s.storeError(w, err)
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
		s.storeError(w, err)
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

// handleApprovalDecision 审批决议（L2 approver 面，P0-3 审批身份车道）。鉴权 =
// requireApprover（只认 approver scope 令牌；admin 令牌决议=403
// APPROVER_SCOPE_REQUIRED，结构性消灭「管理令牌自批」）。决议人安全身份 =
// "approver-token:" + 令牌 hash 前 16 hex，请求体自由文本 approver 降级为展示
// 后缀拼入该串（身份来自令牌而非请求体可声明字段）；actor 侧对称改造见
// handleCommandSubmit（"admin-token:" + hash 前 16），两侧前缀恒异 →
// DecideApproval 的 approver≠actor 文本比较天然成立，FR-53 独立 approver 语义
// 保持。决议语义全部复用 scheduler.DecideApproval（guard.go）：仅 pending 可
// 决议（重复决议 409，本 handler 预检映射 + 服务端原子 UPDATE 兜底）、决议入
// 审计（SR-12）。
func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	tok := s.requireApprover(w, r)
	if tok == nil {
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
	// 自由文本 approver 可选（留空=仅以令牌身份留痕）；填写时仅作展示后缀，≤64 字符。
	if len(req.Approver) > 64 {
		(&adminError{http.StatusBadRequest, "INVALID_APPROVER", "approver 留空=仅以决议令牌身份留痕；填写时 ≤64 字符（展示后缀）"}).write(w)
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
	approver := scopedIdentity("approver-token:", tok.TokenHash, req.Approver)
	// 预检映射 404/409（决议原子性仍由 DecideApproval 的条件 UPDATE 保证）。
	a, ok, err := s.db.GetApproval(r.Context(), approvalID)
	if err != nil {
		s.storeError(w, err)
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
	if err := s.pipeline.DecideApproval(r.Context(), approvalID, approver, approve, req.Reason); err != nil {
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
	s.log.Printf("admin: 审批 %s 决议 %s（approver=%s actor=%s）", approvalID, status, approver, a.Actor)
	writeJSON(w, http.StatusOK, map[string]any{
		"approvalId": approvalID, "status": status, "approver": approver, "actor": a.Actor,
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
	if robotID == "" || robotID == adminScopeRobotID || robotID == approverScopeRobotID {
		(&adminError{http.StatusForbidden, "ROBOT_FORBIDDEN",
			"robotId 为空或保留名（admin=管理面 / approver=决议面）——不得签发兼容面令牌（CR ADM-1 / P0-3）"}).write(w)
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
		s.storeError(w, err)
		return
	}
	if err := s.db.InsertAudit(r.Context(), nil, "admin_openapi_token_issued", robotID,
		fmt.Sprintf("兼容面令牌签发 tenant=%s（仅存 hash，原始值一次返回）", tenant), now); err != nil {
		s.storeError(w, err)
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
		s.storeError(w, err)
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
	if robotID == "" || robotID == adminScopeRobotID || robotID == approverScopeRobotID {
		(&adminError{http.StatusForbidden, "ROBOT_FORBIDDEN",
			"robotId 为空或保留名（admin=管理面 / approver=决议面）——兼容面吊销不适用于 scope 令牌"}).write(w)
		return
	}
	res, err := s.db.Exec(r.Context(),
		`UPDATE api_tokens SET status='revoked' WHERE robot_id = ? AND status='active'`, robotID)
	if err != nil {
		s.storeError(w, err)
		return
	}
	revoked, _ := res.RowsAffected()
	now := s.now().UTC()
	if err := s.db.InsertAudit(r.Context(), nil, "admin_openapi_token_revoked", robotID,
		fmt.Sprintf("吊销 active 兼容面令牌 %d 枚", revoked), now); err != nil {
		s.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"robotId": robotID, "revoked": revoked})
}
