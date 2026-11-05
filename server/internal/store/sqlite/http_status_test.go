// outbox.http_status 回归测试（投递记录 http_status 独立字段车道，2026-11-05）：
//   - 新库：outbox 含 http_status INTEGER 列（迁移 v4 登记）；
//   - 老库（v3 无列）：Open 时 ALTER TABLE 补列，既有行保留且 http_status 为 NULL；
//   - 读写回路：UpdateOutbox 落响应码，DueOutbox/OutboxByStatus 读回；传输错误
//     （无 HTTP 响应）→ NULL（区分「无响应」与「0」）。
package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func outboxHasHTTPStatusColumn(t *testing.T, db *DB) bool {
	t.Helper()
	rows, err := db.QueryDump(context.Background(),
		`SELECT COUNT(*) FROM pragma_table_info('outbox') WHERE name='http_status'`)
	if err != nil || len(rows) != 1 {
		t.Fatalf("探测 outbox.http_status 失败: rows=%v err=%v", rows, err)
	}
	return rows[0][0] == "1"
}

// TestOutboxHTTPStatusMigrationNewDB：新库建表即含 http_status 列，迁移登记 v4。
func TestOutboxHTTPStatusMigrationNewDB(t *testing.T) {
	db := openTestDB(t)
	if !outboxHasHTTPStatusColumn(t, db) {
		t.Fatal("新库 outbox 应含 http_status 列")
	}
	rows, err := db.QueryDump(context.Background(), `SELECT MAX(version) FROM schema_migrations`)
	if err != nil || len(rows) != 1 || rows[0][0] != "4" {
		t.Fatalf("迁移版本应为 4: rows=%v err=%v", rows, err)
	}
}

// TestOutboxHTTPStatusMigrationOldDB：v3 老库（无 http_status 列）打开时自动
// ALTER 补列，既有行数据保留且 http_status 为 NULL。
func TestOutboxHTTPStatusMigrationOldDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wt-old.db")
	{
		sdb, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sdb.Close() }()
		// v3 形态的 outbox（无 http_status）+ 迁移版本 3。
		const oldSchema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS outbox (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  command_id    TEXT NOT NULL DEFAULT '',
  kind          TEXT NOT NULL,
  payload       TEXT NOT NULL,
  attempts      INTEGER NOT NULL DEFAULT 0,
  next_attempt  INTEGER NOT NULL DEFAULT 0,
  status        TEXT NOT NULL DEFAULT 'pending',
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);`
		if _, err := sdb.Exec(oldSchema); err != nil {
			t.Fatal(err)
		}
		if _, err := sdb.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (3, 0)`); err != nil {
			t.Fatal(err)
		}
		if _, err := sdb.Exec(`INSERT INTO outbox(command_id, kind, payload, created_at, updated_at)
VALUES ('cmd-legacy-1','exec_result','{}',1,1)`); err != nil {
			t.Fatal(err)
		}
		if err := sdb.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("老库升级打开失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	if !outboxHasHTTPStatusColumn(t, db) {
		t.Fatal("老库打开后 outbox 应已 ALTER 补 http_status 列")
	}
	rows, err := db.QueryDump(context.Background(), `SELECT MAX(version) FROM schema_migrations`)
	if err != nil || len(rows) != 1 || rows[0][0] != "4" {
		t.Fatalf("老库迁移版本应为 4: rows=%v err=%v", rows, err)
	}
	legacy, err := db.OutboxByStatus(context.Background(), "pending")
	if err != nil || len(legacy) != 1 || legacy[0].CommandID != "cmd-legacy-1" {
		t.Fatalf("既有行应保留: %v err=%v", legacy, err)
	}
	if legacy[0].HTTPStatus.Valid {
		t.Fatalf("既有行 http_status 应为 NULL: %+v", legacy[0].HTTPStatus)
	}
}

// TestOutboxHTTPStatusRoundTrip：UpdateOutbox 写响应码 → 读回；传输错误（无
// HTTP 响应）→ NULL（区分「无响应」与「0」）。
func TestOutboxHTTPStatusRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnqueueOutbox(ctx, tx, "exec_result", "cmd-hs-1", map[string]any{"commandId": "cmd-hs-1"}, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	items, err := db.DueOutbox(ctx, now, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("DueOutbox: %v items=%d", err, len(items))
	}
	if items[0].HTTPStatus.Valid {
		t.Fatalf("新入队行 http_status 应为 NULL: %+v", items[0].HTTPStatus)
	}

	// 响应码写入（delivered）。
	it := items[0]
	it.Status = "delivered"
	it.HTTPStatus = sql.NullInt64{Int64: 201, Valid: true}
	if err := db.UpdateOutbox(ctx, it); err != nil {
		t.Fatal(err)
	}
	got, err := db.OutboxByStatus(ctx, "delivered")
	if err != nil || len(got) != 1 {
		t.Fatalf("OutboxByStatus: %v n=%d", err, len(got))
	}
	if !got[0].HTTPStatus.Valid || got[0].HTTPStatus.Int64 != 201 {
		t.Fatalf("http_status 应为 201: %+v", got[0].HTTPStatus)
	}

	// 传输错误（无 HTTP 响应）→ NULL（不落 0）。
	got[0].Status = "pending"
	got[0].HTTPStatus = sql.NullInt64{}
	if err := db.UpdateOutbox(ctx, got[0]); err != nil {
		t.Fatal(err)
	}
	items, err = db.DueOutbox(ctx, now.Add(time.Second), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("DueOutbox 重取: %v n=%d", err, len(items))
	}
	if items[0].HTTPStatus.Valid {
		t.Fatalf("传输错误后 http_status 应为 NULL: %+v", items[0].HTTPStatus)
	}
}
