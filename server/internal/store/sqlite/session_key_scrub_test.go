// M-4（安全审计）会话私钥残留回归测试：
//   - CloseSession 关闭即把 server_x25519_private 置空（closeLocal/被顶替路径
//     经 serveV2 defer → CloseSession 同点生效）；
//   - ScrubClosedSessionKeys 启动一次性清零全部已 closed 会话残留私钥，
//     open/pending 会话保留（运行解密派生需要）。
package sqlite

import (
	"context"
	"testing"
	"time"
)

func createSessionWithKeys(t *testing.T, db *DB, sessionID, deviceID, priv, pub string, now time.Time) {
	t.Helper()
	if err := db.CreateSession(context.Background(), Session{
		SessionID: sessionID, DeviceID: deviceID,
		ServerX25519Private: priv, ServerX25519Public: pub, DeviceX25519Public: "devpub-" + sessionID,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession %s: %v", sessionID, err)
	}
}

// TestCloseSessionScrubsPrivateKey：关闭后私钥列为 ”，公钥与状态保留。
func TestCloseSessionScrubsPrivateKey(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	createSessionWithKeys(t, db, "sess-close-0001", "dev-scrub-001", "privhex001", "pubhex001", now)

	s, ok, err := db.GetSession(ctx, "sess-close-0001")
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	if s.ServerX25519Private != "privhex001" {
		t.Fatalf("open 前私钥应保留，got %q", s.ServerX25519Private)
	}
	if err := db.OpenSession(ctx, "sess-close-0001", 0, 1, 0, now); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := db.CloseSession(ctx, "sess-close-0001", now.Add(time.Minute)); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	s, ok, err = db.GetSession(ctx, "sess-close-0001")
	if err != nil || !ok {
		t.Fatalf("close 后 GetSession: ok=%v err=%v", ok, err)
	}
	if s.State != "closed" {
		t.Fatalf("状态应 closed，got %q", s.State)
	}
	if s.ServerX25519Private != "" {
		t.Fatalf("close 后私钥列应清零，got %d hex 字符残留", len(s.ServerX25519Private))
	}
	if s.ServerX25519Public != "pubhex001" {
		t.Fatalf("close 后公钥应保留（审计面），got %q", s.ServerX25519Public)
	}
}

// TestScrubClosedSessionKeys：启动一次性清零——closed 清、open/pending 保留；
// 返回清理行数；幂等（无残留再扫 = 0）。
func TestScrubClosedSessionKeys(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	createSessionWithKeys(t, db, "sess-scrub-closed", "dev-scrub-002", "privclosed", "pubclosed", now)
	createSessionWithKeys(t, db, "sess-scrub-open", "dev-scrub-003", "privopen", "pubopen", now)
	createSessionWithKeys(t, db, "sess-scrub-pending", "dev-scrub-004", "privpending", "pubpending", now)
	if err := db.CloseSession(ctx, "sess-scrub-closed", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.OpenSession(ctx, "sess-scrub-open", 0, 1, 0, now); err != nil {
		t.Fatal(err)
	}
	// 注入历史版本残留（CloseSession 现已关闭即清零，这里模拟旧版关闭会话时
	// 私钥尚存的存量行——启动清扫的目标形态）。
	if _, err := db.sql.Exec(
		`UPDATE device_sessions SET server_x25519_private='privclosed' WHERE session_id='sess-scrub-closed'`); err != nil {
		t.Fatal(err)
	}

	n, err := db.ScrubClosedSessionKeys(ctx)
	if err != nil {
		t.Fatalf("ScrubClosedSessionKeys: %v", err)
	}
	if n != 1 {
		t.Fatalf("应清理 1 行（仅 closed），got %d", n)
	}
	check := func(id, want string) {
		t.Helper()
		s, ok, err := db.GetSession(ctx, id)
		if err != nil || !ok {
			t.Fatalf("GetSession %s: ok=%v err=%v", id, ok, err)
		}
		if s.ServerX25519Private != want {
			t.Fatalf("%s 私钥 = %q, want %q", id, s.ServerX25519Private, want)
		}
	}
	check("sess-scrub-closed", "")             // closed → 清零
	check("sess-scrub-open", "privopen")       // open → 保留（运行需要）
	check("sess-scrub-pending", "privpending") // pending → 保留（可再建连）

	// 幂等：无残留再扫为 0 行。
	if n, err := db.ScrubClosedSessionKeys(ctx); err != nil || n != 0 {
		t.Fatalf("二次扫描应 0 行，got n=%d err=%v", n, err)
	}
}
