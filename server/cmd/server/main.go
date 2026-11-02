// Command server 启动 worktool-open-server（Go 单二进制；docs/02-ARCHITECTURE.md §4；
// 任务卡 T1.3 骨架 → T2.3 服务端核心接线：SQLite 持久化 + WS 网关（v2/v1）+
// 统一命令管线 + 回调 outbox）。
//
// 用法：
//
//	server [-listen host:port] [-tls-cert FILE] [-tls-key FILE]
//	       [-db worktool.db] [-callback-url URL] [-callback-secret S]
//
// 环境变量（flag 优先于环境变量，环境变量优先于默认值）：
//
//	WT_LISTEN     监听地址，默认 127.0.0.1:8080
//	WT_TLS_CERT   TLS 证书 PEM 路径（与 WT_TLS_KEY 同设即启用 TLS）
//	WT_TLS_KEY    TLS 私钥 PEM 路径
//	WT_DB         SQLite 库文件，默认 worktool.db
//	WT_CALLBACK_URL / WT_CALLBACK_SECRET / WT_ALLOW_NONLOOPBACK_CALLBACK
//	              回调分发（默认禁外网，仅 loopback 接收器）
//
// SR-09：绑定地址非 loopback 且未启用 TLS 时，启动即拒绝退出。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/config"
	"cn.worktool.open/server/internal/gateway"
	"cn.worktool.open/server/internal/scheduler"
	"cn.worktool.open/server/internal/statemachine"
	"cn.worktool.open/server/internal/store/sqlite"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Environ(), os.Stdout, os.Stderr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "worktool-open-server:", err)
		os.Exit(1)
	}
}

// run 装载配置（含 SR-09 校验，失败即在监听前返回错误）、构建路由并服务。
// ready 在监听成功后、开始 Serve 前回调（传入实际监听地址，供测试与就绪通知使用）。
func run(ctx context.Context, argv []string, environ []string, stdout, stderr io.Writer, ready func(net.Addr)) error {
	cfg, err := config.Load(argv, environ)
	if err != nil {
		return err // SR-09 违规等配置错误：启动即拒绝
	}
	logger := log.New(stderr, "", log.LstdFlags|log.Lmicroseconds)

	// 持久化 + 崩溃恢复（重启后 dispatched/acked/running 无证据 → execution_unknown）
	db, err := sqlite.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", cfg.DBPath, err)
	}
	defer func() { _ = db.Close() }()

	machine := statemachine.Default()
	pipeline := scheduler.New(db, machine)
	unknowns, err := pipeline.RecoverInflight(ctx)
	if err != nil {
		return fmt.Errorf("recover inflight: %w", err)
	}
	if len(unknowns) > 0 {
		logger.Printf("boot: 崩溃恢复登记 %d 条 execution_unknown 对账任务（E06/E07/E08）", len(unknowns))
	}

	hub := gateway.New(db, pipeline, machine, logger)
	srv := api.NewServer(db, hub, pipeline, machine, logger)

	// 回调 outbox（默认禁外网；URL 非 loopback 且未放行 → 启动即拒绝）
	worker, err := api.NewOutboxWorker(db, api.OutboxConfig{
		CallbackURL:      cfg.CallbackURL,
		CallbackSecret:   cfg.CallbackSecret,
		AllowNonLoopback: cfg.AllowNonLoopbackCallback,
	}, logger)
	if err != nil {
		return err
	}
	srv.Outbox = worker

	// 后台任务：ACK TTL 扫描（E17/E18）+ 调度泵 + 回调投递 + cron 定时调度（FR-38）
	bgCtx, bgStop := context.WithCancel(ctx)
	defer bgStop()
	go func() { _ = pipeline.RunAckTTLScanner(bgCtx, 10*time.Second) }()
	go func() { _ = hub.RunDispatchPump(bgCtx, time.Second) }()
	go func() { _ = worker.Run(bgCtx) }()
	go func() { _ = pipeline.RunCronScheduler(bgCtx, 15*time.Second) }()

	httpSrv := &http.Server{
		Handler:           api.NewServeMux(api.Options{Server: srv}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	if ready != nil {
		ready(ln.Addr())
	}
	fmt.Fprintf(stdout, "worktool-open-server listening on %s (tls=%v, db=%s, callback=%s)\n",
		ln.Addr(), cfg.TLSEnabled(), cfg.DBPath, callbackState(cfg.CallbackURL))

	go func() {
		<-ctx.Done()
		bgStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "shutdown: %v\n", err)
		}
	}()

	var serveErr error
	if cfg.TLSEnabled() {
		serveErr = httpSrv.ServeTLS(ln, cfg.TLSCertFile, cfg.TLSKeyFile)
	} else {
		serveErr = httpSrv.Serve(ln)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func callbackState(url string) string {
	if url == "" {
		return "disabled（默认禁外网）"
	}
	return url
}
