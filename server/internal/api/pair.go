// POST /api/v2/pair（spec §5.1）：配对码校验（6 位/TTL 10min/单次有效）、
// 注册设备公钥（Ed25519 签名 + X25519 临时分列两字段）、签发 deviceToken
// （256-bit hex，服务端仅存 SHA-256 hash，原始 token 仅一次返回 + 进程内存凭据）。
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"cn.worktool.open/server/internal/api/compat"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// pairingCodeTTL 配对码有效期（spec §5.1.1 冻结：10 分钟）。
const pairingCodeTTL = 10 * time.Minute

// H-1（安全审计 TOP5）pair 面防爆破参数（var 供测试注入；默认 = 审计口径；
// 与 compat K6 面包级 var 同构：装配后可改，判定时逐请求取值）。
var (
	// PairRatePerMin 单 IP 滚动窗口内允许的 pair 请求数（5 次/分钟）。
	PairRatePerMin = 5
	// PairRateWindow 滚动窗口长度。
	PairRateWindow = time.Minute
	// PairFailBanThreshold 连续失败（400 校验拒绝 / 403 PAIRING_CODE_INVALID）
	// 第 N 次封禁该 IP；配对成功即清零。≤0 = 禁用失败封禁。
	PairFailBanThreshold = 20
	// PairFailBanDuration 失败封禁冷却时长（15 分钟）。
	PairFailBanDuration = 15 * time.Minute
	// PairKeysCap 限流/失败表 entry 基数上限（fail-closed 拒新保旧）。
	PairKeysCap = 4096
)

// pairParams 逐请求取限流口径（var 注入语义与 compat K6 面同构）。
func pairParams() compat.IPParams {
	return compat.IPParams{
		Rate: PairRatePerMin, Window: PairRateWindow,
		BanThreshold: 0, // 超限封禁禁用：pair 面封禁语义由连续失败封禁承担
		BanDuration:  PairFailBanDuration,
		KeysCap:      PairKeysCap,
	}
}

// pairGuard 为 POST /api/v2/pair 的 per-IP 防爆破闸（H-1，安全审计 TOP5）：
//   - 限速：复用 compat ipLimiter 判定（AllowParams），PairRatePerMin（默认 5）
//     次 / PairRateWindow（默认 1 分钟）/ IP，超限 429；
//   - 封禁：连续 PairFailBanThreshold（默认 20）次失败（400 校验拒绝 / 403
//     PAIRING_CODE_INVALID）→ 该 IP PairFailBanDuration（默认 15 分钟）内直接
//     429（Retry-After 附剩余秒数）；配对成功（201）清零连续失败计数；429
//     （限速/封禁期拒绝）不计失败——只有真实触码尝试才计数；
//   - 内存纪律：失败表 keys-cap fail-closed（拒新保旧）+ 封禁过期 entry 惰性回收。
type pairGuard struct {
	ips   *compat.IPLimiter
	mu    sync.Mutex
	fails map[string]*pairFailEntry
}

type pairFailEntry struct {
	consec      int
	bannedUntil time.Time // 零值 = 未封
}

func newPairGuard() *pairGuard {
	return &pairGuard{ips: compat.NewIPLimiter(), fails: map[string]*pairFailEntry{}}
}

// allow 放行判定：失败封禁期内 → (false, true)；限速超限 → (false, false)。
func (g *pairGuard) allow(ip string, now time.Time) (ok, banned bool) {
	if g.banned(ip, now) {
		return false, true
	}
	ok, _ = g.ips.AllowParams(ip, now, pairParams())
	return ok, false
}

// fail 登记一次失败（400/403 路径调用）；连续达到 PairFailBanThreshold → 封禁。
// 封禁期内的新失败不延长冷却（计数冻结，防滚动封禁永久化）。
func (g *pairGuard) fail(ip string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.fails[ip]
	if !ok {
		if len(g.fails) >= PairKeysCap {
			g.expireLocked(now) // 先回收过期 entry 再试（fail-closed 拒新保旧）
			if len(g.fails) >= PairKeysCap {
				return
			}
		}
		e = &pairFailEntry{}
		g.fails[ip] = e
	}
	if !e.bannedUntil.IsZero() {
		if now.Before(e.bannedUntil) {
			return // 封禁期内：计数冻结
		}
		e.bannedUntil = time.Time{} // 冷却结束 = 干净重来
		e.consec = 0
	}
	e.consec++
	if PairFailBanThreshold > 0 && e.consec >= PairFailBanThreshold {
		e.bannedUntil = now.Add(PairFailBanDuration)
		e.consec = 0
	}
}

// pass 登记一次配对成功（201）：清零连续失败并回收 entry。
func (g *pairGuard) pass(ip string) {
	g.mu.Lock()
	delete(g.fails, ip)
	g.mu.Unlock()
}

