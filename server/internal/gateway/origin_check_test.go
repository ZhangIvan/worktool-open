// H-2（安全审计 TOP5）WS 升级同源校验回归：带 Origin 头的握手须同源——跨源
// 403；同源放行；无 Origin（设备端/测试客户端载体）放行。
package gateway_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSOriginCheck(t *testing.T) {
	a := newTestApp(t)
	m := a.pairDevice(t)
	wsURL, _, _, _ := a.createSession(t, m, nil)
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}

	// 跨源 Origin → 403 拒绝
	_, resp, err := dialer.Dial(wsURL, http.Header{"Origin": []string{"https://evil.example"}})
	if err == nil {
		t.Fatal("跨源 Origin 应被拒绝")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨源握手应 403: resp=%v err=%v", resp, err)
	}

	// 同源 Origin（httptest 地址）→ 放行
	u, err := url.Parse(a.httpSrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := dialer.Dial(wsURL, http.Header{"Origin": []string{"http://" + u.Host}})
	if err != nil {
		t.Fatalf("同源 Origin 应放行: %v", err)
	}
	_ = conn.Close()

	// 无 Origin 头（设备端载体）→ 放行
	conn2, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("无 Origin 应放行: %v", err)
	}
	_ = conn2.Close()
}
