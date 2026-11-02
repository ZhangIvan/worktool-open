// v2 安全会话端点（spec §5）：会话创建（含 §5.5.1 Ed25519 重协商换发）与
// WS 建连（hello 协商 / 逐帧验签 / seq/eventId / 45s 失联关闭 / 903 重同步）。
package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// v2Session 为一条已建立的 v2 设备会话（内存态；持久镜像在 device_sessions 表）。
type v2Session struct {
	ID           string
	DeviceID     string
	TokenHex     string
	TokenVersion int64

	ServerX25519Private string
	DeviceX25519Public  string
	EncryptMax          int
	LastEpoch           int64

	InSeq  int64 // 最近合法设备→服务端 seq（严格递增校验）
	OutSeq int64 // 已分配的最高服务端→设备 seq（自 hello.lastReceivedSeq 起步）

	KSession []byte // 懒派生会话密钥（X25519+HKDF，spec §5.3）

	mu      sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex
	nonces  map[string]time.Time // 设备→服务端 nonce 5 分钟去重窗
	closed  bool
}

// nonceSeen 登记/查询设备 nonce（5 分钟窗口；重复 → true）。
func (s *v2Session) nonceSeen(nonce string, now time.Time, window time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nonces == nil {
		s.nonces = map[string]time.Time{}
	}
	for k, ts := range s.nonces {
		if now.Sub(ts) > window {
			delete(s.nonces, k)
		}
	}
	if _, dup := s.nonces[nonce]; dup {
		return true
	}
	s.nonces[nonce] = now
	return false
}

// ---- POST /api/v2/device-sessions ----

type createSessionRequest struct {
	DeviceID string `json:"deviceId"`
	// 可选：设备轮换新临时公钥（默认沿用设备登记值）
	X25519EphemeralPublicKey string `json:"x25519EphemeralPublicKey,omitempty"`
	// 凭据续期（§5.5.1）：服务端无活动凭据（重启后）时必填
	Renegotiate *renegotiateProof `json:"renegotiate,omitempty"`
}

type renegotiateProof struct {
	Ts        int64  `json:"ts"`
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"` // hex Ed25519(UTF-8(deviceId|ts|nonce))
}

type createSessionResponse struct {
	SessionID             string `json:"sessionId"`
	WsPath                string `json:"wsPath"`
	ServerX25519PublicKey string `json:"serverX25519PublicKey"`
	HeartbeatIntervalSecs int    `json:"heartbeatIntervalSeconds"`
	SessionTimeoutSecs    int    `json:"sessionTimeoutSeconds"`
	// 凭据换发时携带（§5.5.1；deviceToken 仅此一次返回）
	CredentialRotated bool   `json:"credentialRotated,omitempty"`
	DeviceToken       string `json:"deviceToken,omitempty"`
	TokenVersion      int64  `json:"tokenVersion,omitempty"`
}

// HandleCreateSession 处理 POST /api/v2/device-sessions。
func (h *Hub) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "仅 POST")
		return
	}
	var req createSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "INVALID_BODY", "请求体解析失败")
		return
	}
	if !wire.DeviceIDPattern.MatchString(req.DeviceID) {
		writeJSONError(w, http.StatusBadRequest, "INVALID_DEVICE_ID", "deviceId 须匹配 ^[A-Za-z0-9._-]{8,64}$")
		return
	}
	dev, ok, err := h.DB.GetDevice(r.Context(), req.DeviceID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	if !ok {
		writeJSONError(w, http.StatusForbidden, "UNKNOWN_DEVICE", "设备未配对，请先走 POST /api/v2/pair")
		return
	}

	cred := h.getCred(req.DeviceID)
	resp := createSessionResponse{HeartbeatIntervalSecs: 15, SessionTimeoutSecs: 45}
	// 服务端凭据失忆（重启/撤销后）→ §5.5.1 重协商：Ed25519 签名换发新凭据
	if cred == nil {
		if req.Renegotiate == nil {
			writeJSONError(w, http.StatusUnauthorized, "RENEGOTIATION_REQUIRED",
				"服务端无活动凭据（重启/撤销后），须携带 renegotiate{ts,nonce,signature}=Ed25519(deviceId|ts|nonce)（spec §5.5.1）")
			return
		}
		newCred, err := h.renegotiate(r.Context(), dev, req)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "RENEGOTIATE_FAILED", err.Error())
			return
		}
		cred = newCred
		resp.CredentialRotated = true
		resp.DeviceToken = newCred.TokenHex
		resp.TokenVersion = newCred.TokenVersion
	}

	srvPriv, srvPub, err := wire.GenerateX25519()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "KEYGEN", err.Error())
		return
	}
	devicePub := req.X25519EphemeralPublicKey
	if devicePub == "" {
		devicePub = dev.DeviceX25519Public
	}
	if !wire.PublicKeyPattern.MatchString(devicePub) {
		writeJSONError(w, http.StatusBadRequest, "INVALID_X25519_PUBLIC_KEY", "x25519EphemeralPublicKey 须为 64 字符 hex")
		return
	}
	sessionID := wire.NewUUID()
	if err := h.DB.CreateSession(r.Context(), sqlite.Session{
		SessionID: sessionID, DeviceID: req.DeviceID,
		ServerX25519Private: srvPriv, ServerX25519Public: srvPub,
		DeviceX25519Public: devicePub, CreatedAt: h.Now().UTC(),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "STORE", err.Error())
		return
	}
	resp.SessionID = sessionID
	resp.WsPath = "/api/v2/device-sessions/" + sessionID
	resp.ServerX25519PublicKey = srvPub
	writeJSONStatus(w, http.StatusCreated, resp)
}

