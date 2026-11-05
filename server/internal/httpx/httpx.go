// Package httpx 提供 admin/api 两面共用的 HTTP 中间件（单一实现，防语义漂移）。
//
// RequestID（requestId 中间件车道，2026-11-05）：
//   - 请求无 X-Request-Id 头 → 生成 16 hex 随机 ID；响应头回显该 ID；
//   - 请求自带 X-Request-Id → 清洗后回显（仅保留 [A-Za-z0-9._-]，≤64 字符——
//     防日志注入；清洗后为空回落生成）；
//   - 请求完成时输出一条服务端访问日志行（method path status 耗时 rid=…）；
//   - 嵌套安全：api 全局面与 admin 子树各自包一层时，内层经 context 识别外层
//     已分配的 rid，复用且不重复记日志（生产装配 = api.NewServeMux 全局面一层 +
//     admin.Register 子树一层；独立挂 admin 时子树层兜底）；
//   - 包装器转发 Flusher/Hijacker——审计导出流式 flush 与 gateway WS 升级
//     （gorilla Hijack）不被中间件破坏。
package httpx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Logger 为最小日志接口（admin.Logger / api.Logger 同型，结构化匹配免导入）。
type Logger interface {
	Printf(format string, args ...any)
}

// maxRIDLen 为回显 rid 的最大长度（超出截断；防恶意超长头撑爆日志行）。
const maxRIDLen = 64

// ridContextKey 为 context 内 rid 传递键（嵌套包装复用外层 ID；不回注请求头）。
type ridContextKey struct{}

// discardLogger 为 nil 日志器兜底（丢弃访问日志行，其余语义不变）。
type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// RIDFromContext 返回请求生命周期内的 rid（无则空串）。
func RIDFromContext(ctx context.Context) string {
	rid, _ := ctx.Value(ridContextKey{}).(string)
	return rid
}

// RequestID 为请求追踪中间件（语义见包注释）。
func RequestID(log Logger, next http.Handler) http.Handler {
	if log == nil {
		log = discardLogger{} // 兜底（compat.Mount 的 deps.Log 直构可空）
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rid := RIDFromContext(r.Context()); rid != "" {
			// 嵌套形态（外层已分配）：复用 rid、不重复记日志。
			w.Header().Set("X-Request-Id", rid)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ridContextKey{}, rid)))
			return
		}
		rid := sanitizeRID(r.Header.Get("X-Request-Id"))
		if rid == "" {
			rid = newRID() // 加密随机 16 hex（生成失败恐慌零容忍——见 newRID）
		}
		w.Header().Set("X-Request-Id", rid)
		sw := newStatusWriter(w)
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), ridContextKey{}, rid)))
		log.Printf("http: rid=%s %s %s -> %d (%s)", rid, r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Microsecond))
	})
}

// sanitizeRID 清洗请求方 rid：仅保留 [A-Za-z0-9._-]，≤64 字符；清洗后为空返回
// 空串（调用方回落生成）。
func sanitizeRID(raw string) string {
	out := make([]rune, 0, maxRIDLen)
	for _, c := range raw {
		if len(out) >= maxRIDLen {
			break
		}
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '.' || c == '_' || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

// newRID 生成 16 hex（8 随机字节）。加密源失败时 panic——追踪 ID 缺失只降级
// 可观测性，但静默重用可预测序列会伪造追踪链路（AUD-M2-04 纪律同源：随机源
// 失败绝不兜底固定值）。
func newRID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("httpx: 随机源失败: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// statusWriter 为状态捕获 ResponseWriter（访问日志用）；转发 Flusher/Hijacker
// ——流式响应与 WS 升级不受包装影响。
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func newStatusWriter(w http.ResponseWriter) *statusWriter {
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	return w.ResponseWriter.Write(b)
}

// Flush 转发（审计导出分页流式 flush 需要）。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 转发（gateway WS 升级：gorilla websocket.Upgrader 要求
// http.Hijacker——包装层缺失该实现会令全部 WS 升级失败）。Hijack 即连接被
// 接管、状态由升级器直写 → 记 101。
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("httpx: 底层 ResponseWriter 不支持 Hijack")
	}
	w.status, w.wrote = http.StatusSwitchingProtocols, true
	return h.Hijack()
}