// banned 报告 IP 是否处于失败封禁期内。
func (g *pairGuard) banned(ip string, now time.Time) bool {
	return g.banRemaining(ip, now) > 0
}

// banRemaining 报告剩余封禁秒数（0 = 未封；Retry-After 取值）。
func (g *pairGuard) banRemaining(ip string, now time.Time) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.fails[ip]
	if !ok || e.bannedUntil.IsZero() || !now.Before(e.bannedUntil) {
		return 0
	}
	return int(e.bannedUntil.Sub(now).Round(time.Second) / time.Second)
}

// expireLocked 回收封禁已过期的 entry（调用方持锁；内存纪律）。
func (g *pairGuard) expireLocked(now time.Time) {
	for k, e := range g.fails {
		if !e.bannedUntil.IsZero() && !now.Before(e.bannedUntil) {
			delete(g.fails, k)
		}
	}
}

// rejectPairRateLimited 统一 429 形态（机读码 + Retry-After）。
func rejectPairRateLimited(w http.ResponseWriter, retryAfterSecs int, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs))
	writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", msg)
}

type pairRequest struct {
	PairingCode              string `json:"pairingCode"`
	DeviceID                 string `json:"deviceId"`
	DeviceName               string `json:"deviceName"`
	Ed25519PublicKey         string `json:"ed25519PublicKey"`
	X25519EphemeralPublicKey string `json:"x25519EphemeralPublicKey"`
}

