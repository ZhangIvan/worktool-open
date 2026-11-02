// Command server 启动 worktool-open-server（Go 单二进制；docs/02-ARCHITECTURE.md §4；
// 任务卡 T1.3 骨架）。
//
// 用法：
//
//	server [-listen host:port] [-tls-cert FILE] [-tls-key FILE]
//
// 环境变量（flag 优先于环境变量，环境变量优先于默认值）：
//
//	WT_LISTEN     监听地址，默认 127.0.0.1:8080
//	WT_TLS_CERT   TLS 证书 PEM 路径（与 WT_TLS_KEY 同设即启用 TLS）
//	WT_TLS_KEY    TLS 私钥 PEM 路径
//
// SR-09：绑定地址非 loopback 且未启用 TLS 时，启动即拒绝退出。
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cn.worktool.open/server/internal/api"
	"cn.worktool.open/server/internal/config"
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

	srv := &http.Server{
		Handler:           api.NewServeMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	if ready != nil {
		ready(ln.Addr())
	}
	fmt.Fprintf(stdout, "worktool-open-server listening on %s (tls=%v)\n", ln.Addr(), cfg.TLSEnabled())

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintf(stderr, "shutdown: %v\n", err)
		}
	}()

	var serveErr error
	if cfg.TLSEnabled() {
		serveErr = srv.ServeTLS(ln, cfg.TLSCertFile, cfg.TLSKeyFile)
	} else {
		serveErr = srv.Serve(ln)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}
