package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRunRefusesNonLoopbackPlaintext（SR-09 反例·flag 路径）：
// 通过 flag 声明非 loopback 明文绑定时，run 必须在监听前拒绝启动。
func TestRunRefusesNonLoopbackPlaintext(t *testing.T) {
	err := run(context.Background(), []string{"-listen", "0.0.0.0:18080"}, nil, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("run 应因 SR-09（非 loopback 明文）拒绝启动，实际返回 nil")
	}
	if !strings.Contains(err.Error(), "SR-09") {
		t.Fatalf("错误信息应包含 SR-09 标识，实际: %v", err)
	}
}

// TestRunRefusesNonLoopbackPlaintextFromEnv（SR-09 反例·env 路径）。
func TestRunRefusesNonLoopbackPlaintextFromEnv(t *testing.T) {
	err := run(context.Background(), nil, []string{"WT_LISTEN=192.0.2.10:18080"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("run 应因 SR-09（env 声明非 loopback 明文）拒绝启动，实际返回 nil")
	}
}

// TestRunServesHealthz（SR-09 正例 + 可启动验证）：
// loopback 明文（本地原型例外）正常启动，/healthz 返回 200，
// 优雅关停后 run 干净返回。
func TestRunServesHealthz(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readyCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, []string{"-listen", "127.0.0.1:0"}, nil, io.Discard, io.Discard, func(a net.Addr) {
			readyCh <- a
		})
	}()

	var addr net.Addr
	select {
	case addr = <-readyCh:
	case <-time.After(5 * time.Second):
		t.Fatal("等待监听就绪超时")
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr.String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz 状态码 = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	if strings.TrimSpace(string(body)) != `{"status":"ok"}` {
		t.Fatalf("/healthz 响应体 = %q, want {\"status\":\"ok\"}", body)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("优雅关停后 run 应返回 nil，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 run 退出超时")
	}
}
