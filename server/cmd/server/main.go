// Command server 启动 worktool-open-server（Go 单二进制；docs/02-ARCHITECTURE.md §4；
// 任务卡 T1.3 骨架 → T2.3 服务端核心接线：SQLite 持久化 + WS 网关（v2/v1）+
// 统一命令管线 + 回调 outbox）。
//
// 用法：
//
//	server [-listen host:port] [-tls-cert FILE] [-tls-key FILE]
//	       [-db worktool.db] [-callback-url URL] [-callback-secret S]
//	       [-enable-v1-compat]（v1 兼容 WS 面，默认不挂载，H-2）
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

	"cn.worktool.open/server/internal/admin"
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

	// M-4（安全审计）：启动一次性清零历史 closed 会话残留的会话私钥列
	// （open/pending 保留——运行解密派生需要）；失败拒绝启动（与 guard 策略
	// 恢复同 fail-fast 纪律，不留假成功）。
	if n, err := db.ScrubClosedSessionKeys(ctx); err != nil {
		return fmt.Errorf("boot: 清零 closed 会话残留私钥失败: %w", err)
	} else if n > 0 {
		logger.Printf("boot: 已清零 %d 条历史 closed 会话残留私钥（M-4）", n)
	}

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

	// 管理面（admin/README：装配处接线；UI 经 -admin-ui 指向仓库 admin/ 目录）。
	adminSrv := admin.New(admin.Deps{
		DB: db, Pipeline: pipeline, Log: logger,
		Online: hub.HasActiveSession, // 设备列表在线态（网关只读快照）
		UIDir: cfg.AdminUIDir,
	})
	if cfg.IssueAdminToken {
		raw, err := adminSrv.IssueAdminToken(ctx)
		if err != nil {
			return fmt.Errorf("issue admin token: %w", err)
		}
		fmt.Fprintf(stdout, "ADMIN_TOKEN=%s\n", raw)
		fmt.Fprintln(stdout, "(仅此一次显示；库中仅存 SHA-256 hash。浏览器打开 /admin/ 粘贴令牌登录)")
		return nil
	}
	// P0-3（审批身份车道）：决议面独立令牌——与管理令牌分持，admin 令牌在决议
	// 面 403 APPROVER_SCOPE_REQUIRED，结构性消灭「管理令牌自批」。
	if cfg.IssueApproverToken {
		raw, err := adminSrv.IssueApproverToken(ctx)
		if err != nil {
			return fmt.Errorf("issue approver token: %w", err)
		}
		fmt.Fprintf(stdout, "APPROVER_TOKEN=%s\n", raw)
		fmt.Fprintln(stdout, "(仅此一次显示；库中仅存 SHA-256 hash。管理台「L2 审批」页粘贴决议令牌使用；仅限决议，不得用于提交)")
		return nil
	}

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
	go func() {
		// B3（code review）：TTL 扫描器一次瞬时错误（如 SQLITE_BUSY）即 return
		// 退出 goroutine → E17/E18/E19 全部静默停摆到重启。改为在 Scanner 循环内
		// 容错（pipeline 侧继续），此处仅保留 ctx 取消退出。
		for ctx.Err() == nil {
			if err := pipeline.RunAckTTLScanner(bgCtx, 10*time.Second); err != nil && ctx.Err() == nil {
				logger.Printf("scheduler: TTL 扫描器异常退出（3s 后重启）: %v", err)
			} else {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	go func() { _ = hub.RunDispatchPump(bgCtx, time.Second) }()
	go func() { _ = worker.Run(bgCtx) }()
	go func() { _ = pipeline.RunCronScheduler(bgCtx, 15*time.Second) }()

	// H-2（安全审计 TOP5）：v1 兼容 WS 面默认不挂载，cfg.EnableV1Compat
	// （flag -enable-v1-compat）显式开启才暴露 /webserver/wework/{robotId}。
	mux := api.NewServeMux(api.Options{Server: srv, EnableV1Compat: cfg.EnableV1Compat})
	adminSrv.Register(mux)
	// P0-2（2026-11-04 外部审计实锤：ApplyPersistedSettings 无启动调用点——重启后
	// 持久化 guard 策略实际失效，查询接口却显示已保存=假成功）。先恢复并验证策略，
	// 再开始接流量；恢复失败拒绝启动（fail-fast，不保留假成功）。
	if err := adminSrv.ApplyPersistedSettings(context.Background()); err != nil {
		return fmt.Errorf("boot: 恢复持久化管理设置失败（拒绝启动，防止策略失效假成功）: %w", err)
	}
	logger.Printf("boot: 持久化管理设置已恢复并应用（guard 策略实际生效）")
	httpSrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	if ready != nil {
		ready(ln.Addr())
	}
	fmt.Fprintf(stdout, "worktool-open-server listening on %s (tls=%v, db=%s, callback=%s, v1compat=%v)\n",
		ln.Addr(), cfg.TLSEnabled(), cfg.DBPath, callbackState(cfg.CallbackURL), cfg.EnableV1Compat)

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
