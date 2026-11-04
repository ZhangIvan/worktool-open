package pg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These scripts exercise Claim and Runner together, without a database or
// sockets. They do not prove PostgreSQL query, SKIP LOCKED or concurrency behavior.
type callbackClaimStep struct {
	op, sql string
	args    []any
	values  []any
	rows    [][]any
	err     error
}

type callbackClaimTx struct {
	fakeTx
	t                  *testing.T
	steps              []callbackClaimStep
	next               int
	readOnly           bool
	releases, discards int
	discardErr         error
}

func (tx *callbackClaimTx) take(ctx context.Context, op, sql string, args []any) callbackClaimStep {
	tx.t.Helper()
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		tx.t.Fatal("unbounded or cancelled query")
	}
	if tx.next >= len(tx.steps) {
		tx.t.Fatalf("unexpected %s after script end", op)
	}
	s := tx.steps[tx.next]
	tx.next++
	if s.op != op || !strings.HasPrefix(sql, s.sql) || (s.args != nil && !reflect.DeepEqual(s.args, args)) {
		tx.t.Fatalf("unexpected query at step %d: %s", tx.next, sql)
	}
	return s
}

func (tx *callbackClaimTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	s := tx.take(ctx, "row", sql, args)
	return callbackTestRow{values: s.values, err: s.err}
}
func (tx *callbackClaimTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	s := tx.take(ctx, "rows", sql, args)
	return &callbackTestRows{rows: s.rows}, s.err
}
func (tx *callbackClaimTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s := tx.take(ctx, "exec", sql, args)
	return pgconn.NewCommandTag("UPDATE 1"), s.err
}

type callbackClaimConn struct{ tx *callbackClaimTx }

func (c callbackClaimConn) BeginTx(_ context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	if opts.IsoLevel != pgx.ReadCommitted || (opts.AccessMode == pgx.ReadOnly) != c.tx.readOnly {
		c.tx.t.Fatal("wrong claim transaction mode")
	}
	return c.tx, nil
}
func (c callbackClaimConn) Release() { c.tx.releases++ }
func (c callbackClaimConn) Discard(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok || ctx.Err() != nil {
		c.tx.t.Fatal("unbounded or cancelled discard")
	}
	c.tx.discards++
	return c.tx.discardErr
}

type callbackClaimPool struct {
	t    *testing.T
	txs  []*callbackClaimTx
	next int
}

func (p *callbackClaimPool) Acquire(context.Context) (Connection, error) {
	if p.next >= len(p.txs) {
		p.t.Fatal("unexpected claim transaction")
	}
	if p.next > 0 && p.txs[p.next-1].releases != 1 {
		p.t.Fatal("previous candidate not cleaned up before next candidate")
	}
	tx := p.txs[p.next]
	p.next++
	return callbackClaimConn{tx}, nil
}

func callbackClaimStore(t *testing.T, txs ...*callbackClaimTx) (*CallbackStore, *callbackClaimPool) {
	t.Helper()
	p := &callbackClaimPool{t: t, txs: txs}
	r, err := NewRunner(p, Timeouts{time.Second, time.Second, 3 * time.Second, time.Second, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := RuntimeAuthorityFromTrusted("worker_cb", "boot_cb", "recovery_cb")
	if err != nil {
		t.Fatal(err)
	}
	var policies []cb.Policy
	for _, dest := range []string{"destination_a", "destination_b"} {
		policies = append(policies, cb.Policy{Binding: cb.Binding{Tenant: "tenant_cb", Destination: dest, URL: "https://callbacks.example.com/result", KeyRef: "key_cb", PolicyVersion: 1, KeyVersion: 1}, CIDRs: []string{"93.184.216.0/24"}})
	}
	s, err := NewCallbackStore(r, runtime, func(context.Context) error { return nil }, policies)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, tx := range txs[:p.next] {
			if tx.next != len(tx.steps) || tx.rollbacks != 1 || tx.releases != 1 {
				t.Error("claim did not finish its script and cleanup exactly once")
			}
		}
	})
	return s, p
}

func callbackClaimBusyTx(t *testing.T, destination bool) *callbackClaimTx {
	t.Helper()
	tenant := callbackClaimStep{op: "row", sql: callbackLockTenant + " SKIP LOCKED", args: []any{"tenant_cb"}, err: pgx.ErrNoRows}
	tx := &callbackClaimTx{t: t, steps: []callbackClaimStep{tenant}}
	if destination {
		tx.steps[0].err = nil
		tx.steps[0].values = []any{"tenant_cb"}
		tx.steps = append(tx.steps, callbackClaimStep{op: "row", sql: callbackLockDestination + " SKIP LOCKED", args: []any{"tenant_cb", "destination_a"}, err: pgx.ErrNoRows})
	}
	return tx
}

