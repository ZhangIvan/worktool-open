// 会话级结构化日志回归测试（审计复核收口：服务端此前无会话打点，自愈只能以
// devices.token_version 间接佐证）：
//   - hello / hello_ack：每次成功建会话各一条，含 deviceId 短哈希（SHA-256 前
//     8 hex）与 ts（RFC3339），不含原始 token/deviceId 等敏感材料；
//   - renegotiate / token_version_rotated：凭据换发成功各一条（renegotiate 行
//     tokenVersion=换发前版本，rotated 行=新版本）；
//   - 被拒 hello（如 tokenVersion 失配）不产生会话打点（仅成功事件留痕）。
package gateway_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

// captureLogger 为可断言日志桩（替换 hub.Log 捕获服务端日志行）。
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// snapshot 返回已捕获日志行副本。
func (l *captureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.lines))
	copy(out, l.lines)
	return out
}

// count 统计含 substr 的行数。
func (l *captureLogger) count(substr string) int {
	n := 0
	for _, ln := range l.snapshot() {
		if strings.Contains(ln, substr) {
			n++
		}
	}
	return n
}

// none 报告没有任何行包含 substr（敏感材料不落日志断言）。
func (l *captureLogger) none(substr string) bool { return l.count(substr) == 0 }

// shortHash 与实现同形的 deviceId 短哈希（SHA-256 前 4 字节 → 8 hex）。
func shortHash(deviceID string) string {
	sum := sha256.Sum256([]byte(deviceID))
	return hex.EncodeToString(sum[:4])
}

// sessionLogEvent 行字段提取：返回行内 event= 字段的精确值（无则空串）。
func sessionLogEvent(line string) string {
	for _, f := range strings.Fields(line) {
		if rest, ok := strings.CutPrefix(f, "event="); ok {
			return rest
		}
	}
	return ""
}

// assertSessionLine 断言恰一条 gateway: session event=<event> 日志行，且含
// deviceId 短哈希、session 键、tokenVersion 与可解析 RFC3339 ts。
func assertSessionLine(t *testing.T, l *captureLogger, event, deviceID, sessionID string, tokenVersion int64) {
	t.Helper()
	var line string
	found := 0
	for _, ln := range l.snapshot() {
		if !strings.HasPrefix(ln, "gateway: session event=") || sessionLogEvent(ln) != event {
			continue
		}
		found++
		line = ln
	}
	if found != 1 {
		t.Fatalf("event=%s 应恰一条会话日志行，got %d；已捕获:\n%s", event, found, strings.Join(l.snapshot(), "\n"))
	}
	for _, frag := range []string{
		"device=" + shortHash(deviceID),
		"session=" + sessionID,
		"tokenVersion=" + strconv.FormatInt(tokenVersion, 10),
	} {
		if !strings.Contains(line, frag) {
			t.Fatalf("event=%s 行缺 %s：%q", event, frag, line)
		}
	}
	i := strings.Index(line, " ts=")
	if i < 0 {
		t.Fatalf("event=%s 行缺 ts：%q", event, line)
	}
	ts := strings.TrimSpace(line[i+4:])
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Fatalf("event=%s 行 ts 非可解析 RFC3339（%q）：%v", event, ts, err)
	}
}

// TestSessionLogHelloAndAck：成功建会话 → hello 与 hello_ack 各一条结构化日志；
// 原始 deviceToken / 原始 deviceId 不出现在任何日志行。
func TestSessionLogHelloAndAck(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	capture := &captureLogger{}
	a.hub.Log = capture // 日志桩注入（Hub.Log 为最小 Logger 接口）

	c, sessionID := openV2Session(t, a, m, 0, 0, 0, 1)
	_ = c

	assertSessionLine(t, capture, "hello", m.deviceID, sessionID, 1)
	assertSessionLine(t, capture, "hello_ack", m.deviceID, sessionID, 1)
	// 脱敏：原始 token / 原始 deviceId 不得落日志（仅短哈希）。
	if !capture.none(m.tokenHex) {
		t.Fatal("会话日志不得包含原始 deviceToken")
	}
	if !capture.none("device=" + m.deviceID) {
		t.Fatal("会话日志不得包含原始 deviceId（仅短哈希）")
	}
}

// TestSessionLogRejectedHelloNotLogged：被拒 hello（tokenVersion 失配 → 4404）
// 不产生 event=hello 打点（仅成功建会话留痕；拒帧已有既有日志路径）。
func TestSessionLogRejectedHelloNotLogged(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	capture := &captureLogger{}
	a.hub.Log = capture

	wsURL, serverPub, _, _ := a.createSession(t, m, nil)
	c := dialV2(t, wsURL, m, serverPub, m.tokenHex, 1)
	hello := map[string]any{
		"type": "hello", "deviceId": m.deviceID, "tokenVersion": 2, // 服务端持 version 1
		"lastEpoch": 0, "lastReceivedSeq": 0,
		"capabilities": map[string]any{"clientVersion": "test-0.1", "encryptTypeMax": 0, "commands": []int{203}},
	}
	ts := time.Now().UTC().UnixMilli()
	nonce, _ := wire.RandomHex(16)
	mac, err := wire.ComputeHMAC(m.tokenHex, "", "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", 1, ts, nonce, wire.EmptyPayloadDigest)
	if err != nil {
		t.Fatal(err)
	}
	hello["meta"] = wire.FrameMeta{
		Ver: 2, Ts: ts, Nonce: nonce, HMAC: mac, Seq: 1,
		EventID: "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", TokenVersion: 2,
	}
	raw, _ := json.Marshal(hello)
	c.send(raw)
	c.expectClose(wire.CloseTokenVersion)

	if n := capture.count("event=hello"); n != 0 {
		t.Fatalf("被拒 hello 不应产生会话打点，got %d 条", n)
	}
}

// TestSessionLogRenegotiateAndTokenRotation：凭据失忆 → §5.5.1 重协商换发 →
// renegotiate（tokenVersion=换发前版本）与 token_version_rotated（=新版本）各一条。
func TestSessionLogRenegotiateAndTokenRotation(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	capture := &captureLogger{}
	a.hub.Log = capture

	a.hub.DropCred(m.deviceID) // 模拟服务端重启：内存凭据失忆

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

	// 换发前无会话可挂：session 键为 "-"。
	assertSessionLine(t, capture, "renegotiate", m.deviceID, "-", 1)
	assertSessionLine(t, capture, "token_version_rotated", m.deviceID, "-", 2)
	if !capture.none(m.tokenHex) {
		t.Fatal("换发日志不得包含原始 deviceToken")
	}
}
