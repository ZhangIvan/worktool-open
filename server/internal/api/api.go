// Package api 提供服务端 REST 兼容层与回调分发（docs/02-ARCHITECTURE.md §4）。
//
// T2.3 落地：POST /api/v2/pair（配对）、/api/v2/device-sessions（会话创建，
// 挂 gateway）、WS 路由挂载（gateway）、回调 outbox worker（HMAC 签名 + 有界
// 重试 1/2/4s + 第 4 次死信 + 默认禁外网仅 loopback 接收器）。
// REST v1 / apifox 兼容原生支持随 T3.2（FR-34）。
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

// apiError 为 REST 错误（状态码 + 机读码 + 人读信息）。
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) write(w http.ResponseWriter) { writeErr(w, e.Status, e.Code, e.Message) }

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Logger 为最小日志接口（与 gateway.Logger 同型；标准 log 与测试桩皆可注入）。
type Logger interface {
	Printf(format string, args ...any)
}

// Server 为 REST/回调分发服务（装配 hub/pipeline/store）。
type Server struct {
	DB       *sqlite.DB
	Hub      *gateway.Hub
	Pipeline *scheduler.Service
	Machine  *statemachine.Machine
	Now      func() time.Time
	Log      Logger

	Outbox *OutboxWorker

	// apifox 兼容面（FR-34，T3.2）：配置与按 robot 限流器（默认 60QPM）。
	openapiCfg     OpenAPIConfig
	openapiLimiter *openapiLimiter
}

// NewServer 装配 API 服务。
func NewServer(db *sqlite.DB, hub *gateway.Hub, pipeline *scheduler.Service, m *statemachine.Machine, logger Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	cfg := OpenAPIConfig{}.withDefaults()
	return &Server{
		DB: db, Hub: hub, Pipeline: pipeline, Machine: m, Now: time.Now, Log: logger,
		openapiCfg: cfg, openapiLimiter: newOpenAPILimiter(cfg),
	}
}

// SetOpenAPIConfig 调整兼容面限流配置（默认 60QPM；部署/测试注入）。
func (s *Server) SetOpenAPIConfig(cfg OpenAPIConfig) {
	cfg = cfg.withDefaults()
	s.openapiCfg = cfg
	s.openapiLimiter = newOpenAPILimiter(cfg)
}

// Options 为 NewServeMux 的可选依赖（零值 = 仅 /healthz 骨架模式，T1.3 兼容）。
type Options struct {
	Server *Server
}

// NewServeMux 构建服务端 HTTP 路由。
// 不传 Options 时仅挂 /healthz（T1.3 骨架行为，向后兼容）；装配 Server 后挂载
// T2.3 全部端点。Go 1.22 ServeMux 路由模式：方法+路径参数。
func NewServeMux(opts ...Options) *http.ServeMux {
	var s *Server
	for _, o := range opts {
		if o.Server != nil {
			s = o.Server
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	if s == nil {
		return mux
	}
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("POST /api/v2/pair", s.HandlePair)
	mux.HandleFunc("/api/v2/device-sessions", s.Hub.HandleCreateSession) // POST（方法守卫在 handler）
	mux.HandleFunc("/api/v2/device-sessions/{sessionId}", s.Hub.HandleDeviceSessionWS)
	mux.HandleFunc("/webserver/wework/{robotId}", s.Hub.HandleV1)
	// apifox 兼容面（FR-34，ADR-08；方法守卫在 handler）
	mux.HandleFunc("/openapi/v1/robots/{robotId}/commands", s.HandleOpenAPICommand)
	mux.HandleFunc("/openapi/v1/robots/{robotId}/commands/{commandId}", s.HandleOpenAPIResult)
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleReadyz 就绪探针：store 可 ping 即就绪（会话态随首条 WS 请求体现）。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.Ping(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "NOT_READY", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
