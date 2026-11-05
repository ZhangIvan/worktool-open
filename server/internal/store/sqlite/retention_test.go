// 保留清理回归测试（服务端可观测与保留清理车道，2026-11-05）：
//   - CleanupOutboxRetention：delivered 行保留 7 天、dead 行保留 30 天（按
//     updated_at 判龄）；dead 删除前同事务落一条 outbox_dead_purged 审计（批量
//     计数）；skipped 行纳入同 sweeper 清理（30 天，无审计要求）；pending 行
//     不动；幂等（再扫 0 行、无重复审计）；
//   - CleanupClosedSessions：closed 会话整行保留 30 天（closed_at 判龄，缺失按
//     created_at 兜底）；open/pending 行无论多旧都保留。
//   - 保留期经参数注入（保留期 config 化车道）：常量为默认值，调用方
//     （cmd/server）经 -retention-* 旗标传入实际窗口。
package sqlite

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func seedOutboxRow(t *testing.T, db *DB, commandID, status string, updatedAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.EnqueueOutbox(ctx, tx, "exec_result", commandID,
		map[string]any{"commandId": commandID}, updatedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryDump(ctx, `SELECT id FROM outbox WHERE command_id=? ORDER BY id DESC LIMIT 1`, commandID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("seed 取行号失败: rows=%v err=%v", rows, err)
	}
	var id int64
	if id, err = strconv.ParseInt(rows[0][0], 10, 64); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE outbox SET status=?, updated_at=? WHERE id=?`,
		status, updatedAt.UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func outboxStatus(t *testing.T, db *DB, id int64) (string, bool) {
	t.Helper()
	rows, err := db.QueryDump(context.Background(), `SELECT status FROM outbox WHERE id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return "", false
	}
	return rows[0][0], true
}

