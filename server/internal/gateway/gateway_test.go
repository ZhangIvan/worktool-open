package gateway_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
	"cn.worktool.open/server/internal/wire"
)

// testApp 为完整装配的本地服务（真 HTTP/WS 栈，loopback）。
type testApp struct {
	t        *testing.T
	db       *sqlite.DB
	pipeline *scheduler.Service
	hub      *gateway.Hub
	apiSrv   *api.Server
	httpSrv  *httptest.Server
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	db, err := sqlite.OpenMem()
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	hub := gateway.New(db, pipeline, machine, testLogger{t})
	apiSrv := api.NewServer(db, hub, pipeline, machine, testLogger{t})
	// H-2（安全审计 TOP5）后 v1 兼容面默认不挂载——测试装配显式 opt-in
	// （对齐生产 main.go 在 -enable-v1-compat 下的形态）。
	mux := api.NewServeMux(api.Options{Server: apiSrv, EnableV1Compat: true})
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return &testApp{t: t, db: db, pipeline: pipeline, hub: hub, apiSrv: apiSrv, httpSrv: httpSrv}
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) {
	l.t.Logf("[srv] "+format, args...)
}

// pairDevice 完成一次真实配对，返回设备侧材料。
type deviceMaterial struct {
	deviceID  string
	tokenHex  string // deviceToken（仅客户端持有）
	edPriv    ed25519.PrivateKey
	edPubHex  string
	xPrivHex  string // 设备 X25519 临时私钥
	xPubHex   string
	sessionID string
}

func (a *testApp) pairDevice(t *testing.T) *deviceMaterial {
	t.Helper()
	code, err := a.apiSrv.CreatePairingCode()
	if err != nil {
		t.Fatalf("签发配对码: %v", err)
	}
	edPub, edPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	xPriv, xPub, err := wire.GenerateX25519()
	if err != nil {
		t.Fatal(err)
	}
	m := &deviceMaterial{
		deviceID: "device-test-0001",
		edPriv:   edPriv,
		edPubHex: toHex(edPub),
		xPrivHex: xPriv,
		xPubHex:  xPub,
	}
	body := map[string]string{
		"pairingCode":              code,
		"deviceId":                 m.deviceID,
		"deviceName":               "lab-test",
		"ed25519PublicKey":         m.edPubHex,
		"x25519EphemeralPublicKey": m.xPubHex,
	}
	resp := a.post(t, "/api/v2/pair", body)
	if resp.code != http.StatusCreated {
		t.Fatalf("pair = %d: %s", resp.code, resp.body)
	}
	var pr struct {
		DeviceID                 string `json:"deviceId"`
		SessionID                string `json:"sessionId"`
		DeviceToken              string `json:"deviceToken"`
		TokenVersion             int64  `json:"tokenVersion"`
		ServerX25519PublicKey    string `json:"serverX25519PublicKey"`
		HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
		SessionTimeoutSeconds    int    `json:"sessionTimeoutSeconds"`
		RotationIntervalDays     int    `json:"rotationIntervalDays"`
		WsPath                   string `json:"wsPath"`
	}
	if err := json.Unmarshal(resp.body, &pr); err != nil {
		t.Fatalf("pair 响应解析: %v", err)
	}
	if pr.DeviceID != m.deviceID || pr.DeviceToken == "" || pr.TokenVersion != 1 ||
		pr.HeartbeatIntervalSeconds != 15 || pr.SessionTimeoutSeconds != 45 ||
		pr.RotationIntervalDays != 30 || !strings.HasPrefix(pr.WsPath, "/api/v2/device-sessions/") {
		t.Fatalf("pair 响应形状漂移: %+v", pr)
	}
	if !wire.PublicKeyPattern.MatchString(pr.ServerX25519PublicKey) {
		t.Fatalf("serverX25519PublicKey 格式非法: %q", pr.ServerX25519PublicKey)
	}
	m.tokenHex = pr.DeviceToken
	m.sessionID = pr.SessionID
	return m
}

type httpResp struct {
	code int
	body []byte
}

