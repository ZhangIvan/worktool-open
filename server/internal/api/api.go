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

	"cn.worktool.open/server/internal/api/compat"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/httpx"
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

	// pairGuard 为 POST /api/v2/pair 的 per-IP 防爆破闸（H-1，安全审计 TOP5：
	// 5 次/分钟/IP + 连续 20 次失败封 15 分钟；NewServer 装配，勿手工置 nil）。
	pairGuard *pairGuard

	// apifox 兼容面（FR-34，T3.2）：配置与按 robot 限流器（默认 60QPM）。
	openapiCfg     OpenAPIConfig
	openapiLimiter *openapiLimiter

	// AllowNonLoopbackCallbacks 放行外网回调接收器（默认 false：仅 loopback，
	// SR-09 同 outbox 纪律；部署侧须 TLS，SR-01）。影响回调绑定的地址校验。
	AllowNonLoopbackCallbacks bool
	// FileDir 上传文件存储目录（POST /fileUpload/upload；空 = ./wt-files，
	// REV-SRV-02 b2 补齐）。测试注入临时目录。
	FileDir string
	// callbackProbe 绑定校验探测客户端（懒建；测试可注入）。
	callbackProbe *http.Client
}

// NewServer 装配 API 服务。
func NewServer(db *sqlite.DB, hub *gateway.Hub, pipeline *scheduler.Service, m *statemachine.Machine, logger Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	cfg := OpenAPIConfig{}.withDefaults()
	s := &Server{
		DB: db, Hub: hub, Pipeline: pipeline, Machine: m, Now: time.Now, Log: logger,
		openapiCfg: cfg, openapiLimiter: newOpenAPILimiter(cfg),
		pairGuard: newPairGuard(), // H-1：pair 面 per-IP 防爆破闸（参数见 pair.go PairRate* / PairFail*）
	}
	// W-M6 事件旁路接线（api/events.go: 网关车道旁路调用点）：设备 socketType=2
	// 事件帧 → HandleEventFrameItems（102~105 事件 + 511/512/513/515 查询推送
	// → 回调 outbox；101/结果类分发器自过滤不消费）。hub 可空（单测骨架装配）。
	if hub != nil {
		hub.EventBypass = s.HandleEventFrameItems
	}
	return s
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
	// EnableV1Compat 挂载 v1 兼容 WS 面 /webserver/wework/{robotId}（H-2，安全
	// 审计 TOP5：robotId 即身份的弱鉴权面**默认不挂载**——config.EnableV1Compat
	// / flag -enable-v1-compat 显式 opt-in 才暴露，迁移期部署用）。
	EnableV1Compat bool
}

