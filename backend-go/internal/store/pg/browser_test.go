package pg

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/consoleapi"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type browserRow struct {
	values []any
	err    error
}

func (r browserRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected selected columns")
	}
	for i, v := range r.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

type browserRows struct {
	pgx.Rows
	records [][]any
	index   int
	closed  bool
}

func (r *browserRows) Next() bool {
	if r.index >= len(r.records) {
		return false
	}
	r.index++
	return true
}
func (r *browserRows) Scan(dest ...any) error {
	return (browserRow{values: r.records[r.index-1]}).Scan(dest...)
}
func (r *browserRows) Close()     { r.closed = true }
func (r *browserRows) Err() error { return nil }

type browserTx struct {
	fakeTx
	t                  *testing.T
	tenant, key, query string
	row                browserRow
	rows               *browserRows
	runtimeInvalid     bool
}

func (t *browserTx) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	if strings.Contains(q, "FROM runtime_instances") {
		if len(args) != 1 || args[0] != "runtime-a" || strings.Contains(q, "FOR SHARE") {
			t.t.Fatal("unsafe runtime read")
		}
		return browserRow{values: []any{"boot-a", "epoch-a", "active", !t.runtimeInvalid}}
	}
	t.query = q
	if len(args) != 2 || args[0] != t.tenant || args[1] != t.key || !strings.Contains(q, "WHERE tenant=$1 AND ") {
		t.t.Fatal("unscoped user query", q, args)
	}
	return t.row
}
func (t *browserTx) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	t.query = q
	if len(args) != 1 || args[0] != t.tenant || !strings.Contains(q, "WHERE tenant=$1") || !strings.Contains(q, "LIMIT 201") {
		t.t.Fatal("unbounded or unscoped devices", q, args)
	}
	for _, bad := range []string{"secret", "key_id", "SELECT *", "session", "token"} {
		if strings.Contains(q, bad) {
			t.t.Fatal("secret column selected", q)
		}
	}
	return t.rows, nil
}

type browserConn struct {
	tx   *browserTx
	opts pgx.TxOptions
}

func (c *browserConn) BeginTx(_ context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	c.opts = opts
	return c.tx, nil
}
func (c *browserConn) Release()                      {}
func (c *browserConn) Discard(context.Context) error { return nil }

type browserPool struct {
	conn     *browserConn
	acquires int
}