func (a *testApp) post(t *testing.T, path string, body any) httpResp {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(a.httpSrv.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return httpResp{code: resp.StatusCode, body: buf.Bytes()}
}

// createSession 建会话，返回 (wsURL, serverX25519PublicKey)。
func (a *testApp) createSession(t *testing.T, m *deviceMaterial, extra map[string]any) (string, string, int64, string) {
	t.Helper()
	body := map[string]any{"deviceId": m.deviceID}
	for k, v := range extra {
		body[k] = v
	}
	resp := a.post(t, "/api/v2/device-sessions", body)
	if resp.code != http.StatusCreated {
		t.Fatalf("device-sessions = %d: %s", resp.code, resp.body)
	}
	var cr struct {
		SessionID             string `json:"sessionId"`
		WsPath                string `json:"wsPath"`
		ServerX25519PublicKey string `json:"serverX25519PublicKey"`
		CredentialRotated     bool   `json:"credentialRotated"`
		DeviceToken           string `json:"deviceToken"`
		TokenVersion          int64  `json:"tokenVersion"`
	}
	if err := json.Unmarshal(resp.body, &cr); err != nil {
		t.Fatal(err)
	}
	wsURL := "ws" + strings.TrimPrefix(a.httpSrv.URL, "http") + cr.WsPath
	return wsURL, cr.ServerX25519PublicKey, cr.TokenVersion, cr.DeviceToken
}

// deviceClient 为 v2 设备端 WS 客户端模拟（seq/nonce/HMAC/GCM 全套）。
type deviceClient struct {
	t           *testing.T
	conn        *websocket.Conn
	m           *deviceMaterial
	serverXPub  string
	kSession    []byte
	tokenHex    string
	tokenVer    int64
	outSeq      int64 // 设备→服务端
	seenEventID map[string]string
	mu          sync.Mutex
}

func dialV2(t *testing.T, url string, m *deviceMaterial, serverXPub string, tokenHex string, tokenVer int64) *deviceClient {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &deviceClient{
		t: t, conn: conn, m: m, serverXPub: serverXPub,
		tokenHex: tokenHex, tokenVer: tokenVer,
		seenEventID: map[string]string{},
	}
	return c
}

// sessionKey 设备侧派生（X25519(设备临时私钥, 服务端公钥) + HKDF）。
func (c *deviceClient) sessionKey(sessionID string) []byte {
	if c.kSession == nil {
		k, err := wire.DeriveSessionKey(c.m.xPrivHex, c.serverXPub, sessionID)
		if err != nil {
			c.t.Fatalf("设备侧派生会话密钥: %v", err)
		}
		c.kSession = k
	}
	return c.kSession
}

// buildFrame 构造设备→服务端签名帧（C-9 输入式）。
func (c *deviceClient) buildFrame(socketType int, list json.RawMessage, commandID string, mutate func(meta *wire.FrameMeta)) []byte {
	c.t.Helper()
	c.mu.Lock()
	c.outSeq++
	seq := c.outSeq
	c.mu.Unlock()
	ts := time.Now().UTC().UnixMilli()
	eventID := wire.NewUUID()
	nonce, err := wire.RandomHex(16)
	if err != nil {
		c.t.Fatal(err)
	}
	var digest string
	env := map[string]any{
		"socketType":  socketType,
		"messageId":   wire.BuildMessageID(ts, eventID),
		"apiSend":     0,
		"encryptType": 0,
	}
	if len(list) > 0 {
		env["list"] = json.RawMessage(list)
		d, err := wire.PayloadDigest(0, list, "")
		if err != nil {
			c.t.Fatal(err)
		}
		digest = d
	} else {
		env["list"] = json.RawMessage(`[]`)
		digest = wire.EmptyPayloadDigest
	}
	mac, err := wire.ComputeHMAC(c.tokenHex, commandID, eventID, seq, ts, nonce, digest)
	if err != nil {
		c.t.Fatal(err)
	}
	meta := wire.FrameMeta{
		Ver: 2, Ts: ts, Nonce: nonce, HMAC: mac,
		Seq: seq, EventID: eventID, CommandID: commandID, TokenVersion: c.tokenVer,
	}
	if mutate != nil {
		mutate(&meta) // 测试注入篡改（改 seq/nonce/ts 后 hmac 不重算 = 声明式篡改）
	}
	env["meta"] = meta
	raw, err := json.Marshal(env)
	if err != nil {
		c.t.Fatal(err)
	}
	return raw
}

// resignFrame 以篡改后的 meta 字段重算 hmac（构造“真实签名”的乱序帧）。
func (c *deviceClient) buildFrameResigned(socketType int, list json.RawMessage, commandID string, mutate func(meta *wire.FrameMeta)) []byte {
	c.t.Helper()
	raw := c.buildFrame(socketType, list, commandID, mutate)
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		c.t.Fatal(err)
	}
	var meta wire.FrameMeta
	if err := json.Unmarshal(env["meta"], &meta); err != nil {
		c.t.Fatal(err)
	}
	var listRaw json.RawMessage
	_ = json.Unmarshal(env["list"], &listRaw)
	if listRaw == nil {
		listRaw = json.RawMessage(`[]`)
	}
	digest, err := wire.PayloadDigest(0, listRaw, "")
	if err != nil {
		c.t.Fatal(err)
	}
	mac, err := wire.ComputeHMAC(c.tokenHex, meta.CommandID, meta.EventID, meta.Seq, meta.Ts, meta.Nonce, digest)
	if err != nil {
		c.t.Fatal(err)
	}
	meta.HMAC = mac
	env["meta"] = testJSON(meta)
	out, err := json.Marshal(env)
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

func (c *deviceClient) send(raw []byte) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		c.t.Fatalf("设备发帧失败: %v", err)
	}
}

// readFrame 读一帧服务端信封（超时 5s）。
func (c *deviceClient) readFrame() *wire.Envelope {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("设备读帧失败: %v", err)
	}
	if mt != websocket.TextMessage {
		c.t.Fatalf("非文本帧 %d", mt)
	}
	env, err := wire.DecodeEnvelope(data)
	if err != nil {
		c.t.Fatalf("服务端帧解析失败: %v (%s)", err, data)
	}
	return env
}

// readHelloAck 读 hello_ack 并逐字段校验（含客户端侧验签）。
func (c *deviceClient) readHelloAck(sessionID string, wantResumeFrom int64) wire.HelloAck {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("读 hello_ack 失败: %v", err)
	}
	var ack wire.HelloAck
	if err := json.Unmarshal(data, &ack); err != nil {
		c.t.Fatalf("hello_ack 解析: %v (%s)", err, data)
	}
	if ack.Type != "hello_ack" {
		c.t.Fatalf("hello_ack.type = %q", ack.Type)
	}
	if ack.ResumeFromSeq != wantResumeFrom {
		t := c.t
		t.Fatalf("resumeFromSeq = %d, want %d", ack.ResumeFromSeq, wantResumeFrom)
	}
	if ack.HeartbeatIntervalSecs != 15 || ack.SessionTimeoutSecs != 45 {
		c.t.Fatalf("hello_ack 间隔冻结值漂移: %+v", ack)
	}
	// 客户端验签（服务端→设备方向 HMAC）
	digest, err := wire.PayloadDigest(0, nil, "")
	if err != nil {
		c.t.Fatal(err)
	}
	ok, err := wire.VerifyHMAC(c.tokenHex, ack.Meta.HMAC, ack.Meta.CommandID, ack.Meta.EventID, ack.Meta.Seq, ack.Meta.Ts, ack.Meta.Nonce, digest)
	if err != nil || !ok {
		c.t.Fatalf("hello_ack 验签失败: ok=%v err=%v（服务端签名实现错误）", ok, err)
	}
	return ack
}