func callbackClaimDiscoveryTx(t *testing.T) *callbackClaimTx {
	return &callbackClaimTx{t: t, readOnly: true, steps: []callbackClaimStep{{
		op: "rows", sql: "SELECT o.tenant,o.destination_id,o.delivery_id FROM unnest($1::text[],$2::text[])",
		args: []any{[]string{"tenant_cb", "tenant_cb"}, []string{"destination_a", "destination_b"}},
		rows: [][]any{{"tenant_cb", "destination_a", "delivery_a"}, {"tenant_cb", "destination_b", "delivery_b"}},
	}}}
}

func callbackClaimReadyTx(t *testing.T, dest, id string, now time.Time) *callbackClaimTx {
	t.Helper()
	body := []byte(`{"eventId":"event_cb","status":"succeeded"}`)
	lease := now.Add(10 * time.Second)
	row := func(sql string, args, values []any) callbackClaimStep {
		return callbackClaimStep{op: "row", sql: sql, args: args, values: values}
	}
	exec := func(sql string, args []any) callbackClaimStep {
		return callbackClaimStep{op: "exec", sql: sql, args: args}
	}
	tx := &callbackClaimTx{t: t, steps: []callbackClaimStep{
		row(callbackLockTenant+" SKIP LOCKED", []any{"tenant_cb"}, []any{"tenant_cb"}),
		row(callbackLockDestination+" SKIP LOCKED", []any{"tenant_cb", dest}, []any{dest}),
		row("SELECT boot_id,recovery_epoch,state", []any{"worker_cb", "worker"}, []any{"boot_cb", "recovery_cb", "active", true}),
		row("SELECT app,device,command,app_epoch", []any{"tenant_cb", id, dest}, []any{"app_cb", "device_cb", "command_cb", int64(1), int64(1), "sub_cb", int64(1)}),
		row("SELECT active AND scopes", []any{"tenant_cb", "app_cb"}, []any{true, int64(1), now.Add(time.Hour).UnixMilli()}),
		row("SELECT state='active',epoch", []any{"tenant_cb", "device_cb"}, []any{true, int64(1)}),
		row("SELECT device FROM developer_bindings", []any{"tenant_cb", "app_cb", "device_cb"}, []any{"device_cb"}),
		row("SELECT canonical_url,key_ref,policy_version,key_version", []any{"tenant_cb", dest, "app_cb"}, []any{"https://callbacks.example.com/result", "key_cb", int64(1), int64(1), true}),
		row("SELECT retired_at IS NULL", []any{"tenant_cb", "app_cb", "sub_cb", int64(1), dest}, []any{true}),
		row("SELECT id FROM commands", []any{"tenant_cb", "app_cb", "device_cb", "command_cb"}, []any{"command_cb"}),
		row(callbackReadDelivery, []any{"tenant_cb", id}, []any{dest, "ready", int64(0), 0, 4, nil, nil, nil, nil, nil, now.Add(time.Hour), nil, now.Add(-time.Minute), body, hashText(string(body)), nil, true, body}),
		row("SELECT event_id,policy_version,key_version", []any{"tenant_cb", id}, []any{"event_cb", int64(1), int64(1)}),
		row("SELECT s.slot_no,s.slot_fence", []any{"tenant", "tenant_cb", "tenant_cb"}, []any{0, int64(2)}),
		row("SELECT s.slot_no,s.slot_fence", []any{"destination", "tenant_cb", dest}, []any{0, int64(3)}),
		row(callbackFinalTime, nil, []any{now}),
		row("SELECT enabled AND (blocked_until", []any{"tenant", "tenant_cb", "tenant_cb", now}, []any{true}),
		row("SELECT enabled AND (blocked_until", []any{"destination", "tenant_cb", dest, now}, []any{true}),
		exec("UPDATE scope_limits SET tokens=", []any{"tenant", "tenant_cb", "tenant_cb", now}),
		exec("UPDATE scope_limits SET tokens=", []any{"destination", "tenant_cb", dest, now}),
		exec("UPDATE callback_outbox SET state='leased'", []any{"tenant_cb", id, int64(1), 1, now, lease, "worker_cb", "recovery_cb", int64(0), 0}),
		exec("INSERT INTO callback_attempts", []any{"tenant_cb", id, int64(1), 1, "recovery_cb", "worker_cb", now}),
		exec("UPDATE scope_slots SET owner_instance=", []any{"tenant", "tenant_cb", "tenant_cb", 0, "worker_cb", id, int64(1), "recovery_cb", lease, int64(2)}),
		exec("UPDATE scope_slots SET owner_instance=", []any{"destination", "tenant_cb", dest, 0, "worker_cb", id, int64(1), "recovery_cb", lease, int64(3)}),
		exec("INSERT INTO audit(", nil),
	}}
	return tx
}

