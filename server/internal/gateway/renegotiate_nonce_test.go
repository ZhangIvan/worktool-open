// M-2（安全审计 TOP5）凭据重协商 nonce 单次化回归：同一 Ed25519 重协商证据
// （ts|nonce|signature）窗口内重放被拒；换新 nonce 的证据不受误伤。
package gateway_test

import (
	"crypto/ed25519"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/wire"
)

func TestRenegotiateNonceReplayRejected(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)

	ts := time.Now().UTC().UnixMilli()
	nonce, err := wire.RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(m.deviceID + "|" + strconv.FormatInt(ts, 10) + "|" + nonce)
	sigHex := toHex(ed25519.Sign(m.edPriv, msg))
	proof := func() map[string]any { // 固定证据：ts/nonce/signature 逐字节相同
		return map[string]any{
			"deviceId": m.deviceID,
			"renegotiate": map[string]any{
				"ts": ts, "nonce": nonce, "signature": sigHex,
			},
		}
	}

	a.hub.DropCred(m.deviceID) // 模拟重启：凭据失忆
	first := a.post(t, "/api/v2/device-sessions", proof())
	if first.code != http.StatusCreated {
		t.Fatalf("首次重协商 = %d: %s", first.code, first.body)
	}

	// 同一证据重放：先令凭据再度失忆（否则走 cred!=nil 路径不触重协商）
	a.hub.DropCred(m.deviceID)
	replay := a.post(t, "/api/v2/device-sessions", proof())
	if replay.code != http.StatusUnauthorized || !strings.Contains(string(replay.body), "RENEGOTIATE_FAILED") {
		t.Fatalf("同 nonce 重放应 401 RENEGOTIATE_FAILED: %d %s", replay.code, replay.body)
	}

	// 新 nonce 的新证据：单次化不误伤合法换发
	a.hub.DropCred(m.deviceID)
	nonce2, _ := wire.RandomHex(16)
	ts2 := time.Now().UTC().UnixMilli()
	msg2 := []byte(m.deviceID + "|" + strconv.FormatInt(ts2, 10) + "|" + nonce2)
	fresh := a.post(t, "/api/v2/device-sessions", map[string]any{
		"deviceId": m.deviceID,
		"renegotiate": map[string]any{
			"ts": ts2, "nonce": nonce2, "signature": toHex(ed25519.Sign(m.edPriv, msg2)),
		},
	})
	if fresh.code != http.StatusCreated {
		t.Fatalf("新 nonce 重协商应 201: %d %s", fresh.code, fresh.body)
	}
}
