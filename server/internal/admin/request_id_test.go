// requestId 中间件管理面接线回归（requestId 车道，2026-11-05）：admin.Register
// 挂载的管理面全部响应回显 X-Request-Id——无头生成 16 hex、带头原样回显。
package admin

import (
	"net/http"
	"regexp"
	"testing"
)

var wantHex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestAdminRequestIDHeader(t *testing.T) {
	f := newFixture(t)

	// 无请求头：401 响应（鉴权拒绝路径）也必须带生成的 16 hex rid。
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/admin/devices", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rid := resp.Header.Get("X-Request-Id")
	_ = resp.Body.Close()
	if !wantHex16.MatchString(rid) {
		t.Fatalf("管理面响应应回显生成的 16 hex rid，got %q", rid)
	}

	// 带请求头：原样回显（调用方追踪链贯通）。
	req2, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/admin/devices", nil)
	req2.Header.Set("X-Request-Id", "trace-case-0001")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if got := resp2.Header.Get("X-Request-Id"); got != "trace-case-0001" {
		t.Fatalf("合法请求头应原样回显，got %q", got)
	}
}
