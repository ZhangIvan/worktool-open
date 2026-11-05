// httpx 中间件回归测试（requestId 中间件车道，2026-11-05）：
//   - 无 X-Request-Id 请求头 → 生成 16 hex，响应头回显，服务端日志行携带；
//   - 携带合法 X-Request-Id → 原样回显（请求方追踪链路贯通）；
//   - 携带含控制字符/超长值 → 清洗后回显（不回注控制字符，防日志注入）；
//   - 嵌套（api 全局面包 admin 子树）→ 内层复用外层 rid 且不重复记访问日志；
//   - Flush/Hijack 转发（审计导出流式 flush 与 WS 升级不被包装破坏）。
package httpx

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

type capLogger struct{ lines []string }

func (l *capLogger) Printf(format string, args ...any) {
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

var wantHex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// wantRid 从访问日志行提取 rid= 后的值（至下一空白）。
var wantRid = regexp.MustCompile(`rid=(\S+)`)

func do(t *testing.T, lg *capLogger, header string, inner http.HandlerFunc) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h := RequestID(lg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inner != nil {
			inner(w, r)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set("X-Request-Id", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Header().Get("X-Request-Id")
}

// TestGenerates16HexAndLogs：无请求头 → 生成 16 hex、响应头回显、日志行携带同一 rid。
func TestGenerates16HexAndLogs(t *testing.T) {
	lg := &capLogger{}
	rec, rid := do(t, lg, "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // 状态捕获：非 200 也要如实记
	}))
	if !wantHex16.MatchString(rid) {
		t.Fatalf("生成 rid 应为 16 hex，got %q", rid)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("handler 状态应透传，got %d", rec.Code)
	}
	if len(lg.lines) != 1 {
		t.Fatalf("应恰一条访问日志: %v", lg.lines)
	}
	line := lg.lines[0]
	for _, want := range []string{"rid=" + rid, "GET", "/probe", "418"} {
		if !strings.Contains(line, want) {
			t.Fatalf("日志行应含 %q: %q", want, line)
		}
	}
}

// TestEchoesProvidedHeader：合法请求头原样回显（响应头与日志一致）。
func TestEchoesProvidedHeader(t *testing.T) {
	lg := &capLogger{}
	_, rid := do(t, lg, "abc123DEF456_789-x", nil)
	if rid != "abc123DEF456_789-x" {
		t.Fatalf("合法 rid 应原样回显，got %q", rid)
	}
	if len(lg.lines) != 1 || !strings.Contains(lg.lines[0], "rid=abc123DEF456_789-x") {
		t.Fatalf("日志应携带请求方 rid: %v", lg.lines)
	}
}

// TestSanitizesUnsafeHeader：控制字符/超长 rid 清洗——响应头与日志不得含控制字符。
func TestSanitizesUnsafeHeader(t *testing.T) {
	lg := &capLogger{}
	rec, rid := do(t, lg, "evil\ninject\r\tvalue"+"x", nil)
	if strings.ContainsAny(rid, "\n\r\t") {
		t.Fatalf("rid 不得含控制字符: %q", rid)
	}
	if !strings.Contains(rid, "evilinjectvalue") { // 非法字符剥除，合法部分保留
		t.Fatalf("合法字符应保留: %q", rid)
	}
	if len(rid) > maxRIDLen {
		t.Fatalf("rid 应截断至 %d 内: %q", maxRIDLen, rid)
	}
	if rec.Header().Get("X-Request-Id") != rid {
		t.Fatalf("响应头应与清洗后 rid 一致")
	}
	// 纯非法字符（清洗后为空）→ 回落生成 16 hex。
	_, rid2 := do(t, lg, "\n\r\t", nil)
	if !wantHex16.MatchString(rid2) {
		t.Fatalf("清洗后为空应生成 16 hex，got %q", rid2)
	}
}

// TestNestedReusesOuterRID：嵌套包装（api 全局面 ⊃ admin 子树）——内层复用外层
// rid（经 context 传递）、不重复记访问日志。
func TestNestedReusesOuterRID(t *testing.T) {
	lg := &capLogger{}
	var innerCtxRID, innerHeaderRID string
	inner := RequestID(lg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		innerCtxRID = RIDFromContext(r.Context())
		innerHeaderRID = r.Header.Get("X-Request-Id") // 请求头不回注——保持原始形态
	}))
	outer := RequestID(lg, inner)
	req := httptest.NewRequest(http.MethodGet, "/nested", nil)
	rec := httptest.NewRecorder()
	outer.ServeHTTP(rec, req)

	if !wantHex16.MatchString(rec.Header().Get("X-Request-Id")) {
		t.Fatalf("响应头应为 16 hex: %q", rec.Header().Get("X-Request-Id"))
	}
	if len(lg.lines) != 1 {
		t.Fatalf("嵌套应只记一条访问日志: %v", lg.lines)
	}
	m := wantRid.FindStringSubmatch(lg.lines[0])
	if m == nil {
		t.Fatalf("日志行应含 rid=<hex>: %q", lg.lines[0])
	}
	if innerCtxRID != m[1] {
		t.Fatalf("内层 context rid 应与外层一致: %q vs %q", innerCtxRID, m[1])
	}
	if innerHeaderRID != "" {
		t.Fatalf("内层不得篡改请求头（只经 context 传递）: %q", innerHeaderRID)
	}
}

// TestFlushAndHijackForwarded：包装器转发 Flusher 与 Hijacker（审计导出流式
// flush / WS 升级不被中间件破坏；Hijack 到达底层 writer 即为转发成功）。
func TestFlushAndHijackForwarded(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newStatusWriter(rec)
	if _, ok := any(sw).(http.Flusher); !ok {
		t.Fatal("包装器应实现 http.Flusher")
	}
	sw.Flush()

	hijackable := &hijackRecorder{ResponseRecorder: rec}
	sw2 := newStatusWriter(hijackable)
	conn, rw, err := sw2.Hijack()
	if err != nil || conn == nil || rw == nil {
		t.Fatalf("Hijack 应转发到底层 writer: conn=%v rw=%v err=%v", conn, rw, err)
	}
	if sw2.status != http.StatusSwitchingProtocols {
		t.Fatalf("Hijack 后状态应记 101: %d", sw2.status)
	}
}

// hijackRecorder 为可 Hijack 的测试 ResponseWriter（net.Pipe 提供真实 conn）。
type hijackRecorder struct {
	*httptest.ResponseRecorder
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c1, c2 := net.Pipe()
	go func() { _ = c2.Close() }()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}
