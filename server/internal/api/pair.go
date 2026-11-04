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
	"time"

	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// pairingCodeTTL 配对码有效期（spec §5.1.1 冻结：10 分钟）。
const pairingCodeTTL = 10 * time.Minute

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
func (s *Server) HandlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅 POST")
		return
	}
	var req pairRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields() // schema additionalProperties:false
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_BODY", "请求体解析失败/含未定义字段")
		return
	}
	if ae := validatePairRequest(&req); ae != nil {
		ae.write(w)
		return
	}
	ctx := r.Context()
	now := s.Now().UTC()

	// 配对码校验：无效/过期/已用 → 403 PAIRING_CODE_INVALID（spec §5.1.2）
	codeHash := sha256Hex(req.PairingCode)
	ok, err := s.DB.ConsumePairingCode(ctx, codeHash, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "PAIRING_CODE_INVALID", "配对码无效、过期或已使用")
		return
	}

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
