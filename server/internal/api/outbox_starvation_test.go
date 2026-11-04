/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

// TestOutboxNoReceiverBacklogDoesNotStarve（第三方审计二-6 回归）：无接收器
// 存量行不得饿死后续可投递行——W-M6 回调闭环修复的守恒测试：
//  1. 系统级零接收器 → 行保持 pending（等待未来配置，保留行为，非饿死语义）；
//  2. 存在接收器后：未绑域存量行（> claim LIMIT 32）终态 skipped，同窗口尾部的
//     新 query_result 行在后续轮次正常投递（修复前存量永不减员、新行永排不进）；
//  3. 后补接收器不复活 skipped 终态行（如需补推走业务重放而非 worker）。
func TestOutboxNoReceiverBacklogDoesNotStarve(t *testing.T) {
	db, err := sqlite.OpenMem()
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now().UTC()

	var hits int
	var lastBody map[string]any
	rcv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		w.WriteHeader(200)
	}))
	t.Cleanup(rcv.Close)

	worker, err := NewOutboxWorker(db, OutboxConfig{CallbackSecret: "s"}, nil)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}

	// ① 系统级零接收器：行保持 pending（等待配置；非饿死语义）
	if err := db.EnqueueOutbox(ctx, nil, "exec_result", "cold-0", map[string]any{"type": 203}, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("seed cold: %v", err)
	}
	if err := worker.DeliverDue(ctx); err != nil {
		t.Fatalf("deliver-cold: %v", err)
	}
	if rows, _ := db.QueryDump(ctx, `SELECT COUNT(*) FROM outbox WHERE status='pending'`); rows[0][0] != "1" {
		t.Fatalf("零接收器应保持 pending：rows=%v", rows)
	}

	// ② 绑定 robot-x type=11（系统自此存在接收器）+ 指令/设备行（路由反查链）
	if err := seedRobotBinding(ctx, db, "robot-x", 11, rcv.URL+"/cb"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	seedCommandWithTarget(ctx, t, db, "cmd-new-1", "device-x", "robot-x")
	// 存量：exec_result 域（type=1）未绑 ×40（> claim LIMIT 32）
	for i := 0; i < 40; i++ {
		if err := db.EnqueueOutbox(ctx, nil, "exec_result", "legacy-cmd-"+string(rune('a'+i%26)), map[string]any{"type": 203}, now.Add(-time.Hour)); err != nil {
			t.Fatalf("seed legacy: %v", err)
		}
	}
	// 尾部新行：query_result 域已绑 → 可投递（修复前永远排不进任何一批）
	if err := db.EnqueueOutbox(ctx, nil, "query_result", "cmd-new-1", map[string]any{"type": 513, "data": map[string]any{"ok": true}}, now); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	// 多轮投递（claim LIMIT 32/批，对齐真实 Run 循环语义）
	for i := 0; i < 3; i++ {
		if err := worker.DeliverDue(ctx); err != nil {
			t.Fatalf("deliver round %d: %v", i, err)
		}
	}

	// 断言：41 存量（40 legacy + cold-0）skipped、新行 delivered、恰投递 1 次
	var skipped, pending, delivered int
	rows, err := db.QueryDump(ctx, `SELECT status, COUNT(*) FROM outbox GROUP BY status`)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, r := range rows {
		n := 0
		_, _ = fmtSscan(r[1], &n)
		switch r[0] {
		case "skipped":
			skipped = n
		case "pending":
			pending = n
		case "delivered":
			delivered = n
		}
	}
	if skipped != 41 || pending != 0 || delivered != 1 || hits != 1 {
		t.Fatalf("存量应全部 skipped 且新行已投：skipped=%d pending=%d delivered=%d hits=%d", skipped, pending, delivered, hits)
	}
	if lastBody["type"].(float64) != 513 {
		t.Fatalf("投递体串扰：got %v", lastBody)
	}

	// ③ 后补接收器不复活 skipped（终态语义）
	if err := seedRobotBinding(ctx, db, "robot-x", 1, rcv.URL+"/exec"); err != nil {
		t.Fatalf("bind2: %v", err)
	}
	if err := worker.DeliverDue(ctx); err != nil {
		t.Fatalf("deliver-final: %v", err)
	}
	if hits != 1 {
		t.Fatalf("skipped 终态不应复活重投：hits=%d", hits)
	}
}

// fmtSscan 测试内小工具。
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return 1, nil
}

// seedRobotBinding 直接落 openapi_callbacks（测试通道；表由 ensureOpenAPISchema
// 建于 mux 装配，独立 DB 手测需自带 DDL——与 admin/console.go 同源）。
func seedRobotBinding(ctx context.Context, db *sqlite.DB, robotID string, typ int, url string) error {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS openapi_callbacks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  robot_id TEXT NOT NULL,
  type INTEGER NOT NULL,
  callback_url TEXT NOT NULL,
  created_ms INTEGER NOT NULL,
  updated_ms INTEGER NOT NULL,
  UNIQUE(robot_id, type))`); err != nil {
		return err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO openapi_callbacks(robot_id, type, callback_url, created_ms, updated_ms) VALUES (?,?,?,?,?)`,
		robotID, typ, url, 0, 0); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// seedCommandWithTarget 落指令行（query_result 路由=commandID→target→robot 反查链）。
func seedCommandWithTarget(ctx context.Context, t *testing.T, db *sqlite.DB, commandID, deviceID, robotID string) {
	t.Helper()
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatalf("tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO devices(device_id, robot_id, device_name, token_hash, token_version, status, epoch, paired_at, updated_at)
		 VALUES (?,?,'t','h',1,'active',0,0,0)`, deviceID, robotID); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO commands(command_id, target, command_type, payload, state, epoch, seq, event_id, risk_class, dry_run, created_at)
		 VALUES (?,?,'513','{}','failed',0,0,'','L0',0,0)`, commandID, deviceID); err != nil {
		t.Fatalf("seed command: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
