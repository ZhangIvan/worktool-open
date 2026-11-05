package main

import (
	"context"
	"errors"
	"io"
	"log"
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

// TestRunRetentionSweeper（服务端可观测与保留清理车道）：保留清理循环——启动即
// 扫一轮（不待首个 tick）：outbox delivered>7d / dead>30d 删（dead 删除前审计
// outbox_dead_purged）、closed 会话>30d 整行删；窗口内行与 pending/open 保留；
// ctx 取消后干净退出。
func TestRunRetentionSweeper(t *testing.T) {
	db, err := sqlite.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	now := time.Now().UTC()

	// 种子：超期终态行（应删）+ 窗口内/活行（应留）。
	seed := func(commandID, status string, updatedAt time.Time) {
		tx, err := db.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := db.EnqueueOutbox(ctx, tx, "exec_result", commandID, map[string]any{"commandId": commandID}, updatedAt); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE outbox SET status=?, updated_at=? WHERE command_id=?`,
			status, updatedAt.UnixMilli(), commandID); err != nil {
			t.Fatal(err)
		}
	}
	seed("cmd-old-delivered", "delivered", now.Add(-8*24*time.Hour))
	seed("cmd-fresh-delivered", "delivered", now.Add(-1*time.Hour))
	seed("cmd-old-dead", "dead", now.Add(-31*24*time.Hour))
	seed("cmd-mid-dead", "dead", now.Add(-8*24*time.Hour))
	seed("cmd-pending", "pending", now.Add(-40*24*time.Hour))
	if err := db.CreateSession(ctx, sqlite.Session{
		SessionID: "sess-sweeper-old", DeviceID: "dev-sweeper",
		ServerX25519Private: "priv", ServerX25519Public: "pub", DeviceX25519Public: "devpub",
		CreatedAt: now.Add(-31 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CloseSession(ctx, "sess-sweeper-old", now.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, sqlite.Session{
		SessionID: "sess-sweeper-open", DeviceID: "dev-sweeper",
		ServerX25519Private: "priv", ServerX25519Public: "pub", DeviceX25519Public: "devpub",
		CreatedAt: now.Add(-40 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.OpenSession(ctx, "sess-sweeper-open", 0, 1, 0, now); err != nil {
		t.Fatal(err)
	}

	// 启动轮应立即清理（不待首个 tick）；周期取极小值顺带验证循环可重入。
	sweepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- runRetentionSweeper(sweepCtx, db, log.New(io.Discard, "", 0), 10*time.Millisecond) }()

	deadline := time.Now().Add(3 * time.Second)
	for {
		rows, err := db.QueryDump(ctx, `SELECT status, COUNT(*) FROM outbox GROUP BY status`)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]string{}
		for _, r := range rows {
			counts[r[0]] = r[1]
		}
		oldClosed, err := db.QueryDump(ctx, `SELECT COUNT(*) FROM device_sessions WHERE session_id='sess-sweeper-old'`)
		if err != nil {
			t.Fatal(err)
		}
		if counts["delivered"] == "1" && counts["dead"] == "1" && oldClosed[0][0] == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("启动轮应清理超期终态行：outbox=%v oldClosed=%v", counts, oldClosed)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 窗口内/活行保留。
	for _, q := range []string{
		`SELECT COUNT(*) FROM outbox WHERE status='delivered' AND command_id='cmd-fresh-delivered'`,
		`SELECT COUNT(*) FROM outbox WHERE status='dead' AND command_id='cmd-mid-dead'`,
		`SELECT COUNT(*) FROM outbox WHERE status='pending'`,
		`SELECT COUNT(*) FROM device_sessions WHERE session_id='sess-sweeper-open'`,
	} {
		rows, err := db.QueryDump(ctx, q)
		if err != nil || rows[0][0] != "1" {
			t.Fatalf("窗口内/活行应保留：%q → rows=%v err=%v", q, rows, err)
		}
	}
	// dead 清理审计恰一条（启动轮落；后续轮 dead=0 不重复）。
	audit, err := db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='outbox_dead_purged'`)
	if err != nil || audit[0][0] != "1" {
		t.Fatalf("dead 清理审计应恰一条：rows=%v err=%v", audit, err)
	}

	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ctx 取消后应返回 context.Canceled，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待清理循环退出超时")
	}
}
