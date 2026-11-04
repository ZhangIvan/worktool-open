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

// TestPairingCodeInvalidateAndCleanup（H-1 单活动码 + L-3 清理，安全审计 TOP5）：
//   - InvalidateUnusedPairingCodes 把全部未使用旧码置 used（不可再消费）；
//   - CleanupPairingCodes 删 TTL 过 24h（used_at/expires_at）的行，24h 内不删。
func TestPairingCodeInvalidateAndCleanup(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	// 两个未使用码 → 签发新码前置 used
	for _, c := range []string{"111111", "222222"} {
		if err := db.CreatePairingCode(ctx, sha256Hex(c), now.Add(10*time.Minute)); err != nil {
			t.Fatalf("签发 %s: %v", c, err)
		}
	}
	n, err := db.InvalidateUnusedPairingCodes(ctx, now)
	if err != nil || n != 2 {
		t.Fatalf("应置 used 2 条: n=%d err=%v", n, err)
	}
	if ok, _ := db.ConsumePairingCode(ctx, sha256Hex("111111"), now); ok {
		t.Fatal("旧码置 used 后应不可消费（单活动码）")
	}
	if n, _ := db.InvalidateUnusedPairingCodes(ctx, now); n != 0 {
		t.Fatalf("无未使用码应返回 0: n=%d", n)
	}

	// 清理：used_at 过 24h 才删（used_at=now）
	if n, err := db.CleanupPairingCodes(ctx, now.Add(23*time.Hour)); err != nil || n != 0 {
		t.Fatalf("24h 内不得删除: n=%d err=%v", n, err)
	}
	if n, err := db.CleanupPairingCodes(ctx, now.Add(24*time.Hour+time.Minute)); err != nil || n != 2 {
		t.Fatalf("过 24h 应删 2 条已用码: n=%d err=%v", n, err)
	}

	// 过期未用码：expires_at 过 24h 即删
	if err := db.CreatePairingCode(ctx, sha256Hex("333333"), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := db.CleanupPairingCodes(ctx, now.Add(34*time.Hour+time.Minute)); err != nil || n != 1 {
		t.Fatalf("过期码过 24h 应删: n=%d err=%v", n, err)
	}
	rows, err := db.QueryDump(ctx, `SELECT COUNT(*) FROM pairing_codes`)
	if err != nil || len(rows) != 1 || rows[0][0] != "0" {
		t.Fatalf("清理后表应为空: rows=%v err=%v", rows, err)
	}
}

// TestUpsertDeviceRobotIDPreserved 回归（2026-11-03 会话 9 根因）：
// 同 Ed25519 身份重配对不得抹掉管理面 robot_id 绑定；公钥变化（身份变更）
// 才清空；显式传值可覆盖。
func TestUpsertDeviceRobotIDPreserved(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 3, 23, 0, 0, 0, time.UTC)
	const dev = "lab-device-syn"
	pair := func(robotID, edKey string) {
		t.Helper()
		if err := db.UpsertDevice(ctx, Device{
			DeviceID: dev, RobotID: robotID, DeviceName: "RIG78-lab",
			Ed25519PublicKey:   edKey,
			DeviceX25519Public: "f38f111a00dd5ecc102da118f84a65e9e1c47442b73a7f3711ec01745adf3809",
			TokenHash:          sha256Hex("tok"), TokenVersion: 1,
			Status: "active", PairedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
	}
	const keyA = "a8d297defb2115902dffe88cb81480a199a8e918fa72493ae01260bd3475aa18"
	const keyB = "b8d297defb2115902dffe88cb81480a199a8e918fa72493ae01260bd3475aa18"

	// ① 首次配对（无绑定）+ 管理面绑定
	pair("", keyA)
	if _, err := db.Exec(ctx, `UPDATE devices SET robot_id='lab-robot-syn' WHERE device_id=?`, dev); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// ② 同身份重配对（pair.go 路径不传 robot_id）→ 绑定必须存活
	pair("", keyA)
	d, ok, err := db.GetDevice(ctx, dev)
	if err != nil || !ok {
		t.Fatalf("GetDevice: ok=%v err=%v", ok, err)
	}
	if d.RobotID != "lab-robot-syn" {
		t.Fatalf("同身份重配对后 robot_id 应保留，实际=%q", d.RobotID)
	}
	// ③ 公钥变化重配对 → 绑定清空（防旧绑定指向新凭据持有者）
	pair("", keyB)
	if d, _, _ := db.GetDevice(ctx, dev); d.RobotID != "" {
		t.Fatalf("公钥变化重配对后 robot_id 应清空，实际=%q", d.RobotID)
	}
	// ④ 显式传值覆盖（v2 轮换/v1 登记/管理面改绑路径）
	pair("lab-driver-99", keyB)
	if d, _, _ := db.GetDevice(ctx, dev); d.RobotID != "lab-driver-99" {
		t.Fatalf("显式传值应覆盖，实际=%q", d.RobotID)
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

// TestSetInboundReceipt（性能审计 H1 服务端半边）：回执记忆（空 receipt 回填 +
// 幂等不覆盖）。
func TestSetInboundReceipt(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if dup, conflict, err := db.RememberInboundEvent(ctx, "dev-1", "evt-1", "digest-a", "", now); err != nil || dup || conflict {
		t.Fatalf("首见应登记: dup=%v conflict=%v err=%v", dup, conflict, err)
	}
	// 空 receipt 不写入（无回执语义）
	if err := db.SetInboundReceipt(ctx, "dev-1", "evt-1", ""); err != nil {
		t.Fatalf("空回执应 no-op: %v", err)
	}
	if r, ok, _ := db.GetInboundReceipt(ctx, "dev-1", "evt-1"); !ok || r != "" {
		t.Fatalf("空回执不应落库: %q ok=%v", r, ok)
	}
	// 回填后可读取
	if err := db.SetInboundReceipt(ctx, "dev-1", "evt-1", `{"socketType":3,"list":[]}`); err != nil {
		t.Fatalf("回执回填: %v", err)
	}
	if r, ok, _ := db.GetInboundReceipt(ctx, "dev-1", "evt-1"); !ok || r != `{"socketType":3,"list":[]}` {
		t.Fatalf("回执应回填: %q ok=%v", r, ok)
	}
	// 幂等：重复处理不覆盖既有回执
	if err := db.SetInboundReceipt(ctx, "dev-1", "evt-1", `{"socketType":3,"list":[1]}`); err != nil {
		t.Fatalf("重复回填: %v", err)
	}
	if r, _, _ := db.GetInboundReceipt(ctx, "dev-1", "evt-1"); r != `{"socketType":3,"list":[]}` {
		t.Fatalf("既有回执不得被覆盖: %q", r)
	}
	// 行不存在 = no-op（帧未被记忆即未被消费）
	if err := db.SetInboundReceipt(ctx, "dev-1", "evt-x", `{"x":1}`); err != nil {
		t.Fatalf("未知帧回填应 no-op 不报错: %v", err)
	}
}

// TestAllocateEpochs（性能审计 H3）：单事务批量分配连续 epoch，单调性与
// BumpEpoch 互操作一致。
func TestAllocateEpochs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	// 新设备批量分配 3 个 → first=1..3
	first, err := db.AllocateEpochs(ctx, "dev-alloc", 3)
	if err != nil {
		t.Fatalf("批量分配: %v", err)
	}
	if first != 1 {
		t.Fatalf("首个 epoch = %d, want 1", first)
	}
	d, ok, _ := db.GetDevice(ctx, "dev-alloc")
	if !ok || d.Epoch != 3 {
		t.Fatalf("设备已分配 epoch = %d (ok=%v), want 3", d.Epoch, ok)
	}
	// 追加单个 = BumpEpoch 语义（4）
	first, err = db.AllocateEpochs(ctx, "dev-alloc", 1)
	if err != nil || first != 4 {
		t.Fatalf("追加单个 epoch = %d err=%v, want 4", first, err)
	}
	// 与 BumpEpoch 互操作单调（5）
	if e, err := db.BumpEpoch(ctx, "dev-alloc"); err != nil || e != 5 {
		t.Fatalf("BumpEpoch = %d err=%v, want 5", e, err)
	}
	// n<=0 不消费
	if e, err := db.AllocateEpochs(ctx, "dev-alloc", 0); err != nil || e != 0 {
		t.Fatalf("n=0 应不消费: e=%d err=%v", e, err)
	}
	if d, _, _ := db.GetDevice(ctx, "dev-alloc"); d.Epoch != 5 {
		t.Fatalf("n=0 消费后 epoch = %d, want 5", d.Epoch)
	}
	// 设备隔离：另一设备独立计数
	if first, _ := db.AllocateEpochs(ctx, "dev-alloc-2", 2); first != 1 {
		t.Fatalf("独立设备首个 epoch = %d, want 1", first)
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
