// L-1（CSP）+ L-2（STORE 错误原文不出网）回归测试。
//
//   - L-1：管理面全部响应（/api/v1/admin/* 与 /admin/ 静态面，含 4xx/5xx/302）
//     统一携带冻结值 Content-Security-Policy。
//   - L-2：STORE 类 500 对外固定文案「存储错误（详情见服务端日志）」，
//     原始 err.Error()（schema/路径/驱动细节）只进服务端日志。
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adminCSPWant 为 L-1 冻结头值（任务卡原文；default-src 'none' 兜底，
// script/style 放行内联——管理台为单页内联脚本形态）。
const adminCSPWant = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'"

// rawCall 发起一次管理面请求，返回状态码、CSP 头与响应体原文（头/原文断言用）。
func rawCall(t *testing.T, srv *httptest.Server, method, path, token string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Security-Policy"), buf.String()
}

// TestAdminCSPHeaderUnified：管理面两类前缀（API + UI 静态）全响应统一 CSP。
func TestAdminCSPHeaderUnified(t *testing.T) {
	f := newFixture(t)

	// API 面：200 与 401（未授权）响应都必须带头。
	for _, tc := range []struct {
		name  string
		token string
		want  int
	}{
		{"已授权 200", f.token, http.StatusOK},
		{"未授权 401", "", http.StatusUnauthorized},
	} {
		st, csp, _ := rawCall(t, f.srv, http.MethodGet, "/api/v1/admin/devices", tc.token)
		if st != tc.want {
			t.Fatalf("%s：status=%d want=%d", tc.name, st, tc.want)
		}
		if csp != adminCSPWant {
			t.Fatalf("%s：CSP 头 = %q, want %q", tc.name, csp, adminCSPWant)
		}
	}

	// UI 静态面（/admin → 302、/admin/ 与 /admin/index.html → 200）。
	f.s.uiDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(f.s.uiDir, "index.html"), []byte("<html><body>ui</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, path := range []string{"/admin", "/admin/", "/admin/index.html"} {
		st, csp, _ := rawCall(t, srv, http.MethodGet, path, "")
		if st == 0 {
			t.Fatalf("%s：请求失败", path)
		}
		if csp != adminCSPWant {
			t.Fatalf("%s：CSP 头 = %q, want %q", path, csp, adminCSPWant)
		}
	}
}

// TestStoreErrorFixedMessage（L-2）：存储层失败（此处以关闭 DB 注入）→
// 500 + code=STORE + 固定文案；原始错误细节（驱动文本）不得出网。
func TestStoreErrorFixedMessage(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	st, csp, body := rawCall(t, f.srv, http.MethodGet, "/api/v1/admin/devices", f.token)
	if st != http.StatusInternalServerError {
		t.Fatalf("存储失败应 500，got %d body=%s", st, body)
	}
	if csp == "" {
		t.Fatalf("L-1：错误响应也应携带 CSP 头")
	}
	var out struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(body))).Decode(&out); err != nil {
		t.Fatalf("响应须为 JSON 信封：%v body=%s", err, body)
	}
	if out.Error != "STORE" {
		t.Fatalf("错误码应保持 STORE（可分类），got %q", out.Error)
	}
	if out.Message != "存储错误（详情见服务端日志）" {
		t.Fatalf("对外文案应固定，got %q", out.Message)
	}
	// 原文不出网：驱动/底层细节（如 "sql: database is closed"）不得出现在响应体。
	if strings.Contains(body, "sql:") || strings.Contains(body, "database is closed") {
		t.Fatalf("原始 STORE 错误原文泄漏到响应体：%s", body)
	}
}