// renegotiate 验证 Ed25519(deviceId|ts|nonce) 并签发新凭据（复用配对注册的签名公钥）。
func (h *Hub) renegotiate(ctx context.Context, dev sqlite.Device, req createSessionRequest) (*LiveCred, error) {
	p := req.Renegotiate
	if p == nil || p.Nonce == "" || p.Signature == "" {
		return nil, errors.New("renegotiate 缺 ts/nonce/signature")
	}
	if d := h.Now().UTC().UnixMilli() - p.Ts; d > 30_000 || d < -30_000 {
		return nil, errors.New("renegotiate ts 超出 ±30s 容差")
	}
	pubBytes, err := hex.DecodeString(dev.Ed25519PublicKey)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, errors.New("设备签名公钥未注册或非法（须先完成配对）")
	}
	sig, err := hex.DecodeString(p.Signature)
	if err != nil {
		return nil, errors.New("signature 非 hex")
	}
	msg := []byte(fmt.Sprintf("%s|%d|%s", req.DeviceID, p.Ts, p.Nonce))
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), msg, sig) {
		return nil, errors.New("Ed25519 签名验证失败")
	}
	tokenHex, err := wire.RandomHex(32)
	if err != nil {
		return nil, err
	}
	newVersion := dev.TokenVersion + 1
	if err := h.DB.UpsertDevice(ctx, sqlite.Device{
		DeviceID: dev.DeviceID, RobotID: dev.RobotID, DeviceName: dev.DeviceName,
		Ed25519PublicKey:   dev.Ed25519PublicKey,
		DeviceX25519Public: dev.DeviceX25519Public,
		TokenHash:          sha256Hex(tokenHex), TokenVersion: newVersion,
		Status: "active", Epoch: dev.Epoch,
		PairedAt: dev.PairedAt, UpdatedAt: h.Now().UTC(),
	}); err != nil {
		return nil, err
	}
	cred := &LiveCred{DeviceID: dev.DeviceID, TokenHex: tokenHex, TokenVersion: newVersion}
	h.putCred(cred)
	return cred, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- WS /api/v2/device-sessions/{sessionId} ----

// HandleDeviceSessionWS 处理 v2 会话 WebSocket。
func (h *Hub) HandleDeviceSessionWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	row, ok, err := h.DB.GetSession(r.Context(), sessionID)
	if err != nil {
		http.Error(w, "store error", http.StatusInternalServerError)
		return
	}
	if !ok || row.State == "closed" {
		http.Error(w, `{"error":"SESSION_NOT_FOUND"}`, http.StatusNotFound)
		return
	}
	cred := h.getCred(row.DeviceID)
	if cred == nil {
		// 会话创建时的凭据已失忆（重启）：须重建会话并重协商换发
		http.Error(w, `{"error":"RENEGOTIATION_REQUIRED"}`, http.StatusUnauthorized)
		return
	}
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &v2Session{
		ID: sessionID, DeviceID: row.DeviceID,
		TokenHex: cred.TokenHex, TokenVersion: cred.TokenVersion,
		ServerX25519Private: row.ServerX25519Private,
		DeviceX25519Public:  row.DeviceX25519Public,
		nonces:              map[string]time.Time{},
		conn:                conn,
	}
	h.serveV2(r.Context(), s, conn)
}