// verifyServerFrame 验签 + （加密帧）解密，返回 list JSON。
func (c *deviceClient) verifyServerFrame(env *wire.Envelope, sessionID string) []byte {
	c.t.Helper()
	meta, err := env.V2Meta()
	if err != nil {
		c.t.Fatal(err)
	}
	digest, err := wire.PayloadDigest(env.EncryptType, env.List, env.EncryptedList)
	if err != nil {
		c.t.Fatal(err)
	}
	ok, err := wire.VerifyHMAC(c.tokenHex, meta.HMAC, meta.CommandID, meta.EventID, meta.Seq, meta.Ts, meta.Nonce, digest)
	if err != nil || !ok {
		c.t.Fatalf("服务端帧验签失败: ok=%v err=%v", ok, err)
	}
	if env.EncryptType == 2 {
		plain, err := wire.OpenList(c.sessionKey(sessionID), wire.GCMNonce(meta.EventID), wire.GCMAAD(meta.Seq, meta.EventID, meta.Ts), env.EncryptedList)
		if err != nil {
			c.t.Fatalf("加密帧解密失败（ECDH/HKDF/GCM 双端不一致）: %v", err)
		}
		return plain
	}
	return env.List
}

// expectClose 断言随后收到指定关闭码。
func (c *deviceClient) expectClose(wantCode int) {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := c.conn.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		c.t.Fatalf("期望关闭码 %d，实际错误: %v", wantCode, err)
	}
	if ce.Code != wantCode {
		c.t.Fatalf("关闭码 = %d, want %d（text=%q）", ce.Code, wantCode, ce.Text)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return toHex(sum[:])
}

func testJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func toHex(b []byte) string { return hex.EncodeToString(b) }

// openV2Session 走完 建会话→dial→hello→hello_ack。
// tokenVer 为设备当前凭据版本（常规=1；重协商换发后=2，spec §5.5.1）。
func openV2Session(t *testing.T, a *testApp, m *deviceMaterial, encryptMax int, lastEpoch, lastReceivedSeq int64, tokenVer int64) (*deviceClient, string) {
	t.Helper()
	wsURL, serverPub, _, _ := a.createSession(t, m, nil)
	c := dialV2(t, wsURL, m, serverPub, m.tokenHex, tokenVer)
	hello := map[string]any{
		"type":            "hello",
		"deviceId":        m.deviceID,
		"tokenVersion":    tokenVer,
		"lastEpoch":       lastEpoch,
		"lastReceivedSeq": lastReceivedSeq,
		"capabilities": map[string]any{
			"clientVersion":  "test-0.1",
			"encryptTypeMax": encryptMax,
			"commands":       []int{203, 204},
		},
	}
	c.mu.Lock()
	c.outSeq = 1 // hello 已消费 seq=1（后续帧自 2 起）
	c.mu.Unlock()
	ts := time.Now().UTC().UnixMilli()
	nonce, _ := wire.RandomHex(16)
	mac, err := wire.ComputeHMAC(c.tokenHex, "", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", 1, ts, nonce, wire.EmptyPayloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	hello["meta"] = wire.FrameMeta{
		Ver: 2, Ts: ts, Nonce: nonce, HMAC: mac, Seq: 1,
		EventID: "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", CommandID: "", TokenVersion: tokenVer,
	}
	raw, _ := json.Marshal(hello)
	c.send(raw)
	sessionID := wsURL[strings.LastIndex(wsURL, "/")+1:]
	c.readHelloAck(sessionID, lastReceivedSeq+1)
	return c, sessionID
}