type pairResponse struct {
	DeviceID                 string `json:"deviceId"`
	SessionID                string `json:"sessionId"`
	DeviceToken              string `json:"deviceToken"`
	TokenVersion             int64  `json:"tokenVersion"`
	ServerX25519PublicKey    string `json:"serverX25519PublicKey"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
	SessionTimeoutSeconds    int    `json:"sessionTimeoutSeconds"`
	RotationIntervalDays     int    `json:"rotationIntervalDays"`
	IssuedAt                 int64  `json:"issuedAt"`
	WsPath                   string `json:"wsPath"`
}

// validatePairRequest 镜像 pair-request.schema.json 约束
// （required 4 字段 + pattern + deviceName ≤64 + additionalProperties=false 由
// 解码层拒绝未知字段）。
func validatePairRequest(req *pairRequest) *apiError {
	if !wire.PairCodePattern.MatchString(req.PairingCode) {
		return &apiError{400, "INVALID_PAIRING_CODE_FORMAT", "pairingCode 须为 6 位数字"}
	}
	if !wire.DeviceIDPattern.MatchString(req.DeviceID) {
		return &apiError{400, "INVALID_DEVICE_ID", "deviceId 须匹配 ^[A-Za-z0-9._-]{8,64}$"}
	}
	if len(req.DeviceName) > 64 {
		return &apiError{400, "INVALID_DEVICE_NAME", "deviceName ≤64 字符"}
	}
	if !wire.PublicKeyPattern.MatchString(req.Ed25519PublicKey) {
		return &apiError{400, "INVALID_ED25519_PUBLIC_KEY", "ed25519PublicKey 须为 32 字节 hex（64 字符）"}
	}
	if !wire.PublicKeyPattern.MatchString(req.X25519EphemeralPublicKey) {
		return &apiError{400, "INVALID_X25519_PUBLIC_KEY", "x25519EphemeralPublicKey 须为 32 字节 hex（64 字符）"}
	}
	return nil
}

// HandlePair 处理 POST /api/v2/pair（TLS 强制部署侧保障，SR-01/09）。
// H-1（安全审计 TOP5）：per-IP 防爆破闸（限速 5 次/分钟 + 连续 20 次失败封
// 15 分钟）在解析/触码之前计数，见 pairGuard。
func (s *Server) HandlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅 POST")
		return
	}
	now := s.Now().UTC()
	ip := compat.IPKey(r.RemoteAddr)
	if ok, banned := s.pairGuard.allow(ip, now); !ok {
		if banned {
			rejectPairRateLimited(w, s.pairGuard.banRemaining(ip, now),
				"连续配对失败，该 IP 已被封禁（冷却期内直接拒绝）")
			return
		}
		rejectPairRateLimited(w, int(PairRateWindow.Seconds()),
			"请求超出配对端点 per-IP 限速（"+strconv.Itoa(PairRatePerMin)+" 次/"+PairRateWindow.String()+"）")
		return
	}
	var req pairRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields() // schema additionalProperties:false
	if err := dec.Decode(&req); err != nil {
		s.pairGuard.fail(ip, now) // H-1：解析失败计入连续失败
		writeErr(w, http.StatusBadRequest, "INVALID_BODY", "请求体解析失败/含未定义字段")
		return
	}
	if ae := validatePairRequest(&req); ae != nil {
		s.pairGuard.fail(ip, now) // H-1：格式校验失败计入连续失败
		ae.write(w)
		return
	}
	ctx := r.Context()

	// 配对码校验：无效/过期/已用 → 403 PAIRING_CODE_INVALID（spec §5.1.2）
	codeHash := sha256Hex(req.PairingCode)
	ok, err := s.DB.ConsumePairingCode(ctx, codeHash, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	if !ok {
		s.pairGuard.fail(ip, now) // H-1：码无效计入连续失败
		writeErr(w, http.StatusForbidden, "PAIRING_CODE_INVALID", "配对码无效、过期或已使用")
		return
	}
	s.pairGuard.pass(ip) // H-1：成功清零连续失败计数

	// 签发凭据（仅存 hash；原始 token 进程内存凭据 + 一次返回）
	tokenHex, err := wire.RandomHex(32)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "KEYGEN", err.Error())
		return
	}
	srvPriv, srvPub, err := wire.GenerateX25519()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "KEYGEN", err.Error())
		return
	}
	// 设备身份唯一性（2026-11-04 所有者反馈：同一物理设备不得多条记录）：
	// 先按 Ed25519 签名公键归并——命中即视为**同一设备重配对**（重装/换前缀后
	// deviceId 变化但身份公钥稳定），沿用既有 device 行（保留 robot 绑定/epoch
	// 水位），响应既有 deviceId（端侧凭据随之归一）。
	if existing, found, err := s.DB.GetDeviceByEd25519(ctx, req.Ed25519PublicKey); err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	} else if found && existing.DeviceID != req.DeviceID {
		req.DeviceID = existing.DeviceID
		if req.DeviceName == "" {
			req.DeviceName = existing.DeviceName
		}
	}
	// 重复 deviceId 携新配对码 = 重新绑定（覆盖注册公钥，旧 deviceToken 失效——
	// 新凭据 putCred 覆盖内存注册表即达成）
	if err := s.DB.UpsertDevice(ctx, sqlite.Device{
		DeviceID: req.DeviceID, DeviceName: req.DeviceName,
		Ed25519PublicKey:   req.Ed25519PublicKey,
		DeviceX25519Public: req.X25519EphemeralPublicKey,
		TokenHash:          sha256Hex(tokenHex), TokenVersion: 1,
		Status: "active", PairedAt: now, UpdatedAt: now,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	sessionID := wire.NewUUID()
	if err := s.DB.CreateSession(ctx, sqlite.Session{
		SessionID: sessionID, DeviceID: req.DeviceID,
		ServerX25519Private: srvPriv, ServerX25519Public: srvPub,
		DeviceX25519Public: req.X25519EphemeralPublicKey, CreatedAt: now,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	s.Hub.PutCred(&gateway.LiveCred{DeviceID: req.DeviceID, TokenHex: tokenHex, TokenVersion: 1})
	_ = s.DB.InsertAudit(ctx, nil, "pair", req.DeviceID, "配对成功（token 仅存 hash）", now)

	writeJSON(w, http.StatusCreated, pairResponse{
		DeviceID:                 req.DeviceID,
		SessionID:                sessionID,
		DeviceToken:              tokenHex,
		TokenVersion:             1,
		ServerX25519PublicKey:    srvPub,
		HeartbeatIntervalSeconds: 15,
		SessionTimeoutSeconds:    45,
		RotationIntervalDays:     s.Machine.Policy().RotationDays,
		IssuedAt:                 now.UnixMilli(),
		WsPath:                   "/api/v2/device-sessions/" + sessionID,
	})
}

// CreatePairingCode 签发一个一次性配对码（6 位数字、TTL 10 分钟、单次有效；
// 控制台/管理员面随 T3.x admin 车道落地 RBAC——docs/02 §4 internal/admin，
// T2.3 以服务方法供运维联调与测试消费）。
func (s *Server) CreatePairingCode() (string, error) {
	// 随机源失败直接透传错误，绝不兜底为固定值（AUD-M2-04：兜底会使全部调用方拿到同一配对码）。
	hexStr, err := wire.RandomHex(6)
	if err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		return "", err
	}
	digits := make([]byte, 6)
	for i := 0; i < 6; i++ {
		digits[i] = byte('0') + raw[i]%10
	}
	code := string(digits)
	now := s.Now().UTC()
	// H-1（安全审计 TOP5）：签发新码即把全部未使用旧码置 used（单活动码语义，
	// 压缩爆破面——旧码即使未过期也不再可用；管理台签发路径同步接线）。
	if _, err := s.DB.InvalidateUnusedPairingCodes(contextBG(), now); err != nil {
		return "", err
	}
	if err := s.DB.CreatePairingCode(contextBG(), sha256Hex(code), now.Add(pairingCodeTTL)); err != nil {
		return "", err
	}
	return code, nil
}

func contextBG() context.Context { return context.Background() }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