// serveV2 为 v2 会话主循环：hello 协商 → 读循环（§5.2.4 校验顺序）。
func (h *Hub) serveV2(ctx context.Context, s *v2Session, conn *websocket.Conn) {
	const sessionTimeout = 45 * time.Second // spec §3 冻结值

	// hello 首帧（10s 内）
	_ = conn.SetReadDeadline(h.Now().Add(10 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		closeWith(&s.writeMu, conn, wire.CloseFrameInvalid, "hello 超时/读失败")
		return
	}
	hello, frameErr := wire.DecodeHello(data)
	if frameErr != nil {
		h.rejectV2(s, conn, asFrameErr(frameErr))
		return
	}
	if hello.DeviceID != s.DeviceID {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "deviceId 与会话不匹配"})
		return
	}
	// §5.2.4 校验顺序：格式（DecodeHello 已过）→ tokenVersion → ts → nonce → hmac → seq/eventId
	if hello.TokenVersion != s.TokenVersion || hello.Meta.TokenVersion != s.TokenVersion {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseTokenVersion, Msg: "tokenVersion 校验失败"})
		return
	}
	now := h.Now().UTC()
	if d := now.UnixMilli() - hello.Meta.Ts; d > 30_000 || d < -30_000 {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseClockSkew, Msg: "ts 超出 ±30s 容差"})
		return
	}
	if s.nonceSeen(hello.Meta.Nonce, now, 5*time.Minute) {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseNonceReplay, Msg: "nonce 重放"})
		return
	}
	digest, err := wire.PayloadDigest(0, nil, "") // hello 为空载荷帧
	if err != nil {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: err.Error()})
		return
	}
	if ok, _ := wire.VerifyHMAC(s.TokenHex, hello.Meta.HMAC, "", hello.Meta.EventID, 1, hello.Meta.Ts, hello.Meta.Nonce, digest); !ok {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseAuthFailure, Msg: "hello HMAC 验签失败"})
		return
	}
	if hello.Meta.Seq != 1 {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseSeqOrEventID, Msg: "hello seq 须为 1"})
		return
	}
	if dup, conflict, err := h.DB.RememberInboundEvent(ctx, s.DeviceID, hello.Meta.EventID, digest, "hello", now); err != nil || dup || conflict {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseSeqOrEventID, Msg: "hello eventId 冲突"})
		return
	}

	// 注册会话（一设备一活动会话：旧会话被顶替即关闭——501013 多端互踢语义在 v2
	// 由会话唯一性承载，spec §3）
	h.mu.Lock()
	if old := h.v2ByDev[s.DeviceID]; old != nil && old != s {
		old.closeLocal()
	}
	h.v2[s.ID] = s
	h.v2ByDev[s.DeviceID] = s
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.v2[s.ID] == s {
			delete(h.v2, s.ID)
		}
		if h.v2ByDev[s.DeviceID] == s {
			delete(h.v2ByDev, s.DeviceID)
		}
		h.mu.Unlock()
		_ = h.DB.CloseSession(ctx, s.ID, h.Now().UTC())
	}()

	// epoch 拒旧基线（docs/02 §5.1：服务端不得下发更小 epoch）
	s.LastEpoch = hello.LastEpoch
	s.EncryptMax = 0
	if hello.Capabilities != nil {
		s.EncryptMax = hello.Capabilities.EncryptMax
	}
	s.InSeq = 1
	s.OutSeq = hello.LastReceivedSeq // 下一帧 = +1（自持久确认点补发，spec §5.4.2）
	if err := h.DB.EnsureEpochAtLeast(ctx, s.DeviceID, hello.LastEpoch); err != nil {
		h.Log.Printf("gateway: EnsureEpochAtLeast: %v", err)
	}
	if err := h.DB.OpenSession(ctx, s.ID, hello.LastEpoch, hello.LastReceivedSeq, int64(s.EncryptMax), h.Now().UTC()); err != nil {
		h.Log.Printf("gateway: OpenSession: %v", err)
	}

	// hello_ack（形状冻结，spec §5.4.1；meta 为服务端签名帧）
	ack, ackMeta, err := s.buildSignedFrame(h, wire.SocketMessageList, nil, "")
	if err != nil {
		h.Log.Printf("gateway: hello_ack 构造失败: %v", err)
		return
	}
	if err := writeRaw(&s.writeMu, conn, mustJSON(ackEnvelope(ack, ackMeta, hello.LastReceivedSeq+1))); err != nil {
		return
	}

	// 会话就绪：冲刷积压指令 + 恢复触发对账（UNKNOWN 任务入口之二：重连触发）
	if _, err := s.flushQueued(ctx, h); err != nil {
		h.Log.Printf("gateway: flushQueued: %v", err)
	}
	if pending, err := h.DB.PendingUnknownRisk(ctx); err == nil && len(pending) > 0 {
		// 重连触发对账：请求端侧 904 journal 补报（服务端不凭超时定性，三原则①）
		if err := s.sendJournalRequest(h); err != nil {
			h.Log.Printf("gateway: journal request: %v", err)
		}
	}

	for {
		_ = conn.SetReadDeadline(h.Now().Add(sessionTimeout))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return // 45s 失联/网络错误：会话关闭（defer 清理）
		}
		if mt != websocket.TextMessage {
			h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "仅接受文本帧"})
			return
		}
		if h.handleV2Frame(ctx, s, conn, data) {
			return
		}
	}
}