func waitFor(t *testing.T, timeout time.Duration, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok, msg := cond(); ok {
			return
		} else if msg != "" {
			t.Fatalf("条件失败: %s", msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// ---- 集成用例 ----

// TestPairStoreHashOnly：deviceToken 仅存 hash——DB 任何字段不含原始 token。
func TestPairStoreHashOnly(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	rows, err := a.db.QueryDump(context.Background(), `SELECT device_id, COALESCE(robot_id,''), device_name, ed25519_public_key, device_x25519_public, token_hash, token_version, status FROM devices`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		found = true
		joined := strings.Join(row, "|")
		if strings.Contains(joined, m.tokenHex) {
			t.Fatalf("devices 表泄漏原始 deviceToken: %s", joined)
		}
		sum := sha256Hex(m.tokenHex)
		if !strings.Contains(joined, sum) {
			t.Fatalf("devices.token_hash ≠ SHA-256(deviceToken): %s", joined)
		}
	}
	if !found {
		t.Fatal("devices 表无设备行")
	}
}

// TestV2HappyPathEncrypted：配对→会话→hello→hello_ack→加密指令下发→结果回执→终态+outbox。
func TestV2HappyPathEncrypted(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, sessionID := openV2Session(t, a, m, 2 /*encryptTypeMax*/, 0, 0, 1)

	res, err := a.pipeline.Submit(context.Background(), scheduler.SubmitRequest{
		Target: m.deviceID, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"integration"}`,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := a.hub.DispatchQueued(context.Background(), m.deviceID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	frame := c.readFrame()
	if frame.SocketType != wire.SocketMessageList {
		t.Fatalf("指令帧 socketType = %d", frame.SocketType)
	}
	if frame.EncryptType != 2 {
		t.Fatalf("encryptTypeMax=2 时指令应加密下发，实际 encryptType=%d", frame.EncryptType)
	}
	meta, _ := frame.V2Meta()
	if meta.CommandID != res.CommandID {
		t.Fatalf("指令帧 commandId = %s, want %s", meta.CommandID, res.CommandID)
	}
	if meta.Seq != 2 { // hello_ack 为服务端首帧（seq=1），指令帧应为 seq=2
		t.Fatalf("指令帧 seq = %d, want 2", meta.Seq)
	}
	plain := c.verifyServerFrame(frame, sessionID)
	if !strings.Contains(string(plain), `"type":203`) {
		t.Fatalf("解密载荷与提交不符: %s", plain)
	}

	// 设备回执执行结果（socketType=3，meta.commandId 回填；结果帧蕴含传输+启动证据）
	result := c.buildFrame(wire.SocketRawConfirm, json.RawMessage(`[{"type":203,"errorCode":0,"runTime":"2026-11-02 10:00:06","timeCost":42,"successList":["外部群测试群"],"failList":[],"metaJson":"{\"dispatchId\":\"`+res.CommandID+`\"}"}]`), res.CommandID, nil)
	c.send(result)

	waitFor(t, 3*time.Second, func() (bool, string) {
		cmd, ok, _ := a.db.GetCommand(context.Background(), res.CommandID)
		return ok && cmd.State == "succeeded", ""
	})
	// outbox 元数据行
	items, err := a.db.OutboxByStatus(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CommandID != res.CommandID {
		t.Fatalf("outbox 行错误: %+v", items)
	}
	// 回调载荷仅元数据：不得 echo 指令正文（receivedContent 等）；successList 为
	// 设备回执 ExecCallbackBean 的结果元数据（apifox 兼容面），允许携带
	if strings.Contains(items[0].Payload, "integration") {
		t.Fatalf("回调载荷不得 echo 指令正文（泄露面收敛）: %s", items[0].Payload)
	}
}

// TestV2NegativeFrames：验签/重放/篡改/乱序否定用例（G8）。
func TestV2NegativeFrames(t *testing.T) {
	build := func(t *testing.T) (*testApp, *deviceMaterial, *deviceClient) {
		a := newTestApp(t)
		m := a.pairDevice(t)
		c, _ := openV2Session(t, a, m, 0, 0, 0, 1)
		return a, m, c
	}
	heartbeat := func(c *deviceClient) []byte {
		return c.buildFrame(wire.SocketHeartbeat, nil, "", nil)
	}

	t.Run("错 HMAC → 4401", func(t *testing.T) {
		_, _, c := build(t)
		c.send(c.buildFrame(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.HMAC = strings.Repeat("0", 64) }))
		c.expectClose(wire.CloseAuthFailure)
	})
	t.Run("篡改 seq（声明 hmac 不重算）→ 4401（C-9）", func(t *testing.T) {
		_, _, c := build(t)
		c.send(c.buildFrame(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.Seq = 99 }))
		c.expectClose(wire.CloseAuthFailure)
	})
	t.Run("nonce 重放 → 4402", func(t *testing.T) {
		_, _, c := build(t)
		frame := heartbeat(c)
		c.send(frame)
		c.send(frame) // 同 nonce 同帧重发
		// 第一帧合法（无回复），第二帧必须 4402
		c.expectClose(wire.CloseNonceReplay)
	})
	t.Run("ts 超差 → 4403", func(t *testing.T) {
		_, _, c := build(t)
		c.send(c.buildFrameResigned(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.Ts -= 60_000 }))
		c.expectClose(wire.CloseClockSkew)
	})
	t.Run("tokenVersion 落后 → 4404", func(t *testing.T) {
		_, _, c := build(t)
		c.send(c.buildFrameResigned(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.TokenVersion = 99 }))
		c.expectClose(wire.CloseTokenVersion)
	})
	t.Run("真实签名 seq 回退 → 4405（先 903）", func(t *testing.T) {
		a, m, c := build(t)
		_ = a
		_ = m
		// 当前设备 seq=1（hello）；构造 seq=1 的重签帧（回退）
		c.send(c.buildFrameResigned(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.Seq = 1 }))
		// 期望先收到 903 重同步帧，再 4405 关闭
		_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			t.Fatalf("期望 903 帧前收到错误: %v", err)
		}
		env, err := wire.DecodeEnvelope(data)
		if err != nil {
			t.Fatal(err)
		}
		items, _ := env.ListItems()
		if len(items) == 0 || wire.ListItemType(items[0]) != wire.TypeResyncRequest {
			t.Fatalf("期望 903 RESYNC_REQUEST，实际: %s", data)
		}
		c.expectClose(wire.CloseSeqOrEventID)
	})
	t.Run("缺 hmac → 4400", func(t *testing.T) {
		_, _, c := build(t)
		raw := c.buildFrame(wire.SocketHeartbeat, nil, "", nil)
		var env map[string]any
		_ = json.Unmarshal(raw, &env)
		meta := env["meta"].(map[string]any)
		delete(meta, "hmac")
		env["meta"] = meta
		out, _ := json.Marshal(env)
		c.send(out)
		c.expectClose(wire.CloseFrameInvalid)
	})
	t.Run("encryptType=2 携明文 list → 4400", func(t *testing.T) {
		_, _, c := build(t)
		raw := c.buildFrame(wire.SocketMessageList, json.RawMessage(`[{"type":203}]`), "", nil)
		var env map[string]json.RawMessage
		_ = json.Unmarshal(raw, &env)
		delete(env, "encryptedList")
		env["encryptType"] = json.RawMessage(`2`)
		out, _ := json.Marshal(env)
		c.send(out)
		c.expectClose(wire.CloseFrameInvalid)
	})
	t.Run("v1 帧混入 v2 会话 → 4400", func(t *testing.T) {
		_, _, c := build(t)
		c.send([]byte(`{"td":1793613600000}`))
		c.expectClose(wire.CloseFrameInvalid)
	})
}

// TestV2EventIDDedup：同 eventId 同内容（新 seq 重签）幂等无重复副作用；同 eventId
// 异内容 → 4405（spec §5.4.3）。
func TestV2EventIDDedup(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, sessionID := openV2Session(t, a, m, 0, 0, 0, 1)
	res, err := a.pipeline.Submit(context.Background(), scheduler.SubmitRequest{
		Target: m.deviceID, CommandType: 203, Payload: `{"type":203,"receivedContent":"dup"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.DispatchQueued(context.Background(), m.deviceID); err != nil {
		t.Fatal(err)
	}
	_ = c.readFrame() // 指令帧

	// 定制结果帧：显式 eventId + seq（设备重发场景 = 同 eventId 新 seq 重新签名）
	sendResult := func(eventID string, seq int64, errorCode int) {
		t.Helper()
		c.outSeq = seq
		list := json.RawMessage(`[{"type":203,"errorCode":` + strconv.Itoa(errorCode) +
			`,"metaJson":"{\"dispatchId\":\"` + res.CommandID + `\"}"}]`)
		digest, err := wire.PayloadDigest(0, list, "")
		if err != nil {
			t.Fatal(err)
		}
		ts := time.Now().UTC().UnixMilli()
		nonce, _ := wire.RandomHex(16)
		mac, err := wire.ComputeHMAC(c.tokenHex, res.CommandID, eventID, seq, ts, nonce, digest)
		if err != nil {
			t.Fatal(err)
		}
		frame := map[string]any{
			"socketType": wire.SocketRawConfirm,
			"messageId":  wire.BuildMessageID(ts, eventID),
			"apiSend":    1, "encryptType": 0, "list": list,
			"meta": wire.FrameMeta{
				Ver: 2, Ts: ts, Nonce: nonce, HMAC: mac, Seq: seq,
				EventID: eventID, CommandID: res.CommandID, TokenVersion: 1,
			},
		}
		raw, _ := json.Marshal(frame)
		c.send(raw)
	}

	eventID := wire.NewUUID()
	sendResult(eventID, 2, 0)
	waitFor(t, 3*time.Second, func() (bool, string) {
		cmd, ok, _ := a.db.GetCommand(context.Background(), res.CommandID)
		return ok && cmd.State == "succeeded", ""
	})

	// H1 服务端半边：结果帧处理后回写 receipt（socketType=3，同载荷 result/ack）
	receipt := c.readFrame()
	if receipt.SocketType != wire.SocketRawConfirm {
		t.Fatalf("结果帧应回写 receipt，socketType = %d", receipt.SocketType)
	}
	if got := c.verifyServerFrame(receipt, sessionID); !strings.Contains(string(got), `"errorCode":0`) {
		t.Fatalf("receipt 载荷应回显原结果内容: %s", got)
	}

	// 同 eventId 同 digest（新 seq=3 重签重发）→ 幂等回执原确认，无重复副作用
	sendResult(eventID, 3, 0)
	replay := c.readFrame()
	if replay.SocketType != wire.SocketRawConfirm {
		t.Fatalf("重复帧应原确认回执，socketType = %d", replay.SocketType)
	}
	if got := c.verifyServerFrame(replay, sessionID); !strings.Contains(string(got), `"errorCode":0`) {
		t.Fatalf("重复帧 receipt 载荷应与首帧一致: %s", got)
	}
	// 连接应保持：随后发一条合法心跳仍被接受（无回复即无关闭）
	c.send(c.buildFrame(wire.SocketHeartbeat, nil, "", nil))

	// 同 eventId 异 digest（errorCode 变化）→ 先 903 重同步再 4405 关闭
	sendResult(eventID, 5, 1)
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, resyncData, err := c.conn.ReadMessage()
	if err != nil {
		t.Fatalf("期望 903 帧: %v", err)
	}
	rsEnv, err := wire.DecodeEnvelope(resyncData)
	if err != nil {
		t.Fatal(err)
	}
	rsItems, _ := rsEnv.ListItems()
	if len(rsItems) == 0 || wire.ListItemType(rsItems[0]) != wire.TypeResyncRequest {
		t.Fatalf("期望 903 RESYNC_REQUEST，实际 %s", resyncData)
	}
	c.expectClose(wire.CloseSeqOrEventID)

	items, _ := a.db.OutboxByStatus(context.Background(), "pending")
	if len(items) != 1 {
		t.Fatalf("重复/冲突回执不得产生额外 outbox: %+v", items)
	}
}

// TestV2DupReceiptReplay（性能审计 H1 服务端半边回归）：结果帧首处理后服务端
// 回写 receipt 并记忆；同 eventId 重放（设备重发语义 = 同 eventId 新 seq 重签，
// 端侧 AtLeastOnceQueue.refreshForResend）→ 收到同 receipt 回写（同 socketType/
// commandId/载荷内容），且不重复管线副作用（outbox/事件流不变）。
func TestV2DupReceiptReplay(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, sessionID := openV2Session(t, a, m, 0, 0, 0, 1)
	res, err := a.pipeline.Submit(context.Background(), scheduler.SubmitRequest{
		Target: m.deviceID, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"receipt"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.DispatchQueued(context.Background(), m.deviceID); err != nil {
		t.Fatal(err)
	}
	_ = c.readFrame() // 指令帧

	resultList := json.RawMessage(`[{"type":203,"errorCode":0,"timeCost":7,"successList":["外部群测试群"],"failList":[],"metaJson":"{\"dispatchId\":\"` + res.CommandID + `\"}"}]`)
	sendResult := func(eventID string, seq int64) {
		t.Helper()
		c.mu.Lock()
		c.outSeq = seq
		c.mu.Unlock()
		digest, err := wire.PayloadDigest(0, resultList, "")
		if err != nil {
			t.Fatal(err)
		}
		ts := time.Now().UTC().UnixMilli()
		nonce, _ := wire.RandomHex(16)
		mac, err := wire.ComputeHMAC(c.tokenHex, res.CommandID, eventID, seq, ts, nonce, digest)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{
			"socketType": wire.SocketRawConfirm,
			"messageId":  wire.BuildMessageID(ts, eventID),
			"apiSend":    1, "encryptType": 0, "list": resultList,
			"meta": wire.FrameMeta{
				Ver: 2, Ts: ts, Nonce: nonce, HMAC: mac, Seq: seq,
				EventID: eventID, CommandID: res.CommandID, TokenVersion: 1,
			},
		})
		c.send(raw)
	}

	eventID := wire.NewUUID()
	sendResult(eventID, 2)

	// ① 首处理：回写 receipt（socketType=3，载荷回显原结果内容）并记忆
	receipt1 := c.readFrame()
	if receipt1.SocketType != wire.SocketRawConfirm {
		t.Fatalf("首处理应回写 receipt，socketType = %d", receipt1.SocketType)
	}
	plain1 := c.verifyServerFrame(receipt1, sessionID)
	if string(plain1) != string(resultList) {
		t.Fatalf("receipt 载荷应回显原结果帧 list:\n got %s\nwant %s", plain1, resultList)
	}
	m1, _ := receipt1.V2Meta()
	if m1.CommandID != res.CommandID {
		t.Fatalf("receipt meta.commandId = %q, want %q", m1.CommandID, res.CommandID)
	}
	waitFor(t, 3*time.Second, func() (bool, string) {
		cmd, ok, _ := a.db.GetCommand(context.Background(), res.CommandID)
		return ok && cmd.State == "succeeded", ""
	})
	stored, ok, err := a.db.GetInboundReceipt(context.Background(), m.deviceID, eventID)
	if err != nil || !ok || stored == "" {
		t.Fatalf("receipt 应已记忆: ok=%v stored=%q err=%v", ok, stored, err)
	}
	var content struct {
		SocketType int             `json:"socketType"`
		CommandID  string          `json:"commandId"`
		List       json.RawMessage `json:"list"`
	}
	if err := json.Unmarshal([]byte(stored), &content); err != nil {
		t.Fatalf("receipt 内容形态（JSON）: %v (%s)", err, stored)
	}
	if content.SocketType != wire.SocketRawConfirm || content.CommandID != res.CommandID ||
		string(content.List) != string(resultList) {
		t.Fatalf("receipt 内容与回写帧不一致: %+v", content)
	}
	eventsBefore, _ := a.db.Events(context.Background(), res.CommandID)
	outboxBefore, _ := a.db.OutboxByStatus(context.Background(), "pending")

	// ② 同 eventId 重放（新 seq 重签）→ 收到同 receipt 回写；无重复副作用
	sendResult(eventID, 3)
	receipt2 := c.readFrame()
	if receipt2.SocketType != wire.SocketRawConfirm {
		t.Fatalf("重复帧应原确认回执，socketType = %d", receipt2.SocketType)
	}
	plain2 := c.verifyServerFrame(receipt2, sessionID)
	if string(plain2) != string(plain1) {
		t.Fatalf("重复帧 receipt 应同内容回写:\n got %s\nwant %s", plain2, plain1)
	}
	m2, _ := receipt2.V2Meta()
	if m2.CommandID != m1.CommandID {
		t.Fatalf("重复帧 receipt commandId 漂移: %q != %q", m2.CommandID, m1.CommandID)
	}
	if m2.Seq <= m1.Seq {
		t.Fatalf("重复帧 receipt 应为重签新帧（seq 单调），got %d <= %d", m2.Seq, m1.Seq)
	}
	waitFor(t, 2*time.Second, func() (bool, string) { // 服务端消化完重复帧
		eventsAfter, _ := a.db.Events(context.Background(), res.CommandID)
		return len(eventsAfter) == len(eventsBefore), ""
	})
	eventsAfter, _ := a.db.Events(context.Background(), res.CommandID)
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("重放不得新增事件流: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	outboxAfter, _ := a.db.OutboxByStatus(context.Background(), "pending")
	if len(outboxAfter) != len(outboxBefore) {
		t.Fatalf("重放不得新增 outbox: before=%d after=%d", len(outboxBefore), len(outboxAfter))
	}
	// 连接保持：重放后合法心跳仍被接受（无关闭）
	c.send(c.buildFrame(wire.SocketHeartbeat, nil, "", nil))
}

// TestV2FlushDrainsQueue（性能审计 H3 回归）：积压 > 单批上限（64）时，一次
// DispatchQueued 冲刷到队空（原实现每秒泵至多一批，吞吐被泵周期封顶）。
func TestV2FlushDrainsQueue(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	c, _ := openV2Session(t, a, m, 0, 0, 0, 1)

	const total = 70 // > v2FlushBatchSize(64)：至少跨两批
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		r, err := a.pipeline.Submit(context.Background(), scheduler.SubmitRequest{
			Target: m.deviceID, CommandType: 203,
			Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"q` + strconv.Itoa(i) + `"}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.CommandID)
	}
	n, err := a.hub.DispatchQueued(context.Background(), m.deviceID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if n != total {
		t.Fatalf("一次冲刷应排空全队 %d 条，实际 %d（每秒至多一批的旧语义？）", total, n)
	}
	for _, id := range ids {
		cmd, ok, _ := a.db.GetCommand(context.Background(), id)
		if !ok || cmd.State != "dispatched" {
			t.Fatalf("指令 %s 状态 = %v（应 dispatched）", id, cmd.State)
		}
		if cmd.Epoch <= 0 {
			t.Fatalf("指令 %s epoch 未分配", id)
		}
	}
	// 全部下发帧可读（指令帧 total 条），epoch 单调递增
	lastEpoch := int64(0)
	for i := 0; i < total; i++ {
		env := c.readFrame()
		meta, _ := env.V2Meta()
		if meta.Seq != int64(i+2) { // hello_ack=1，指令帧自 2 连续递增
			t.Fatalf("指令帧 seq = %d, want %d", meta.Seq, i+2)
		}
		plain := c.verifyServerFrame(env, "")
		var items []json.RawMessage
		if err := json.Unmarshal(plain, &items); err != nil || len(items) != 1 {
			t.Fatalf("指令载荷应为单元数组: %s", plain)
		}
		var probe struct {
			Epoch int64 `json:"epoch"`
		}
		if err := json.Unmarshal(items[0], &probe); err != nil {
			t.Fatalf("epoch 解析: %v (%s)", err, items[0])
		}
		if probe.Epoch <= lastEpoch {
			t.Fatalf("epoch 应单调递增: %d <= %d", probe.Epoch, lastEpoch)
		}
		lastEpoch = probe.Epoch
	}
	// 队空：再次冲刷返回 0（排空收敛，不死循环）
	if n, err := a.hub.DispatchQueued(context.Background(), m.deviceID); err != nil || n != 0 {
		t.Fatalf("队空冲刷应返回 0: n=%d err=%v", n, err)
	}
}

// TestV1CompatPath：v1 兼容信令全链（td/裸心跳/下发/确认/结果/上报）。
func TestV1CompatPath(t *testing.T) {
	a := newTestApp(t)
	const robotID = "robot-v1-77"
	wsURL := "ws" + strings.TrimPrefix(a.httpSrv.URL, "http") + "/webserver/wework/" + robotID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial v1: %v", err)
	}
	defer func() { _ = conn.Close() }()
	sendV1 := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			t.Fatalf("v1 发帧: %v", err)
		}
	}
	readV1 := func() *wire.Envelope {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("v1 读帧: %v", err)
		}
		env, err := wire.DecodeEnvelope(data)
		if err != nil {
			t.Fatalf("v1 帧解析: %v (%s)", err, data)
		}
		return env
	}

	// onOpen td + 裸心跳（上游还原形态，须接受）
	sendV1(map[string]int64{"td": 1793613600000})
	sendV1(map[string]int{"type": 11})

	res, err := a.pipeline.Submit(context.Background(), scheduler.SubmitRequest{
		Target: "v1:" + robotID, CommandType: 203,
		Payload: `{"type":203,"targetRoom":"外部群测试群","receivedContent":"v1-compat"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.hub.DispatchQueued(context.Background(), "v1:"+robotID); err != nil {
		t.Fatal(err)
	}

	env := readV1()
	if env.SocketType != wire.SocketMessageList || env.APISend == nil || *env.APISend != 1 {
		t.Fatalf("v1 下发信封形状错误: %+v", env)
	}
	if env.IsV2() || !wire.MessageIDPattern.MatchString(env.MessageID) {
		t.Fatalf("v1 信封 messageId/meta 形态错误: %q v2=%v", env.MessageID, env.IsV2())
	}
	items, _ := env.ListItems()
	if len(items) != 1 || wire.ListItemType(items[0]) != 203 {
		t.Fatalf("v1 下发载荷错误: %s", env.List)
	}

	// socketType=1 消息确认 → E05
	sendV1(map[string]any{"socketType": 1, "messageId": env.MessageID, "meta": "", "apiSend": 1, "encryptType": 0, "list": []any{}})
	waitFor(t, 3*time.Second, func() (bool, string) {
		cmd, ok, _ := a.db.GetCommand(context.Background(), res.CommandID)
		return ok && cmd.State == "acked", ""
	})

	// socketType=3 RAW_CONFIRM 结果（metaJson.dispatchId 回配）→ E10
	sendV1(map[string]any{
		"socketType": 3, "messageId": env.MessageID, "meta": "", "apiSend": 1, "encryptType": 0,
		"list": []any{map[string]any{
			"type": 203, "errorCode": 0, "timeCost": 12,
			"successList": []string{"外部群测试群"}, "failList": []any{},
			"metaJson": `{"dispatchId":"` + res.CommandID + `"}`,
		}},
	})
	waitFor(t, 3*time.Second, func() (bool, string) {
		cmd, ok, _ := a.db.GetCommand(context.Background(), res.CommandID)
		return ok && cmd.State == "succeeded", ""
	})

	// 101 上报 → outbox report（仅元数据）
	sendV1(map[string]any{
		"socketType": 2, "messageId": wire.BuildMessageID(time.Now().UnixMilli(), wire.NewUUID()),
		"meta": "", "apiSend": 0, "encryptType": 0,
		"list": []any{map[string]any{"type": 101, "roomType": 2, "textType": 1}},
	})
	waitFor(t, 3*time.Second, func() (bool, string) {
		reports, err := a.db.OutboxByStatus(context.Background(), "pending")
		if err != nil {
			return false, ""
		}
		for _, r := range reports {
			if r.Kind == "report" && strings.Contains(r.Payload, `"reportType":101`) {
				return true, ""
			}
		}
		return false, ""
	})
}

// TestV2FrameOnV1PathRejected：v1 路径不接受 v2 帧（同设备会话不混用，spec §2）。
func TestV2FrameOnV1PathRejected(t *testing.T) {
	a := newTestApp(t)
	const robotID = "robot-mixed"
	wsURL := "ws" + strings.TrimPrefix(a.httpSrv.URL, "http") + "/webserver/wework/" + robotID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	raw := `{"socketType":0,"meta":{"ver":2,"ts":1,"nonce":"` + strings.Repeat("a", 32) + `","hmac":"` + strings.Repeat("b", 64) + `","seq":1,"eventId":"1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d","commandId":"","tokenVersion":1},"encryptType":0,"list":[]}`
	if err := conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err = conn.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok || ce.Code != websocket.CloseNormalClosure {
		// v1 路径对 v2 帧实现为撕裂连接（handleFrame 返回 true）
		t.Fatalf("期望连接被关闭（v2 帧混入 v1 路径），实际: %v", err)
	}
}

// TestPairNegativeCases：配对否定用例（格式 400 / 码无效过期已用 403）。
// H-1 后 pair 面 per-IP 限速默认 5 次/分钟，本例 7 次 POST → 放宽限速注入
// （对齐 compat_test.go 包级 var 注入形态；限速/封禁路径归 pair_guard 专项测试）。
func TestPairNegativeCases(t *testing.T) {
	api.PairRatePerMin = 1000
	defer func() { api.PairRatePerMin = 5 }()
	a := newTestApp(t)
	base := func(code string) map[string]string {
		return map[string]string{
			"pairingCode":              code,
			"deviceId":                 "device-neg-0001",
			"ed25519PublicKey":         strings.Repeat("ab", 32),
			"x25519EphemeralPublicKey": strings.Repeat("cd", 32),
		}
	}
	t.Run("码非 6 位数字 → 400", func(t *testing.T) {
		if r := a.post(t, "/api/v2/pair", base("12ab!")); r.code != http.StatusBadRequest {
			t.Fatalf("= %d: %s", r.code, r.body)
		}
	})
	t.Run("未知码 → 403 PAIRING_CODE_INVALID", func(t *testing.T) {
		if r := a.post(t, "/api/v2/pair", base("314159")); r.code != http.StatusForbidden || !strings.Contains(string(r.body), "PAIRING_CODE_INVALID") {
			t.Fatalf("= %d: %s", r.code, r.body)
		}
	})
	t.Run("公钥非法 → 400", func(t *testing.T) {
		body := base("314159")
		body["ed25519PublicKey"] = "not-hex"
		if r := a.post(t, "/api/v2/pair", body); r.code != http.StatusBadRequest {
			t.Fatalf("= %d: %s", r.code, r.body)
		}
	})
	t.Run("未知字段 → 400（additionalProperties:false）", func(t *testing.T) {
		body := map[string]string{"extra": "x"}
		for k, v := range base("314159") {
			body[k] = v
		}
		if r := a.post(t, "/api/v2/pair", body); r.code != http.StatusBadRequest {
			t.Fatalf("= %d: %s", r.code, r.body)
		}
	})
	t.Run("单次有效：已用码 → 403", func(t *testing.T) {
		code, err := a.apiSrv.CreatePairingCode()
		if err != nil {
			t.Fatal(err)
		}
		first := a.post(t, "/api/v2/pair", base(code))
		if first.code != http.StatusCreated {
			t.Fatalf("首次配对 = %d: %s", first.code, first.body)
		}
		second := a.post(t, "/api/v2/pair", base(code))
		if second.code != http.StatusForbidden {
			t.Fatalf("二次使用同码 = %d: %s", second.code, second.body)
		}
	})
	t.Run("过期码 → 403", func(t *testing.T) {
		code, err := a.apiSrv.CreatePairingCode()
		if err != nil {
			t.Fatal(err)
		}
		// 直接过期登记（构造 TTL 已过的码）
		if err := a.db.CreatePairingCode(context.Background(), sha256Hex("000001"), time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if r := a.post(t, "/api/v2/pair", base("000001")); r.code != http.StatusForbidden {
			t.Fatalf("过期码 = %d: %s", r.code, r.body)
		}
		_ = code
	})
}

// TestRenegotiateAfterCredLoss：凭据失忆（重启模拟）→ 401 → Ed25519 重协商换发 → 会话可用。
func TestRenegotiateAfterCredLoss(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)

	a.hub.DropCred(m.deviceID) // 模拟服务端重启：内存凭据失忆

	// 无重协商证据 → 401 RENEGOTIATION_REQUIRED
	resp, err := http.Post(a.httpSrv.URL+"/api/v2/device-sessions", "application/json",
		strings.NewReader(`{"deviceId":"`+m.deviceID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("失忆后建会话 = %d, want 401", resp.StatusCode)
	}

	// Ed25519 重协商（复用配对注册的签名公钥，spec §5.5.1）
	ts := time.Now().UTC().UnixMilli()
	nonce, _ := wire.RandomHex(16)
	msg := []byte(m.deviceID + "|" + strconv.FormatInt(ts, 10) + "|" + nonce)
	sig := ed25519.Sign(m.edPriv, msg)
	body := map[string]any{
		"deviceId": m.deviceID,
		"renegotiate": map[string]any{
			"ts": ts, "nonce": nonce, "signature": toHex(sig),
		},
	}
	rotated := a.post(t, "/api/v2/device-sessions", body)
	if rotated.code != http.StatusCreated {
		t.Fatalf("重协商 = %d: %s", rotated.code, rotated.body)
	}
	var cr struct {
		CredentialRotated bool   `json:"credentialRotated"`
		DeviceToken       string `json:"deviceToken"`
		TokenVersion      int64  `json:"tokenVersion"`
	}
	_ = json.Unmarshal(rotated.body, &cr)
	if !cr.CredentialRotated || cr.DeviceToken == "" || cr.TokenVersion != 2 {
		t.Fatalf("重协商应换发凭据（tokenVersion+1）: %+v", cr)
	}

	// 新凭据可建会话并 hello 成功（tokenVersion=2）
	m.tokenHex = cr.DeviceToken
	c, _ := openV2Session(t, a, m, 0, 0, 0, 2)
	_ = c
	// 旧 token 失效：以旧凭据签名帧应 4401（服务端只认新 version/token）
	c.tokenHex = cr.DeviceToken
	c.send(c.buildFrameResigned(wire.SocketHeartbeat, nil, "", func(meta *wire.FrameMeta) { meta.TokenVersion = 1 }))
	// tokenVersion=1 落后 → 4404
	c.expectClose(wire.CloseTokenVersion)
}
