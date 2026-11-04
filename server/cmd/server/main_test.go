package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
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

// TestRunIssueApproverToken（P0-3 审批身份车道）：-issue-approver-token 签发
// 决议面令牌（打印 APPROVER_TOKEN= 后退出，不监听不起服），且库内仅存 hash。
func TestRunIssueApproverToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wt.db")
	stdout := &strings.Builder{}
	if err := run(context.Background(), []string{"-issue-approver-token", "-db", dbPath}, nil, stdout, io.Discard, nil); err != nil {
		t.Fatalf("签发决议令牌应干净退出，实际: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "APPROVER_TOKEN=") {
		t.Fatalf("stdout 应含 APPROVER_TOKEN= 行，实际: %q", out)
	}
	rest, _, _ := strings.Cut(strings.SplitN(out, "APPROVER_TOKEN=", 2)[1], "\n")
	raw := strings.TrimSpace(rest)
	if len(raw) != 64 { // 256-bit hex
		t.Fatalf("决议令牌应为 64 hex，got %d 字符", len(raw))
	}
	// 库内仅存 hash：api_tokens.robot_id='approver' 恰一行，原始值不落库。
	// （run 已关闭 DB；此处重开只读校验。）
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryDump(context.Background(),
		`SELECT robot_id, status FROM api_tokens WHERE robot_id='approver'`)
	if err != nil || len(rows) != 1 || rows[0][0] != "approver" || rows[0][1] != "active" {
		t.Fatalf("approver scope 令牌应恰一行 active：rows=%v err=%v", rows, err)
	}
}

// TestRunServesHealthz（SR-09 正例 + 可启动验证）：
// loopback 明文（本地原型例外）正常启动，/healthz 返回 200，
// 优雅关停后 run 干净返回。T2.3 起附带 /readyz（store 就绪探针）。
func TestRunServesHealthz(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "wt.db")
	readyCh := make(chan net.Addr, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, []string{"-listen", "127.0.0.1:0"}, []string{"WT_DB=" + dbPath}, io.Discard, io.Discard, func(a net.Addr) {
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

	readyResp, err := client.Get("http://" + addr.String() + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz 失败: %v", err)
	}
	readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /readyz 状态码 = %d, want 200", readyResp.StatusCode)
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
