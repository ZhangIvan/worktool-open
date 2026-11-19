package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These adapters execute the real authentication, consumption and Upgrade code
// without a database or network listener. They script only the successful old
// consumption and a later session already being present before it returns.
// This proves local replacement ordering, not PostgreSQL transaction behavior.
type replacementRow struct {
	values []any
	err    error
}

func (r replacementRow) Scan(out ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(out) != len(r.values) {
		return errors.New("unexpected replacement row width")
	}
	for i, v := range r.values {
		d := reflect.ValueOf(out[i]).Elem()
		if reflect.TypeOf(v) != d.Type() {
			return errors.New("unexpected replacement row type")
		}
		d.Set(reflect.ValueOf(v))
	}
	return nil
}

type replacementRows struct {
	pgx.Rows
	row  replacementRow
	done bool
}

func (r *replacementRows) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}
func (r *replacementRows) Scan(out ...any) error { return r.row.Scan(out...) }
func (r *replacementRows) Close()                {}
func (r *replacementRows) Err() error            { return nil }

type replacementTx struct {
	pgx.Tx
	session        string
	fingerprint    string
	now            time.Time
	consuming      bool
	committed      bool
	onConsume      func()
	queries, execs int
}

func (tx *replacementTx) QueryRow(_ context.Context, q string, _ ...any) pgx.Row {
	tx.queries++
	switch {
	case strings.HasPrefix(q, "SELECT boot_id,recovery_epoch,state,"):
		return replacementRow{values: []any{"boot_demo", "recovery_demo", "active", true}}
	case strings.HasPrefix(q, "SELECT s.tenant,s.device,s.id,"):
		return replacementRow{values: []any{"tenant_demo", "device_demo", tx.session, int64(0), tx.now.Add(time.Minute).UnixMilli(), false, "active", int64(0), false, ""}}
	case strings.HasPrefix(q, "SELECT n,floor(extract(epoch FROM n)"):
		return replacementRow{values: []any{tx.now, tx.now.UnixMilli()}}
	case strings.HasPrefix(q, "SELECT capacity_jobs,capacity_bytes,"):
		return replacementRow{values: []any{int64(100), int64(100000), int64(0), int64(0), true, true}}
	case strings.HasPrefix(q, "SELECT session_id,owner_instance,"):
		return replacementRow{err: pgx.ErrNoRows}
	case strings.HasPrefix(q, "SELECT state,epoch,policy,capabilities"):
		return replacementRow{values: []any{"active", int64(0), int64(1), true}}
	case strings.HasPrefix(q, "SELECT ws_consumed FROM device_sessions"):
		return replacementRow{values: []any{false}}
	default:
		panic("unexpected replacement QueryRow")
	}
}
func (tx *replacementTx) Query(_ context.Context, q string, _ ...any) (pgx.Rows, error) {
	tx.queries++
	if !strings.HasPrefix(q, "SELECT id,token,epoch,expires,revoked FROM device_sessions") {
		panic("unexpected replacement Query")
	}
	return &replacementRows{row: replacementRow{values: []any{tx.session, tx.fingerprint, int64(0), tx.now.Add(time.Minute).UnixMilli(), false}}}, nil
}
func (tx *replacementTx) Exec(_ context.Context, q string, _ ...any) (pgconn.CommandTag, error) {
	tx.execs++
	switch {
	case strings.HasPrefix(q, "UPDATE device_sessions SET ws_consumed=true,"):
		tx.consuming = true
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.HasPrefix(q, "INSERT INTO audit("):
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	default:
		panic("unexpected replacement Exec")
	}
}
func (tx *replacementTx) Commit(context.Context) error {
	tx.committed = true
	if tx.consuming && tx.onConsume != nil {
		tx.onConsume()
	}
	return nil
}
func (tx *replacementTx) Rollback(context.Context) error { return pgx.ErrTxClosed }

type replacementPool struct{ tx *replacementTx }

func (p replacementPool) Acquire(context.Context) (pg.Connection, error)         { return p, nil }
func (p replacementPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return p.tx, nil }
func (replacementPool) Release()                                                 {}
func (replacementPool) Discard(context.Context) error                            { return nil }

