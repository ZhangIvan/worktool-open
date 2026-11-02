package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store/sqlite"
)

func newOutboxDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "wt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func enqueueOutboxRow(t *testing.T, db *sqlite.DB, commandID string) {
	t.Helper()
	tx, err := db.Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := db.EnqueueOutbox(context.Background(), tx, "exec_result", commandID,
		map[string]any{"commandId": commandID, "errorCode": 0}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// receiver 为 loopback 测试接收器（可编程失败次数 + 签名校验）。
type receiver struct {
	mu          sync.Mutex
	failFirst   int // 前 N 次 500
	gotBodies   []string
	gotSigns    []string
	gotTs       []string
	badSignSeen bool
	srv         *httptest.Server
}

func newReceiver(t *testing.T, secret string, failFirst int) *receiver {
	t.Helper()
	r := &receiver{failFirst: failFirst}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		ts := req.Header.Get("X-WT-Timestamp")
		sig := req.Header.Get("X-WT-Signature")
		r.mu.Lock()
		n := len(r.gotBodies)
		r.gotBodies = append(r.gotBodies, string(body))
		r.gotSigns = append(r.gotSigns, sig)
		r.gotTs = append(r.gotTs, ts)
		r.mu.Unlock()
		var tsI int64
		_ = json.Unmarshal([]byte(ts), &tsI)
		if !VerifySign(secret, tsI, body, sig) {
			r.mu.Lock()
			r.badSignSeen = true
			r.mu.Unlock()
		}
		if n < r.failFirst {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.srv = httptest.NewServer(mux)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.gotBodies)
}

// TestOutboxDeliveryRetrySchedule：签名正确 + 重试计划命中 + 送达。
func TestOutboxDeliveryRetrySchedule(t *testing.T) {
	db := newOutboxDB(t)
	const secret = "cb-secret-1"
	rcv := newReceiver(t, secret, 2) // 前 2 次 500，第 3 次 200
	w, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    rcv.srv.URL + "/callback",
		CallbackSecret: secret,
		RetryDelays:    []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond},
	}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return time.Now().UTC() }
	enqueueOutboxRow(t, db, "cmd-retry-1")

	// 手动步进投递（等价于 worker 周期行为，确定性更强）
	for i := 0; i < 8 && rcv.attempts() < 3; i++ {
		if err := w.DeliverDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(12 * time.Millisecond)
	}
	if rcv.attempts() != 3 {
		t.Fatalf("尝试次数 = %d, want 3（2 失败 + 1 成功）", rcv.attempts())
	}
	dead, _ := db.OutboxByStatus(context.Background(), "dead")
	delivered, _ := db.OutboxByStatus(context.Background(), "delivered")
	if len(dead) != 0 || len(delivered) != 1 {
		t.Fatalf("终态错误: delivered=%d dead=%d", len(delivered), len(dead))
	}
	if rcv.badSignSeen {
		t.Fatal("接收器验签失败（HMAC 签名实现错误）")
	}
}

// TestOutboxDeadLetterAfterFourth：恒败 → 4 次尝试（1+1/2/4 节奏）→ 死信 + 审计。
func TestOutboxDeadLetterAfterFourth(t *testing.T) {
	db := newOutboxDB(t)
	const secret = "cb-secret-2"
	rcv := newReceiver(t, secret, 1000) // 恒 500
	w, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    rcv.srv.URL + "/callback",
		CallbackSecret: secret,
		RetryDelays:    []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond},
	}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-dead-1")

	for i := 0; i < 12; i++ {
		if err := w.DeliverDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(6 * time.Millisecond)
		dead, _ := db.OutboxByStatus(context.Background(), "dead")
		if len(dead) == 1 {
			if got := rcv.attempts(); got != 4 {
				t.Fatalf("死信前尝试次数 = %d, want 4（首发+3 重试，默认 1/2/4s 节奏）", got)
			}
			audits, err := db.QueryDump(context.Background(), `SELECT kind FROM audit_log WHERE kind='outbox_dead_letter'`)
			if err != nil || len(audits) != 1 {
				t.Fatalf("死信审计缺失: %v %+v", err, audits)
			}
			return
		}
	}
	t.Fatal("未在预期尝试内死信")
}

// TestOutboxDefaultRetrySchedule：默认重试节奏 = 1s/2s/4s（共 4 次，第 4 次死信）。
func TestOutboxDefaultRetrySchedule(t *testing.T) {
	if len(DefaultRetryDelays) != 3 ||
		DefaultRetryDelays[0] != time.Second ||
		DefaultRetryDelays[1] != 2*time.Second ||
		DefaultRetryDelays[2] != 4*time.Second {
		t.Fatalf("默认重试节奏漂移: %v（want 1s/2s/4s）", DefaultRetryDelays)
	}
}

// TestOutboxRejectsNonLoopback：默认禁外网——非 loopback 回调 URL 构造即拒绝。
func TestOutboxRejectsNonLoopback(t *testing.T) {
	db := newOutboxDB(t)
	if _, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    "http://203.0.113.10:9000/cb",
		CallbackSecret: "s",
	}, testLogger{t}); err == nil {
		t.Fatal("非 loopback 回调应拒绝（默认禁外网）")
	}
	if _, err := NewOutboxWorker(db, OutboxConfig{
		CallbackURL:    "http://203.0.113.10:9000/cb",
		CallbackSecret: "s",
		// 显式放行（部署侧强制 TLS，SR-01）
		AllowNonLoopback: true,
	}, testLogger{t}); err != nil {
		t.Fatalf("显式放行应允许: %v", err)
	}
	// loopback 各形态放行
	for _, u := range []string{"http://127.0.0.1:1/cb", "http://localhost:2/cb", "http://[::1]:3/cb"} {
		if _, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: u, CallbackSecret: "s"}, testLogger{t}); err != nil {
			t.Fatalf("loopback %s 应放行: %v", u, err)
		}
	}
	// 未配置 secret 拒绝由 config.Validate 承担；非法 scheme 拒绝
	if _, err := NewOutboxWorker(db, OutboxConfig{CallbackURL: "ftp://127.0.0.1/cb", CallbackSecret: "s"}, testLogger{t}); err == nil {
		t.Fatal("非 http(s) scheme 应拒绝")
	}
}

// TestOutboxNoURLStaysPending：未配置回调 → 行保持 pending（禁外网缺省形态）。
func TestOutboxNoURLStaysPending(t *testing.T) {
	db := newOutboxDB(t)
	w, err := NewOutboxWorker(db, OutboxConfig{}, testLogger{t})
	if err != nil {
		t.Fatal(err)
	}
	enqueueOutboxRow(t, db, "cmd-pending-1")
	if err := w.DeliverDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, _ := db.OutboxByStatus(context.Background(), "pending")
	if len(items) != 1 {
		t.Fatalf("未配置回调时行应保持 pending: %+v", items)
	}
}

type testLogger struct{ t *testing.T }

func (l testLogger) Printf(format string, args ...any) { l.t.Logf("[outbox] "+format, args...) }