// NewServeMux 构建服务端 HTTP 路由。
// 不传 Options 时仅挂 /healthz（T1.3 骨架行为，向后兼容）；装配 Server 后挂载
// T2.3 全部端点。Go 1.22 ServeMux 路由模式：方法+路径参数。
func NewServeMux(opts ...Options) *http.ServeMux {
	var s *Server
	enableV1Compat := false
	for _, o := range opts {
		if o.Server != nil {
			s = o.Server
		}
		if o.EnableV1Compat {
			enableV1Compat = true
		}
	}
	mux := http.NewServeMux()
	if s == nil {
		mux.HandleFunc("/healthz", handleHealthz) // T1.3 骨架模式（无 Server 不包中间件）
		return mux
	}
	// requestId 中间件（requestId 车道，2026-11-05）：api 面在 NewServeMux
	// 装配处统一包出——无 X-Request-Id 则生成 16 hex，响应头回显，访问日志行
	// 携带 rid。逐路由包装（返回类型保持 *ServeMux——main.go 在其上继续挂 admin
	// 子树，其内层包装复用本层 rid、不重复记日志，httpx 嵌套安全）。未匹配
	// 路径（404）无路由即无包装、无 rid——如实记录。Flusher/Hijacker 经包装器
	// 转发（WS 升级与流式响应不受影响）。
	reg := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, httpx.RequestID(s.logger(), h))
	}
	reg("/healthz", handleHealthz)
	reg("/readyz", s.handleReadyz)
	reg("POST /api/v2/pair", s.HandlePair)
	reg("/api/v2/device-sessions", s.Hub.HandleCreateSession) // POST（方法守卫在 handler）
	reg("/api/v2/device-sessions/{sessionId}", s.Hub.HandleDeviceSessionWS)
	// H-2（安全审计 TOP5）：v1 兼容面默认不挂载（Options.EnableV1Compat 显式开启）。
	if enableV1Compat {
		reg("/webserver/wework/{robotId}", s.Hub.HandleV1)
	}
	// apifox 兼容面（FR-34，ADR-08；方法守卫在 handler）
	reg("/openapi/v1/robots/{robotId}/commands", s.HandleOpenAPICommand)
	reg("/openapi/v1/robots/{robotId}/commands/{commandId}", s.HandleOpenAPIResult)
	// 兼容面扩展组（apifox 全量面：配置/回调/历史/群/队列；openapi_rest.go）
	// + b2 补齐组（文件上传/取回、robotInfo/update；openapi_upload.go，REV-SRV-02）
	if err := s.ensureOpenAPISchema(context.Background()); err != nil {
		// 建表失败不静默：装配即拒绝（fail-fast），避免半可用面
		panic(err)
	}
	if err := s.ensureOpenAPIUploadSchema(context.Background()); err != nil {
		panic(err)
	}
	reg("/fileUpload/upload", s.HandleFileUpload)
	reg("/robot/clientLog/upload", s.HandleClientLogUpload) // 510 契约端点（W-M6 W2）
	reg("/robot/robotInfo/update", s.HandleRobotInfoUpdate)
	reg("GET /files/{fileId}", s.HandleFileGet)
	reg("/openapi/v1/robots/{robotId}/info", s.HandleOpenAPIRobotInfo)
	reg("/openapi/v1/robots/{robotId}/online", s.HandleOpenAPIRobotOnline)
	reg("/openapi/v1/robots/{robotId}/logs", s.HandleOpenAPIRobotLogs)
	reg("/openapi/v1/robots/{robotId}/corps", s.HandleOpenAPIRobotCorps)
	reg("/openapi/v1/robots/{robotId}/callbacks", s.HandleOpenAPICallbacks)
	reg("/openapi/v1/robots/{robotId}/groups", s.HandleOpenAPIGroups)
	reg("/openapi/v1/robots/{robotId}/queue", s.HandleOpenAPIQueue)
	reg("/openapi/v1/robots/{robotId}/queue/priority", s.HandleOpenAPIQueuePriority)
	reg("/openapi/v1/robots/{robotId}/queue/{commandId}", s.HandleOpenAPIQueueItem)
	reg("/openapi/v1/robots/{robotId}/history/commands", s.HandleOpenAPIHistoryCommands)
	reg("/openapi/v1/robots/{robotId}/history/results", s.HandleOpenAPIHistoryResults)
	reg("/openapi/v1/robots/{robotId}/history/callbacks", s.HandleOpenAPIHistoryCallbacks)
	reg("/openapi/v1/robots/{robotId}/history/messages", s.HandleOpenAPIHistoryMessages)
	// T3.6 apifox 兼容适配层（docs/15）：K1/K2/K6 提交面挂载 + K3/K5 回调面
	// 能力装配（双面并存——既有 /openapi/v1 面零改动）。compat.Mount 在其
	// 挂载处自带 requestId 包装（deps.Log 注入），与全局面一致。
	compat.Mount(mux, s.compatDeps())
	s.wireOutboxCompat()
	return mux
}

// logger 返回注入日志器（直构 Server 未设 Log 时回落标准 log——requestId
// 中间件兜底，nil 接口调用会 panic）。
func (s *Server) logger() Logger {
	if s.Log == nil {
		return log.Default()
	}
	return s.Log
}