func (p *browserPool) Acquire(context.Context) (Connection, error) { p.acquires++; return p.conn, nil }
func browserFixture(t *testing.T, gate func(context.Context) error) (*BrowserStore, *browserTx, *browserPool) {
	t.Helper()
	tx := &browserTx{t: t, tenant: "tenant-a", key: "admin", row: browserRow{values: []any{"tenant-a", "u1", "admin", "admin", "!disabled", true}}, rows: &browserRows{records: [][]any{{"d1", "name", "active", int64(1), int64(0), []string{"message.send_text"}}}}}
	p := &browserPool{conn: &browserConn{tx: tx}}
	r, e := NewRunner(p, Timeouts{time.Second, time.Second, 2 * time.Second, time.Second, time.Second})
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewBrowserStore(r, gate, RuntimeAuthority{instance: "runtime-a", boot: "boot-a", epoch: "epoch-a", trusted: true})
	if e != nil {
		t.Fatal(e)
	}
	return s, tx, p
}
func TestBrowserScopedReadOnlyUser(t *testing.T) {
	for _, byID := range []bool{false, true} {
		t.Run(map[bool]string{false: "login", true: "current"}[byID], func(t *testing.T) {
			s, tx, p := browserFixture(t, func(context.Context) error { return nil })
			if byID {
				tx.key = "u1"
				u, e := s.CurrentUser(context.Background(), "tenant-a", "u1")
				if e != nil || u.ID != "u1" || !strings.Contains(tx.query, "AND id=$2") {
					t.Fatal(u, e)
				}
			} else {
				u, e := s.FindUser(context.Background(), "tenant-a", "admin")
				if e != nil || u.Tenant != "tenant-a" || !strings.Contains(tx.query, "AND login=$2") {
					t.Fatal(u, e)
				}
			}
			if p.conn.opts.AccessMode != pgx.ReadOnly || tx.commits != 1 {
				t.Fatal("not read only")
			}
		})
	}
}
func TestBrowserRecoveryGateBeforeAcquire(t *testing.T) {
	s, _, p := browserFixture(t, func(context.Context) error { return errors.New("recovery") })
	if _, e := s.FindUser(context.Background(), "tenant-a", "admin"); e == nil || p.acquires != 0 {
		t.Fatal("acquired before gate")
	}
	if _, e := s.ListDevices(context.Background(), browserAuthority()); e == nil || p.acquires != 0 {
		t.Fatal("device gate missing")
	}
}
func TestBrowserSelectedDeviceFieldsAndBounds(t *testing.T) {
	s, tx, _ := browserFixture(t, func(context.Context) error { return nil })
	tx.key = "u1"
	out, e := s.ListDevices(context.Background(), browserAuthority())
	if e != nil || len(out) != 1 || out[0].ID != "d1" || !tx.rows.closed {
		t.Fatal(out, e)
	}
	s, tx, _ = browserFixture(t, func(context.Context) error { return nil })
	tx.key = "u1"
	row := tx.rows.records[0]
	for len(tx.rows.records) < 201 {
		tx.rows.records = append(tx.rows.records, row)
	}
	out, e = s.ListDevices(context.Background(), browserAuthority())
	if e == nil || out != nil || !tx.rows.closed {
		t.Fatal("partial/unbounded device result")
	}
}
func TestBrowserNoPartialOnCommitFailure(t *testing.T) {
	s, tx, _ := browserFixture(t, func(context.Context) error { return nil })
	tx.commitErr = errors.New("commit")
	u, e := s.FindUser(context.Background(), "tenant-a", "admin")
	if e == nil || u.ID != "" {
		t.Fatal("returned data on uncertain commit")
	}
}
func TestBrowserGateRechecked(t *testing.T) {
	n := 0
	s, _, _ := browserFixture(t, func(context.Context) error {
		n++
		if n == 3 {
			return errors.New("recovery changed")
		}
		return nil
	})
	u, e := s.FindUser(context.Background(), "tenant-a", "admin")
	if e == nil || u.ID != "" {
		t.Fatal("gate change leaked result")
	}
}

func browserAuthority() consoleapi.DeviceAuthority {
	return consoleapi.DeviceAuthority{Tenant: "tenant-a", ID: "u1", Role: "admin", PasswordFingerprint: sha256.Sum256([]byte("!disabled"))}
}
func TestBrowserDeviceRevocationSnapshot(t *testing.T) {
	for _, kind := range []string{"role", "password", "active"} {
		t.Run(kind, func(t *testing.T) {
			s, tx, p := browserFixture(t, func(context.Context) error { return nil })
			tx.key = "u1"
			switch kind {
			case "role":
				tx.row.values[3] = "operator"
			case "password":
				tx.row.values[4] = "changed"
			case "active":
				tx.row.values[5] = false
			}
			out, e := s.ListDevices(context.Background(), browserAuthority())
			if !errors.Is(e, consoleapi.ErrUnauthenticated) || out != nil || tx.rows.index != 0 {
				t.Fatal("revocation exposed devices")
			}
			if p.conn.opts.IsoLevel != pgx.RepeatableRead {
				t.Fatal("inconsistent snapshot")
			}
		})
	}
}

func TestBrowserRuntimeRevoked(t *testing.T) {
	s, tx, _ := browserFixture(t, func(context.Context) error { return nil })
	tx.runtimeInvalid = true
	out, e := s.ListDevices(context.Background(), browserAuthority())
	if e == nil || out != nil || tx.rows.index != 0 {
		t.Fatal("revoked runtime exposed data")
	}
}
