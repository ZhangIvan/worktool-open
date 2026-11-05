// requestId 中间件 api 面接线回归（requestId 车道，2026-11-05）：NewServeMux
// 全局面响应回显 X-Request-Id（真实 Server 装配；/openapi 面与 /healthz 同源
// 包装）。
package api

import (
	"net/http"
	"regexp"
	"testing"
)

var wantHex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

func TestAPIRequestIDHeader(t *testing.T) {
	f := newOpenAPIFixture(t)
	// 无请求头：生成 16 hex 回显。
	resp, err := http.Get(f.srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !wantHex16.MatchString(resp.Header.Get("X-Request-Id")) {
		t.Fatalf("api 面响应应回显生成的 16 hex rid，got %q", resp.Header.Get("X-Request-Id"))
	}

	// 带请求头：原样回显（兼容面 /wework/sendRawMessage 由 compat.Mount 自包）。
	req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/openapi/v1/robots/robot-reqid/info", nil)
	req.Header.Set("X-Request-Id", "api-trace-0001")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if got := resp2.Header.Get("X-Request-Id"); got != "api-trace-0001" {
		t.Fatalf("合法请求头应原样回显，got %q", got)
	}
}