// compatDeps 为兼容适配层（T3.6）构造最小依赖桥：鉴权/目标解析/per-robot 限流
// 复用既有私有实现（compat 子包不反向依赖本包——避免 import cycle；错误经
// compat.Err 映射，码族与 openapiHTTPCode 同源）。
func (s *Server) compatDeps() compat.Deps {
	return compat.Deps{
		DB: s.DB, Pipeline: s.Pipeline, Now: s.Now, Log: s.Log,
		RateLimitPerMinute: s.openapiCfg.RateLimitPerMinute,
		RateWindow:         s.openapiCfg.RateWindow,
		Authn: func(ctx context.Context, r *http.Request, robotID string) (string, *compat.Err) {
			t, ae := s.authenticate(ctx, r, robotID)
			if ae != nil {
				return "", &compat.Err{Status: ae.Status, Code: openapiHTTPCode(ae), Message: ae.Message}
			}
			return t.Tenant, nil
		},
		ResolveTarget: func(ctx context.Context, robotID string) (string, *compat.Err) {
			target, ae := s.resolveTarget(ctx, robotID)
			if ae != nil {
				return "", &compat.Err{Status: ae.Status, Code: openapiHTTPCode(ae), Message: ae.Message}
			}
			return target, nil
		},
		RobotRateAllow: func(robotID string, now time.Time) bool {
			return s.openapiLimiter.allow(robotID, now) // 闭包动态取用（SetOpenAPIConfig 重建限流器后仍生效）
		},
		OpenAPIIntent: OpenAPIIntent,
	}
}

// wireOutboxCompat 装配 K5 代回复管线引用（worker 已挂时注入；装配序约定 =
// main 先 srv.Outbox = worker 再 NewServeMux——cmd/server/main.go 现序满足，
// 测试直接构造 worker 时可显式 SetReplyPipeline）。
func (s *Server) wireOutboxCompat() {
	if s.Outbox != nil {
		s.Outbox.SetReplyPipeline(s.Pipeline)
	}
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

// handleReadyz 就绪探针（W-ops 增强）：在 store ping 之上增加三个后台循环的
// 活性探针——派发泵（Hub）、回调 worker（Server.Outbox）、cron 循环（Pipeline）。
// 判定口径：循环已启动（periodMs>0）且最近一次唤醒距今 ≤ 3×周期；超窗或从未
// 启动 = not ready。状态区分：liveness（进程内循环是否活着）与 readiness
// （DB + 活性全部通过，可接流量）。HTTP 200=ready / 503=not ready。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]any{}
	dbErr := ""
	if err := s.DB.Ping(ctx); err != nil {
		dbErr = err.Error()
	}
	checks["db"] = map[string]any{"ok": dbErr == "", "detail": dbErr}
	ready := dbErr == ""
	live := true
	nowMs := time.Now().UnixMilli() // 活性以墙钟评估（循环登记亦用墙钟；不取注入时钟）
	if s.Hub != nil {
		lastTick, period := s.Hub.DispatchPumpLiveness()
		st := loopProbe(nowMs, lastTick, period)
		checks["dispatch_pump"] = st
		live = live && st["ok"] == true
	}
	if s.Outbox != nil {
		lastTick, period := s.Outbox.DeliverLiveness()
		st := loopProbe(nowMs, lastTick, period)
		checks["callback_worker"] = st
		live = live && st["ok"] == true
	}
	if s.Pipeline != nil {
		lastTick, period := s.Pipeline.CronLoopLiveness()
		st := loopProbe(nowMs, lastTick, period)
		checks["cron_loop"] = st
		live = live && st["ok"] == true
	}
	ready = ready && live
	liveness := "alive"
	if !live {
		liveness = "dead"
	}
	readiness := "ready"
	if !ready {
		readiness = "not_ready"
	}
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{
		"status": readiness, "liveness": liveness, "readiness": readiness, "checks": checks,
	})
}

// loopProbe 把单循环活性快照（lastTickMs, periodMs）折算为探针结论（nowMs 注入
// ——纯函数可表测）：
//   - periodMs=0（循环未启动）或 lastTickMs=0（已配置未唤醒）→ not_started；
//   - 距最近唤醒 > 3×周期 → stalled；
//   - 其余 → alive。
func loopProbe(nowMs, lastTickMs, periodMs int64) map[string]any {
	const stallFactor = 3
	state := "alive"
	ok := true
	if periodMs <= 0 || lastTickMs <= 0 {
		state, ok = "not_started", false
	} else if age := nowMs - lastTickMs; age > stallFactor*periodMs {
		state, ok = "stalled", false
	}
	return map[string]any{
		"ok": ok, "state": state, "lastTickMs": lastTickMs, "periodMs": periodMs,
		"maxAgeMs": stallFactor * periodMs,
	}
}