// handleV2Frame 校验并分发一帧设备消息；返回 true 表示会话应终止。
func (h *Hub) handleV2Frame(ctx context.Context, s *v2Session, conn *websocket.Conn, data []byte) bool {
	var peek map[string]json.RawMessage
	if err := json.Unmarshal(data, &peek); err != nil {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "JSON 解析失败"})
		return true
	}
	if _, isTD := peek["td"]; isTD {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "v2 会话不接受 v1 td 帧"})
		return true
	}
	if len(peek) == 1 {
		if _, isBare := peek["type"]; isBare {
			h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "v2 会话不接受裸 type 帧"})
			return true
		}
	}
	env, err := wire.DecodeEnvelope(data)
	if err != nil {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "信封解析失败"})
		return true
	}
	if err := env.ValidateV2(); err != nil {
		h.rejectV2(s, conn, asFrameErr(err))
		return true
	}
	meta, _ := env.V2Meta()
	now := h.Now().UTC()

	// §5.2.4 顺序：tokenVersion → ts → nonce → hmac → seq/eventId →（加密帧）解密
	if meta.TokenVersion != s.TokenVersion {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseTokenVersion, Msg: "tokenVersion 校验失败"})
		return true
	}
	if d := now.UnixMilli() - meta.Ts; d > 30_000 || d < -30_000 {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseClockSkew, Msg: "ts 超出 ±30s 容差"})
		return true
	}
	if s.nonceSeen(meta.Nonce, now, 5*time.Minute) {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseNonceReplay, Msg: "nonce 重放"})
		return true
	}
	digest, err := wire.PayloadDigest(env.EncryptType, env.List, env.EncryptedList)
	if err != nil {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "payloadDigest 计算失败"})
		return true
	}
	if ok, _ := wire.VerifyHMAC(s.TokenHex, meta.HMAC, meta.CommandID, meta.EventID, meta.Seq, meta.Ts, meta.Nonce, digest); !ok {
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseAuthFailure, Msg: "HMAC 验签失败"})
		return true
	}
	if meta.Seq != s.InSeq+1 {
		// 真实签名的乱序/回退：4405 + 903 重同步（spec §5.4.2/§5.4.4）
		s.sendResync(h)
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseSeqOrEventID,
			Msg: fmt.Sprintf("seq 乱序/回退：期望 %d 收到 %d", s.InSeq+1, meta.Seq)})
		return true
	}
	// seq 校验通过即推进计数（幂等重发帧同样按帧序消费 seq，防止双端计数失步）
	s.InSeq = meta.Seq
	dup, conflict, err := h.DB.RememberInboundEvent(ctx, s.DeviceID, meta.EventID, digest, "", now)
	if err != nil {
		h.Log.Printf("gateway: eventId 记录失败: %v", err)
		return true
	}
	if conflict {
		// 同 ID 异内容 → 冲突关闭 4405（§5.4.3）
		s.sendResync(h)
		h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseSeqOrEventID, Msg: "eventId 同 ID 异内容冲突"})
		return true
	}
	if dup {
		// 同 ID 同内容 → 幂等回执原确认，不重复副作用（§5.4.3；无原回执则静默）
		if receipt, ok, _ := h.DB.GetInboundReceipt(ctx, s.DeviceID, meta.EventID); ok && receipt != "" {
			_ = writeRaw(&s.writeMu, conn, []byte(receipt))
		}
		return false
	}

	// 加密帧：验签已过 → 解密（解密失败 = 帧整体拒绝，spec §5.3）
	listJSON := env.List
	if env.EncryptType == 2 {
		key, err := s.sessionKey()
		if err != nil {
			h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "会话密钥派生失败"})
			return true
		}
		plain, err := wire.OpenList(key, wire.GCMNonce(meta.EventID), wire.GCMAAD(meta.Seq, meta.EventID, meta.Ts), env.EncryptedList)
		if err != nil {
			h.rejectV2(s, conn, &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: "GCM 解密/认证失败"})
			return true
		}
		listJSON = plain
	}

	h.dispatchV2Payload(ctx, s, env, meta, listJSON)
	return false
}