func TestCallbackClaimCleanBusyContinuesToReadyCandidate(t *testing.T) {
	for _, destination := range []bool{false, true} {
		name := "tenant"
		if destination {
			name = "destination"
		}
		t.Run(name, func(t *testing.T) {
			for _, closed := range []bool{false, true} {
				t.Run(map[bool]string{false: "rollback_ok", true: "already_closed"}[closed], func(t *testing.T) {
					now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
					discovery, busy := callbackClaimDiscoveryTx(t), callbackClaimBusyTx(t, destination)
					if closed {
						busy.rollbackErr = pgx.ErrTxClosed
					}
					ready := callbackClaimReadyTx(t, "destination_b", "delivery_b", now)
					s, pool := callbackClaimStore(t, discovery, busy, ready)
					s.now = func() time.Time { return now }
					d, ok, err := s.Claim(context.Background())
					if err != nil || !ok || d.Handle.DeliveryID != "delivery_b" || d.Binding.Destination != "destination_b" || d.Handle.Fence != 1 || d.Handle.Attempt != 1 || d.EventID != "event_cb" || hashText(string(d.Body)) != d.Digest {
						t.Fatalf("ready tail candidate not claimed: ok=%v err=%v", ok, err)
					}
					if !d.LeaseUntil.Equal(now.Add(9900*time.Millisecond)) || pool.next != 3 || discovery.commits != 1 || busy.commits != 0 || busy.discards != 0 || ready.commits != 1 {
						t.Fatal("unsafe claim lifecycle or deadline")
					}
				})
			}
		})
	}
}

func TestCallbackClaimFailuresStopBeforeNextCandidate(t *testing.T) {
	for _, failure := range []string{"query", "rollback", "rollback_and_discard", "commit_unknown", "runtime_revoked", "authority_query", "integrity"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
			discovery := callbackClaimDiscoveryTx(t)
			failed := callbackClaimBusyTx(t, true)
			want, commits, discards := "CALLBACK_STORE_UNAVAILABLE", 0, 0
			switch failure {
			case "query":
				failed.steps[1].err = errors.New("PRIVATE query failure")
			case "rollback", "rollback_and_discard":
				failed.rollbackErr = errors.New("PRIVATE rollback failure")
				discards = 1
				if failure == "rollback_and_discard" {
					failed.discardErr = errors.New("PRIVATE discard failure")
				}
			case "commit_unknown":
				failed = callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
				failed.commitErr = errors.New("PRIVATE commit acknowledgement lost")
				want, commits, discards = cb.ErrCommitUnknown.Error(), 1, 1
			case "runtime_revoked":
				failed = callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
				failed.steps = failed.steps[:3]
				failed.steps[2].values[2] = "revoked"
				want = cb.ErrStale.Error()
			case "authority_query":
				failed = callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
				failed.steps = failed.steps[:5]
				failed.steps[4].err = errors.New("PRIVATE authority read failure")
			case "integrity":
				failed = callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
				failed.steps = failed.steps[:11]
				failed.steps[10].values[16] = false // Invalid immutable reservation graph.
				want = cb.ErrDenied.Error()
			}
			// Any acquire beyond the failed transaction fails this script, so the
			// second discovery row cannot receive a claim after an uncertain result.
			s, pool := callbackClaimStore(t, discovery, failed)
			s.now = func() time.Time { return now }
			d, ok, err := s.Claim(context.Background())
			if err == nil || err.Error() != want || ok || !reflect.DeepEqual(d, cb.Delivery{}) {
				t.Fatalf("failure granted a claim or lost classification: ok=%v err=%v", ok, err)
			}
			if pool.next != 2 || failed.commits != commits || failed.discards != discards {
				t.Fatal("failed candidate retried or cleanup changed")
			}
		})
	}
}

func TestCallbackClaimBusyCleanupFailureIsNotSkipped(t *testing.T) {
	for _, destination := range []bool{false, true} {
		name := "tenant"
		if destination {
			name = "destination"
		}
		t.Run(name, func(t *testing.T) {
			for _, discardFails := range []bool{false, true} {
				t.Run(map[bool]string{false: "rollback", true: "rollback_and_discard"}[discardFails], func(t *testing.T) {
					tx := callbackClaimBusyTx(t, destination)
					tx.rollbackErr = errors.New("PRIVATE rollback connection failure")
					if discardFails {
						tx.discardErr = errors.New("PRIVATE discard connection failure")
					}
					s, pool := callbackClaimStore(t, tx)
					d, ok, err := s.claimOne(context.Background(), callbackCandidate{"tenant_cb", "destination_a", "delivery_a"})
					if err == nil || err.Error() != "CALLBACK_STORE_UNAVAILABLE" || ok || !reflect.DeepEqual(d, cb.Delivery{}) {
						t.Fatalf("cleanup failure was treated as safe busy skip: ok=%v err=%v", ok, err)
					}
					if pool.next != 1 || tx.commits != 0 || tx.discards != 1 {
						t.Fatal("cleanup failure retried or did not discard")
					}
				})
			}
		})
	}
}
