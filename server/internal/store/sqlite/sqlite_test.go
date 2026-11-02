package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wt.db")
	for i := 0; i < 3; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次打开: %v", i+1, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("关闭: %v", err)
		}
	}
}

func TestPairingCodeLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	code := "482913"
	if err := db.CreatePairingCode(ctx, sha256Hex(code), now.Add(10*time.Minute)); err != nil {
		t.Fatalf("签发: %v", err)
	}
	// 无效码
	if ok, err := db.ConsumePairingCode(ctx, sha256Hex("000000"), now); err != nil || ok {
		t.Fatalf("未知码应拒绝: ok=%v err=%v", ok, err)
	}
	// 单次有效
	if ok, err := db.ConsumePairingCode(ctx, sha256Hex(code), now); err != nil || !ok {
		t.Fatalf("首次消费应成功: ok=%v err=%v", ok, err)
	}
	if ok, err := db.ConsumePairingCode(ctx, sha256Hex(code), now); err != nil || ok {
		t.Fatalf("二次消费应拒绝（单次有效）: ok=%v err=%v", ok, err)
	}
	// TTL：10 分钟
	code2 := "999888"
	if err := db.CreatePairingCode(ctx, sha256Hex(code2), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ConsumePairingCode(ctx, sha256Hex(code2), now.Add(10*time.Minute+time.Second)); err != nil || ok {
		t.Fatalf("过期码应拒绝: ok=%v err=%v", ok, err)
	}
}

func TestInboundEventDedup(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	// 首见
	dup, conflict, err := db.RememberInboundEvent(ctx, "dev-1", "evt-1", "digest-a", `{"ok":true}`, now)
	if err != nil || dup || conflict {
		t.Fatalf("首见应登记: dup=%v conflict=%v err=%v", dup, conflict, err)
	}
	// 同 eventId 同 digest → 幂等命中
	dup, conflict, err = db.RememberInboundEvent(ctx, "dev-1", "evt-1", "digest-a", `{"ok":true}`, now)
	if err != nil || !dup || conflict {
		t.Fatalf("同 ID 同内容应幂等: dup=%v conflict=%v err=%v", dup, conflict, err)
	}
	// 同 eventId 异 digest → 冲突（4405）
	dup, conflict, err = db.RememberInboundEvent(ctx, "dev-1", "evt-1", "digest-b", `{"ok":true}`, now)
	if err != nil || !dup || !conflict {
		t.Fatalf("同 ID 异内容应冲突: dup=%v conflict=%v err=%v", dup, conflict, err)
	}
	// 设备隔离
	dup, conflict, err = db.RememberInboundEvent(ctx, "dev-2", "evt-1", "digest-b", "", now)
	if err != nil || dup || conflict {
		t.Fatalf("设备隔离失效: dup=%v conflict=%v err=%v", dup, conflict, err)
	}
	if r, ok, _ := db.GetInboundReceipt(ctx, "dev-1", "evt-1"); !ok || r != `{"ok":true}` {
		t.Fatalf("回执读取错误: %q ok=%v", r, ok)
	}
}

func TestTombstonePurgeGuard(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	insert := func(id string, purgeAfter time.Time) {
		t.Helper()
		tx, err := db.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := db.InsertTombstone(ctx, tx, Tombstone{CommandID: id, FinalState: "succeeded", TerminalAt: now, PurgeAfter: purgeAfter}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// 到期可清
	insert("cmd-gone", now.Add(-time.Second))
	// 到期但有 pending execution_unknown（guard 阻止清理）
	insert("cmd-unknown", now.Add(-time.Second))
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertCommand(ctx, tx, Command{CommandID: "cmd-unknown", Target: "d", CommandType: 203, Payload: "{}", State: "execution_unknown", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 未到期不动
	insert("cmd-keep", now.Add(24*time.Hour))

	n, err := db.PurgeDueTombstones(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("清理数 = %d, want 1（unknown guard + 未到期保留）", n)
	}
	left, err := db.Tombstones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("残留墓碑 = %d, want 2", len(left))
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