// sessionKey 懒派生并缓存会话密钥。
func (s *v2Session) sessionKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.KSession != nil {
		return s.KSession, nil
	}
	k, err := wire.DeriveSessionKey(s.ServerX25519Private, s.DeviceX25519Public, s.ID)
	if err != nil {
		return nil, err
	}
	s.KSession = k
	return k, nil
}

// dispatchV2Payload 按帧语义分发到统一命令管线。
func (h *Hub) dispatchV2Payload(ctx context.Context, s *v2Session, env *wire.Envelope, meta *wire.FrameMeta, listJSON []byte) {
	switch env.SocketType {
	case wire.SocketHeartbeat:
		return // 心跳：读活动即存活证据（45s 失联由读 deadline 关闭）
	case wire.SocketRawConfirm, wire.SocketMessageList:
	default:
		return
	}
	items, err := (&wire.Envelope{List: listJSON}).ListItems()
	if err != nil {
		h.Log.Printf("gateway: list 解析失败: %v", err)
		return
	}
	now := h.Now().UTC()
	for _, item := range items {
		switch t := wire.ListItemType(item); {
		case t == wire.TypeJournalReplay:
			var payload struct {
				Entries []scheduler.JournalEntry `json:"entries"`
			}
			if err := json.Unmarshal(item, &payload); err != nil {
				h.Log.Printf("gateway: 904 载荷解析失败: %v", err)
				continue
			}
			if err := h.Pipeline.ReconcileJournal(ctx, payload.Entries); err != nil {
				h.Log.Printf("gateway: journal 对账: %v", err)
			}
		case wire.IsExecResult(item):
			r, err := wire.DecodeExecResult(item)
			if err != nil {
				h.Log.Printf("gateway: 结果载荷解析失败: %v", err)
				continue
			}
			commandID := meta.CommandID
			if commandID == "" {
				var probe struct {
					MetaJSON string `json:"metaJson"`
				}
				_ = json.Unmarshal(item, &probe)
				commandID = dispatchIDFromMetaJSON(probe.MetaJSON)
			}
			if commandID == "" {
				h.Log.Printf("gateway: 结果帧缺 commandId（meta.commandId / metaJson.dispatchId）")
				continue
			}
			if _, err := h.Pipeline.HandleResult(ctx, commandID, r); err != nil {
				h.Log.Printf("gateway: 结果处理 commandId=%s: %v", commandID, err)
			}
		case t == wire.TypeReceiveMessageList || t == wire.TypeRobotLog || t == wire.TypeRobotErrorLog:
			// 上报：审计 + 回调 outbox（仅元数据不 echo 内容，docs/02 §6.1）
			kind := "report"
			if t != wire.TypeReceiveMessageList {
				kind = "device_log"
			}
			tx, txErr := h.DB.Tx(ctx)
			if txErr == nil {
				_ = h.DB.InsertAudit(ctx, tx, kind, s.DeviceID, string(truncateForAudit(item, 512)), now)
				_ = h.DB.EnqueueOutbox(ctx, tx, kind, "", map[string]any{
					"kind": kind, "deviceId": s.DeviceID, "reportType": t, "at": now.UnixMilli(),
				}, now)
				_ = tx.Commit()
			}
		case t == wire.TypeHeartbeat:
			// 信封化心跳（socketType=0 之外的 type=11 载荷）：无操作
		default:
			h.Log.Printf("gateway: 未消费载荷 type=%d", t)
		}
	}
}