func replacementStore(t *testing.T) (*pg.GatewayStore, *replacementTx) {
	t.Helper()
	tx := &replacementTx{now: time.Now()}
	runner, err := pg.NewRunner(replacementPool{tx}, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 2 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := pg.RuntimeAuthorityFromTrusted("gateway_demo", "boot_demo", "recovery_demo")
	if err != nil {
		t.Fatal(err)
	}
	store, err := pg.NewGatewayStore(runner, func(context.Context) error { return nil }, runtime)
	if err != nil {
		t.Fatal(err)
	}
	return store, tx
}

func replacementAuth(t *testing.T, store *pg.GatewayStore, tx *replacementTx, session string) pg.SessionAuth {
	t.Helper()
	raw := strings.Repeat("synthetic_", 4) + session
	fingerprint := sha256.Sum256([]byte(raw))
	tx.session, tx.fingerprint = session, hex.EncodeToString(fingerprint[:])
	a, err := store.AuthenticateSession(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// No socket: writes are discarded and reads terminate immediately after the
// handshake, so Upgrade never sees HELLO or acquires a lease in this fixture.
type replacementConn struct{}

func (replacementConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (replacementConn) Write(p []byte) (int, error)      { return len(p), nil }
func (replacementConn) Close() error                     { return nil }
func (replacementConn) LocalAddr() net.Addr              { return replacementAddr("local") }
func (replacementConn) RemoteAddr() net.Addr             { return replacementAddr("peer") }
func (replacementConn) SetDeadline(time.Time) error      { return nil }
func (replacementConn) SetReadDeadline(time.Time) error  { return nil }
func (replacementConn) SetWriteDeadline(time.Time) error { return nil }

type replacementAddr string

func (replacementAddr) Network() string  { return "in_memory" }
func (a replacementAddr) String() string { return string(a) }

type replacementWriter struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (w *replacementWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	c := replacementConn{}
	return c, bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c)), nil
}

func TestUpgradeDoesNotReplaceBeforeLeaseAcquisition(t *testing.T) {
	store, tx := replacementStore(t)
	newAuth := replacementAuth(t, store, tx, "session_new")
	oldAuth := replacementAuth(t, store, tx, "session_old")
	c, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	readCtx, readCancel := context.WithCancel(context.Background())
	newer := &connection{parent: c, ctx: ctx, cancel: cancel, readCtx: readCtx, readCancel: readCancel, auth: newAuth, ingress: newIngressQueue(), lease: &pg.LeaseHandle{Tenant: "tenant_demo", DeviceID: "device_demo", SessionID: "session_new", RecoveryEpoch: "recovery_demo", Fence: 2}}
	t.Cleanup(func() { cancel(); readCancel() })
	inserted := false
	tx.onConsume = func() {
		// Model old consumption's committed return being delayed while the new
		// session finishes acquiring its lease. No fake timing sleeps are used.
		c.mu.Lock()
		defer c.mu.Unlock()
		for x := range c.live {
			_, _, session, _ := x.auth.Identity()
			if session != "" {
				t.Fatal("old auth unexpectedly published before consume returned")
			}
		}
		c.live[newer] = struct{}{}
		inserted = true
	}
	r := httptest.NewRequest(http.MethodGet, "http://in-memory.invalid/ws", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	w := &replacementWriter{ResponseRecorder: httptest.NewRecorder()}
	c.Upgrade(w, r, oldAuth)
	if !inserted || !tx.committed || tx.execs != 2 || !w.hijacked {
		t.Fatalf("fixture did not reach committed consumption and accepted upgrade: inserted=%v committed=%v execs=%d hijacked=%v", inserted, tx.committed, tx.execs, w.hijacked)
	}
	if newer.ctx.Err() != nil {
		t.Error("delayed older Upgrade canceled the newer leased connection before acquiring its own lease")
	}
	c.mu.Lock()
	delete(c.live, newer)
	c.mu.Unlock()
	join, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if err := c.Shutdown(join); err != nil {
		t.Fatal(err)
	}
	if c.LiveCount() != 0 || c.CleanupErrors() != 0 {
		t.Fatal("upgrade did not clean up")
	}
}
