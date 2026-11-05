// outbox.http_status 投递侧回归（投递记录 http_status 独立字段车道，2026-11-05）：
//   - 2xx 投递成功 → delivered 行 http_status = 响应码；
//   - 4xx/5xx 响应（重试/死信）→ http_status = 该次响应码；
//   - 传输错误（连接拒绝/无 HTTP 响应）→ http_status 保持 NULL；
//   - skipped（无接收器终态）→ http_status 保持 NULL。
package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

func newHTTPStatusDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt-hs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestOutboxHTTPStatusDelivered：2xx 投递成功 → delivered 行 http_status=响应码
//（接收器返回 200，断言独立字段恰存响应码本身而非 0/文本）。
func TestOutboxHTTPStatusDelivered(t *testing.T) {
	db := newHTTPStatusDB(t)
	const secret = "cb-hs-201"
	rcv := newReceiver(t, secret, 0) // 直接 200
	w, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    rcv.srv.URL + "/callback",
		CallbackSecret: secret,
	}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-hs-delivered")
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	delivered, err := db.OutboxByStatus(context.Background(), "delivered")
	if err != nil || len(delivered) != 1 {
		t.Fatalf("应恰 1 行 delivered: n=%d err=%v", len(delivered), err)
	}
	if !delivered[0].HTTPStatus.Valid || delivered[0].HTTPStatus.Int64 != http.StatusOK {
		t.Fatalf("delivered 行 http_status 应为 200: %+v", delivered[0].HTTPStatus)
	}
}

// TestOutboxHTTPStatusServerError：接收器恒 500 → 死信行 http_status=500
// （既有 TestOutboxDeadLetterAfterFourth 的 http_status 投影）。
func TestOutboxHTTPStatusServerError(t *testing.T) {
	db := newHTTPStatusDB(t)
	const secret = "cb-hs-500"
	rcv := newReceiver(t, secret, 1000) // 恒 500
	w, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    rcv.srv.URL + "/callback",
		CallbackSecret: secret,
		RetryDelays:    []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond},
	}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-hs-dead")
	for i := 0; i < 12; i++ {
		if err := w.DeliverDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		dead, _ := db.OutboxByStatus(context.Background(), "dead")
		if len(dead) == 1 {
			if !dead[0].HTTPStatus.Valid || dead[0].HTTPStatus.Int64 != http.StatusInternalServerError {
				t.Fatalf("dead 行 http_status 应为 500: %+v", dead[0].HTTPStatus)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("未在预期尝试内死信")
}

// TestOutboxHTTPStatusTransportError：目标端口无监听（连接拒绝，无 HTTP 响应）
// → 重试中的行 http_status 保持 NULL（不落 0、不落 LastError 的数字）。
func TestOutboxHTTPStatusTransportError(t *testing.T) {
	db := newHTTPStatusDB(t)
	w, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    "http://127.0.0.1:1/callback", // port 1：连接拒绝
		CallbackSecret: "cb-hs-tx",
		RetryDelays:    []time.Duration{time.Hour, time.Hour, time.Hour}, // 单轮仅一次尝试，行留 pending
	}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-hs-txerr")
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := db.OutboxByStatus(context.Background(), "pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("传输错误后应留 1 行 pending: n=%d err=%v", len(pending), err)
	}
	if pending[0].HTTPStatus.Valid {
		t.Fatalf("传输错误行 http_status 应为 NULL: %+v", pending[0].HTTPStatus)
	}
	if pending[0].LastError != "transport error" {
		t.Fatalf("传输错误 lastError 应为 transport error: %q", pending[0].LastError)
	}
}

// TestOutboxHTTPStatusSkippedNULL：该 kind 无回调绑定（存在其他绑定使 worker
// 进入逐行解析）→ 行终态 skipped，http_status 保持 NULL（从未发出 HTTP 请求）。
func TestOutboxHTTPStatusSkippedNULL(t *testing.T) {
	db := newHTTPStatusDB(t)
	ctx := context.Background()
	// robot-hs 绑定 type=11（消息域）——系统存在接收器；exec_result（type=1 域）
	// 未绑且全局 URL 空 → 该行解析目标为空 → skipped。
	if err := seedRobotBinding(ctx, db, "robot-hs", 11, "http://127.0.0.1:2/cb"); err != nil {
		t.Fatal(err)
	}
	w, err := NewOutboxWorker(db, OutboxConfig{CallbackSecret: "cb-hs-skip"}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-hs-skipped")
	if err := w.DeliverDue(ctx); err != nil {
		t.Fatal(err)
	}
	skipped, err := db.OutboxByStatus(ctx, "skipped")
	if err != nil || len(skipped) != 1 {
		t.Fatalf("应恰 1 行 skipped: n=%d err=%v", len(skipped), err)
	}
	if skipped[0].HTTPStatus.Valid {
		t.Fatalf("skipped 行 http_status 应为 NULL: %+v", skipped[0].HTTPStatus)
	}
}