// flushQueued 把目标的 queued 指令构造为签名帧下发并落 E03（先发送后留痕）。
// 取批→发送→留痕整段持 Hub 级 flush 锁：与 hello 冲刷/调度泵并发时防止同一
// queued 指令被取走两次（双发帧 + 第二次 MarkDispatched 409，T3.2 修复的
// 既有竞态——帧发送为小文本写，锁窗口可接受）。
func (s *v2Session) flushQueued(ctx context.Context, h *Hub) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, nil
	}
	h.flushMu.Lock()
	defer h.flushMu.Unlock()
	cmds, err := h.Pipeline.QueuedForTarget(ctx, s.DeviceID, 64)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, cmd := range cmds {
		epoch, err := h.DB.BumpEpoch(ctx, s.DeviceID)
		if err != nil {
			return n, err
		}
		frame, meta, err := s.buildSignedFrame(h, wire.SocketMessageList, json.RawMessage(`[`+cmd.Payload+`]`), cmd.CommandID)
		if err != nil {
			return n, err
		}
		if err := writeRaw(&s.writeMu, s.conn, mustJSON(frame)); err != nil {
			return n, err
		}
		if err := h.Pipeline.MarkDispatched(ctx, cmd.CommandID, epoch, meta.Seq, meta.EventID); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *v2Session) closeLocal() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	closeWith(&s.writeMu, s.conn, wire.CloseSeqOrEventID, "superseded by newer session")
}

// sendResync 下发 903 RESYNC_REQUEST（4405 关闭前）。
func (s *v2Session) sendResync(h *Hub) {
	frame, _, err := s.buildSignedFrame(h, wire.SocketMessageList, json.RawMessage(`[{"type":903}]`), "")
	if err != nil {
		return
	}
	_ = writeRaw(&s.writeMu, s.conn, mustJSON(frame))
}

// sendJournalRequest 下发 904 journal 补报请求（重连触发对账）。
func (s *v2Session) sendJournalRequest(h *Hub) error {
	frame, _, err := s.buildSignedFrame(h, wire.SocketMessageList, json.RawMessage(`[{"type":904,"request":"journal_replay"}]`), "")
	if err != nil {
		return err
	}
	return writeRaw(&s.writeMu, s.conn, mustJSON(frame))
}

func (h *Hub) rejectV2(s *v2Session, conn *websocket.Conn, fe *wire.FrameError) {
	h.Log.Printf("gateway: v2 拒帧 device=%s: %d %s", s.DeviceID, fe.Code, fe.Msg)
	closeWith(&s.writeMu, conn, fe.Code, fe.Msg)
}

func asFrameErr(err error) *wire.FrameError {
	var fe *wire.FrameError
	if errors.As(err, &fe) {
		return fe
	}
	return &wire.FrameError{Code: wire.CloseFrameInvalid, Msg: err.Error()}
}

func dispatchIDFromMetaJSON(metaJSON string) string {
	if metaJSON == "" {
		return ""
	}
	var probe struct {
		DispatchID string `json:"dispatchId"`
	}
	if err := json.Unmarshal([]byte(metaJSON), &probe); err != nil {
		return ""
	}
	return probe.DispatchID
}

func truncateForAudit(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return append(append([]byte{}, b[:n:n]...), []byte("...")...)
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSONStatus(w, code, map[string]string{"error": errCode, "message": msg})
}

// ackEnvelope 把签名帧包装为 hello_ack 形状（type/serverTime/resumeFromSeq + meta）。
func ackEnvelope(_ *wire.Envelope, meta *wire.FrameMeta, resumeFrom int64) wire.HelloAck {
	return wire.HelloAck{
		Type:                  "hello_ack",
		ServerTime:            meta.Ts,
		ResumeFromSeq:         resumeFrom,
		HeartbeatIntervalSecs: 15,
		SessionTimeoutSecs:    45,
		Meta:                  *meta,
	}
}