// TestCleanupOutboxRetention：窗口语义 + dead 审计 + skipped 同 sweeper 30 天
// 清理 + 幂等。
func TestCleanupOutboxRetention(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	idDeliveredOld := seedOutboxRow(t, db, "cmd-delivered-old", "delivered", now.Add(-8*24*time.Hour))     // >7d → 删
	idDeliveredFresh := seedOutboxRow(t, db, "cmd-delivered-fresh", "delivered", now.Add(-6*24*time.Hour)) // <7d → 留
	idDeadOld := seedOutboxRow(t, db, "cmd-dead-old", "dead", now.Add(-31*24*time.Hour))                   // >30d → 删
	idDeadMid := seedOutboxRow(t, db, "cmd-dead-mid", "dead", now.Add(-8*24*time.Hour))                    // 8d：delivered 窗口外、dead 窗口内 → 留
	idPending := seedOutboxRow(t, db, "cmd-pending", "pending", now.Add(-40*24*time.Hour))                 // pending → 不动
	idSkippedOld := seedOutboxRow(t, db, "cmd-skipped-old", "skipped", now.Add(-40*24*time.Hour))          // skipped >30d → 删
	idSkippedFresh := seedOutboxRow(t, db, "cmd-skipped-fresh", "skipped", now.Add(-29*24*time.Hour))      // skipped <30d → 留

	delivered, dead, skipped, err := db.CleanupOutboxRetention(ctx, now, OutboxDeliveredRetain, OutboxDeadRetain)
	if err != nil {
		t.Fatalf("CleanupOutboxRetention: %v", err)
	}
	if delivered != 1 || dead != 1 || skipped != 1 {
		t.Fatalf("应删 delivered=1 dead=1 skipped=1，got delivered=%d dead=%d skipped=%d", delivered, dead, skipped)
	}
	if _, ok := outboxStatus(t, db, idDeliveredOld); ok {
		t.Fatal("delivered 超 7 天应已删除")
	}
	if st, ok := outboxStatus(t, db, idDeliveredFresh); !ok || st != "delivered" {
		t.Fatalf("delivered 未满 7 天应保留，ok=%v st=%q", ok, st)
	}
	if _, ok := outboxStatus(t, db, idDeadOld); ok {
		t.Fatal("dead 超 30 天应已删除")
	}
	if st, ok := outboxStatus(t, db, idDeadMid); !ok || st != "dead" {
		t.Fatalf("dead 8 天（30 天窗口内）应保留——与 delivered 7 天窗口区分，ok=%v st=%q", ok, st)
	}
	if st, ok := outboxStatus(t, db, idPending); !ok || st != "pending" {
		t.Fatalf("pending 不应被清理，ok=%v st=%q", ok, st)
	}
	if _, ok := outboxStatus(t, db, idSkippedOld); ok {
		t.Fatal("skipped 超 30 天应已删除（与 delivered/dead 同 sweeper）")
	}
	if st, ok := outboxStatus(t, db, idSkippedFresh); !ok || st != "skipped" {
		t.Fatalf("skipped 未满 30 天应保留，ok=%v st=%q", ok, st)
	}

	// dead 清理前审计：恰一条 outbox_dead_purged，detail 含批量计数；skipped
	// 清理无审计要求（不计入 outbox_dead_purged）。
	audit, err := db.QueryDump(ctx, `SELECT kind, detail FROM audit_log WHERE kind='outbox_dead_purged'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 1 {
		t.Fatalf("dead 清理应落恰一条 outbox_dead_purged 审计，got %d 行", len(audit))
	}
	if !strings.Contains(audit[0][1], "count=1") {
		t.Fatalf("dead 审计应含批量计数 count=1，detail=%q", audit[0][1])
	}

	// 幂等：再扫 0 行、不追加审计。
	delivered, dead, skipped, err = db.CleanupOutboxRetention(ctx, now, OutboxDeliveredRetain, OutboxDeadRetain)
	if err != nil || delivered != 0 || dead != 0 || skipped != 0 {
		t.Fatalf("二次扫描应 0 行，got delivered=%d dead=%d skipped=%d err=%v", delivered, dead, skipped, err)
	}
	if audit, err := db.QueryDump(ctx, `SELECT COUNT(*) FROM audit_log WHERE kind='outbox_dead_purged'`); err != nil || audit[0][0] != "1" {
		t.Fatalf("无 dead 删除不应追加审计：rows=%v err=%v", audit, err)
	}
}

// TestCleanupClosedSessions：closed 超 30 天整行删除；窗口内 closed 保留；
// open/pending 无论多旧都保留；closed_at 缺失按 created_at 兜底判龄。
func TestCleanupClosedSessions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	mk := func(t *testing.T, db *DB, sessionID string, createdAt time.Time) {
		t.Helper()
		if err := db.CreateSession(ctx, Session{
			SessionID: sessionID, DeviceID: "dev-retain",
			ServerX25519Private: "priv-" + sessionID, ServerX25519Public: "pub-" + sessionID,
			DeviceX25519Public: "devpub-" + sessionID, CreatedAt: createdAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// closed 超 30 天 → 整行删除。
	mk(t, db, "sess-old-closed", old)
	if err := db.CloseSession(ctx, "sess-old-closed", old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// closed 窗口内 → 保留。
	mk(t, db, "sess-fresh-closed", fresh)
	if err := db.CloseSession(ctx, "sess-fresh-closed", fresh); err != nil {
		t.Fatal(err)
	}
	// open / pending 多旧都不动。
	mk(t, db, "sess-old-open", old)
	if err := db.OpenSession(ctx, "sess-old-open", 0, 1, 0, old); err != nil {
		t.Fatal(err)
	}
	mk(t, db, "sess-old-pending", old)
	// closed_at 缺失（历史形态）→ created_at 兜底判龄 → 删。
	mk(t, db, "sess-legacy-closed", old)
	if _, err := db.sql.Exec(`UPDATE device_sessions SET state='closed', closed_at=NULL WHERE session_id='sess-legacy-closed'`); err != nil {
		t.Fatal(err)
	}

	n, err := db.CleanupClosedSessions(ctx, now, ClosedSessionRetain)
	if err != nil {
		t.Fatalf("CleanupClosedSessions: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删 2 行（超期 closed 两形态），got %d", n)
	}
	for _, id := range []string{"sess-old-closed", "sess-legacy-closed"} {
		if _, ok, err := db.GetSession(ctx, id); err != nil || ok {
			t.Fatalf("%s 超期 closed 会话应已整行删除（ok=%v err=%v）", id, ok, err)
		}
	}
	for _, id := range []string{"sess-fresh-closed", "sess-old-open", "sess-old-pending"} {
		s, ok, err := db.GetSession(ctx, id)
		if err != nil || !ok {
			t.Fatalf("%s 应保留（ok=%v err=%v）", id, ok, err)
		}
		// closed 行保留时私钥列应为清零语义（M-4：CloseSession 即清零）；仅
		// open/pending 行保留原私钥（运行解密派生需要）。
		want := "priv-" + id
		if s.State == "closed" {
			want = ""
		}
		if s.ServerX25519Private != want {
			t.Fatalf("%s 私钥列 = %q, want %q", id, s.ServerX25519Private, want)
		}
	}

	// 幂等：再扫 0 行。
	if n, err := db.CleanupClosedSessions(ctx, now, ClosedSessionRetain); err != nil || n != 0 {
		t.Fatalf("二次扫描应 0 行，got n=%d err=%v", n, err)
	}
}
