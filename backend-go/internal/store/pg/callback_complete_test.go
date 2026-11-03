package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// This is a scripted pgx boundary fake, not a SQL parser, database, lock proof or
// transport. It applies the expected fixture changes only on a fake Commit.
type callbackTestRow struct {
	values []any
	err    error
}

func (r callbackTestRow) Scan(dst ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dst) != len(r.values) {
		panic("unexpected callback row shape")
	}
	for i, v := range r.values {
		out := reflect.ValueOf(dst[i]).Elem()
		if v == nil {
			out.SetZero()
		} else {
			out.Set(reflect.ValueOf(v))
		}
	}
	return nil
}

type callbackTestRows struct {
	pgx.Rows
	rows  [][]any
	index int
	err   error
}

func (r *callbackTestRows) Next() bool {
	if r.index < len(r.rows) {
		r.index++
		return true
	}
	return false
}
func (r *callbackTestRows) Scan(dst ...any) error {
	return (callbackTestRow{values: r.rows[r.index-1]}).Scan(dst...)
}
func (r *callbackTestRows) Close()     {}
func (r *callbackTestRows) Err() error { return r.err }

type callbackTestCall struct {
	sql  string
	args []any
}
type callbackTestDB struct {
	t                                                                   *testing.T
	delivery                                                            callbackDeliveryRow
	attempt                                                             callbackAttemptRow
	slots                                                               []callbackSlotRow
	now                                                                 time.Time
	runtime                                                             RuntimeAuthority
	calls                                                               []callbackTestCall
	failSQL                                                             string
	zeroSQL                                                             string
	queryHook                                                           func(string)
	commitErr                                                           error
	commits, rollbacks, discards, releases, acquires, audits, gateCalls int
	denyGateAt                                                          int
	quota                                                               int
}
type callbackTestTx struct {
	pgx.Tx
	db        *callbackTestDB
	pending   []func()
	committed bool
}
type callbackTestConn struct{ tx *callbackTestTx }
type callbackTestPool struct{ db *callbackTestDB }

func (p callbackTestPool) Acquire(context.Context) (Connection, error) {
	p.db.acquires++
	return &callbackTestConn{&callbackTestTx{db: p.db}}, nil
}
func (c *callbackTestConn) BeginTx(_ context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	if o.IsoLevel != pgx.ReadCommitted {
		panic("wrong isolation")
	}
	return c.tx, nil
}
func (c *callbackTestConn) Release()                      { c.tx.db.releases++ }
func (c *callbackTestConn) Discard(context.Context) error { c.tx.db.discards++; return nil }
func (tx *callbackTestTx) Commit(ctx context.Context) error {
	tx.db.commits++
	if ctx.Err() != nil {
		panic("cancelled fake commit")
	}
	if tx.db.commitErr != nil {
		return tx.db.commitErr
	}
	for _, f := range tx.pending {
		f()
	}
	tx.committed = true
	return nil
}
func (tx *callbackTestTx) Rollback(ctx context.Context) error {
	tx.db.rollbacks++
	tx.pending = nil
	if ctx.Err() != nil {
		panic("unbounded/cancelled fake cleanup")
	}
	if tx.committed {
		return pgx.ErrTxClosed
	}
	return nil
}
func (tx *callbackTestTx) record(sql string, args []any) error {
	tx.db.calls = append(tx.db.calls, callbackTestCall{sql, append([]any(nil), args...)})
	if tx.db.queryHook != nil {
		tx.db.queryHook(sql)
	}
	if sql == tx.db.failSQL {
		return errors.New("PRIVATE raw SQL/credential must not escape")
	}
	return nil
}
func (tx *callbackTestTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if err := tx.record(sql, args); err != nil {
		return callbackTestRow{err: err}
	}
	d, a := tx.db.delivery, tx.db.attempt
	var v []any
	switch sql {
	case callbackLockTenant:
		v = []any{"tenant_cb"}
	case callbackDestinationHint, callbackLockDestination:
		v = []any{d.destination}
	case callbackReadDelivery:
		v = []any{d.destination, d.state, d.fence, d.attempts, d.maxAttempts, d.owner, d.recovery, d.lease, d.claimed, d.hard, d.expires, d.delivered, d.available, d.body, d.digest, d.lastCode, d.graph, d.eventBody}
	case callbackReadAttempt:
		v = []any{a.number, a.owner, a.recovery, a.started, a.prepared, a.finished, a.outcome, a.status, a.code}
	case callbackOldSlotExists:
		exists := false
		for _, s := range tx.db.slots {
			exists = exists || (s.delivery == args[1].(string) && s.owner == args[2].(string) && s.recovery == args[3].(string) && s.fence == args[4].(int64))
		}
		v = []any{exists}
	case callbackFinalTime:
		v = []any{tx.db.now}
	default:
		if sql != `SELECT boot_id,recovery_epoch,state,$2=ANY(roles) FROM runtime_instances WHERE instance_id=$1 FOR SHARE` {
			panic("unexpected callback SQL query")
		}
		if len(args) != 2 || args[1] != "worker" {
			panic("callback used non-worker role")
		}
		v = []any{tx.db.runtime.boot, tx.db.runtime.epoch, "active", true}
	}
	return callbackTestRow{values: v}
}
func (tx *callbackTestTx) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := tx.record(sql, args); err != nil {
		return nil, err
	}
	if sql != callbackReadSlots {
		panic("unexpected callback multirow query")
	}
	rows := [][]any{}
	for _, s := range tx.db.slots {
		if s.delivery == args[1].(string) {
			rows = append(rows, []any{s.kind, s.id, s.number, s.owner, s.delivery, s.fence, s.recovery, s.slotFence, s.lease})
		}
	}
	if len(rows) > 3 {
		rows = rows[:3]
	}
	return &callbackTestRows{rows: rows}, nil
}
func (tx *callbackTestTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := tx.record(sql, args); err != nil {
		return pgconn.CommandTag{}, err
	}
	if sql == tx.db.zeroSQL {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}
	switch sql {
	case callbackUpdateAttempt:
		tx.pending = append(tx.pending, func() {
			a := &tx.db.attempt
			a.finished = callbackTime(args[5].(time.Time))
			a.outcome = args[6].(string)
			if args[7] == nil {
				a.status = nil
			} else {
				a.status = callbackInt(args[7].(int))
			}
			a.code = callbackString(args[8].(string))
		})
	case callbackUpdateDelivery:
		tx.pending = append(tx.pending, func() {
			d := &tx.db.delivery
			d.state = args[5].(string)
			if d.state == "ready" {
				d.available = args[6].(time.Time)
			}
			if d.state == "delivered" {
				d.delivered = callbackTime(args[7].(time.Time))
			} else {
				d.delivered = nil
			}
			d.owner = nil
			d.recovery = nil
			d.lease = nil
			d.lastCode = callbackString(args[8].(string))
		})
	case callbackReleaseSlot:
		tx.pending = append(tx.pending, func() {
			for i := range tx.db.slots {
				s := &tx.db.slots[i]
				if s.kind == args[0].(string) && s.id == args[2].(string) && s.number == args[3].(int) {
					s.owner = ""
					s.delivery = ""
					s.fence = 0
					s.recovery = ""
					s.lease = time.Time{}
				}
			}
		})
	case callbackInsertAudit:
		tx.pending = append(tx.pending, func() { tx.db.audits++ })
	default:
		panic("unexpected callback DML")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}
func callbackTime(v time.Time) *time.Time { return &v }
func callbackString(v string) *string     { return &v }
func callbackInt(v int) *int              { return &v }
func callbackFixture(t *testing.T) (*CallbackCompleter, *callbackTestDB, CallbackCompletion) {
	t.Helper()
	now := time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)
	runtime, err := RuntimeAuthorityFromTrusted("worker_cb", "boot_cb", "recovery_cb")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"eventId":"callback_cb","status":"succeeded"}`)
	digest := sha256.Sum256(body)
	d := callbackDeliveryRow{destination: "destination_cb", state: "leased", fence: 9, attempts: 1, maxAttempts: 4, owner: callbackString("worker_cb"), recovery: callbackString("recovery_cb"), lease: callbackTime(now.Add(5 * time.Second)), claimed: callbackTime(now.Add(-3 * time.Second)), hard: callbackTime(now.Add(7 * time.Second)), expires: now.Add(time.Hour), available: now.Add(-time.Minute), body: body, digest: hex.EncodeToString(digest[:]), graph: true, eventBody: append([]byte(nil), body...)}
	a := callbackAttemptRow{number: 1, owner: "worker_cb", recovery: "recovery_cb", started: now.Add(-2 * time.Second), prepared: callbackTime(now.Add(-time.Second)), outcome: "in_progress"}
	db := &callbackTestDB{t: t, delivery: d, attempt: a, now: now, runtime: runtime, quota: 90112}
	db.slots = []callbackSlotRow{{"tenant", "tenant_cb", 2, "worker_cb", "delivery_cb", 9, "recovery_cb", 17, *d.lease}, {"destination", "destination_cb", 0, "worker_cb", "delivery_cb", 9, "recovery_cb", 21, *d.lease}}
	runner, err := NewRunner(callbackTestPool{db}, Timeouts{time.Second, time.Second, 3 * time.Second, time.Second, time.Second})
	if err != nil {
		t.Fatal(err)
	}
	completer, err := NewCallbackCompleter(runner, runtime, func(context.Context) error {
		db.gateCalls++
		if db.denyGateAt == db.gateCalls {
			return errors.New("PRIVATE generation unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := CallbackCompletion{Handle: CallbackCompletionHandle{"tenant_cb", "delivery_cb", "worker_cb", "recovery_cb", 9, 1, 4}, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}
	return completer, db, c
}
func callbackDML(db *callbackTestDB) []callbackTestCall {
	out := []callbackTestCall{}
	for _, c := range db.calls {
		if strings.HasPrefix(c.sql, "UPDATE ") || strings.HasPrefix(c.sql, "INSERT ") {
			out = append(out, c)
		}
	}
	return out
}
func callbackRetry(c CallbackCompletion, code string) CallbackCompletion {
	c.Outcome = "retryable_failure"
	c.State = "ready"
	c.Code = code
	c.Status = 0
	c.NextAt = time.Now().Add(time.Hour)
	return c
}

func TestCallbackCompleteAtomicShape(t *testing.T) {
	s, db, c := callbackFixture(t)
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if db.delivery.state != "delivered" || db.attempt.outcome != "delivered" || db.attempt.status == nil || *db.attempt.status != 204 || db.audits != 1 || db.quota != 90112 || db.delivery.fence != 9 || db.delivery.attempts != 1 {
		t.Fatal("wrong fake committed graph")
	}
	if db.delivery.owner != nil || db.delivery.recovery != nil || db.delivery.lease != nil || db.slots[0].delivery != "" || db.slots[1].delivery != "" || db.slots[0].slotFence != 17 || db.slots[1].slotFence != 21 {
		t.Fatal("lease/slot cleanup")
	}
	got := callbackDML(db)
	if len(got) != 5 {
		t.Fatal("DML count", len(got))
	}
	if got[0].sql != callbackUpdateAttempt || got[1].sql != callbackUpdateDelivery || got[2].sql != callbackReleaseSlot || got[3].sql != callbackReleaseSlot || got[4].sql != callbackInsertAudit {
		t.Fatal("write order")
	}
	if got[2].args[0] != "tenant" || got[2].args[8] != int64(17) || got[3].args[0] != "destination" || got[3].args[8] != int64(21) {
		t.Fatal("slot CAS lost exact fences")
	}
	if db.commits != 1 || db.discards != 0 || db.releases != 1 || db.gateCalls != 2 {
		t.Fatal("runner lifecycle")
	}
	// No read/write request to commands, counters, subscriptions or reservation mutation.
	for _, call := range got {
		if strings.Contains(call.sql, "charged_") || strings.Contains(call.sql, "commands") || strings.Contains(call.sql, "admission_reservations") {
			t.Fatal("scope expanded")
		}
	}
}
func TestCallbackCompleteStatusNullAndRevocation(t *testing.T) {
	for _, mode := range []string{"retry", "cancelled", "permanent"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			db.attempt.prepared = nil
			switch mode {
			case "retry":
				c = callbackRetry(c, "DNS_UNAVAILABLE")
			case "cancelled":
				c.Outcome = "permanent_failure"
				c.State = "cancelled"
				c.Code = "AUTHORITY_DENIED"
				c.Status = 0
			case "permanent":
				c.Outcome = "permanent_failure"
				c.State = "dead_letter"
				c.Code = "DNS_ANSWER_DENIED"
				c.Status = 0
			}
			if err := s.Complete(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if callbackDML(db)[0].args[7] != nil || db.attempt.status != nil {
				t.Fatal("zero status was not SQL NULL")
			}
			if mode == "retry" && !db.delivery.available.Equal(db.now.Add(time.Second)) {
				t.Fatal("trusted local NextAt instead of PG now")
			}
			if mode == "cancelled" && (db.attempt.outcome != "permanent_failure" || db.delivery.state != "cancelled") {
				t.Fatal("invalid SQL cancellation outcome")
			}
			// Complete has no app/device permission recheck or scope-enabled predicate.
			for _, q := range db.calls {
				if strings.Contains(q.sql, "FROM developer_apps") || strings.Contains(q.sql, "AND enabled") {
					t.Fatal("blocked existing attempt on scheduling revocation")
				}
			}
		})
	}
}
func TestCallbackCompleteFiniteMatrix(t *testing.T) {
	s, db, base := callbackFixture(t)
	bad := []CallbackCompletion{}
	for _, change := range []func(*CallbackCompletion){
		func(c *CallbackCompletion) { c.Outcome = "cancelled" }, func(c *CallbackCompletion) { c.Code = "arbitrary error\nPRIVATE" }, func(c *CallbackCompletion) { c.Status = 0 }, func(c *CallbackCompletion) { c.Status = 600 }, func(c *CallbackCompletion) { c.State = "ready" }, func(c *CallbackCompletion) { c.NextAt = time.Now() }, func(c *CallbackCompletion) { c.Handle.Attempt = 0 }, func(c *CallbackCompletion) { c.Handle.MaxAttempts = 5 }, func(c *CallbackCompletion) { c.Handle.Fence = 0 },
	} {
		c := base
		change(&c)
		bad = append(bad, c)
	}
	for _, c := range bad {
		if err := s.Complete(context.Background(), c); !errors.Is(err, ErrCallbackCompletionInvalid) {
			t.Fatal("invalid intent", err)
		}
	}
	if db.acquires != 0 {
		t.Fatal("invalid input reached DB")
	}
	c := base
	c.Handle.Recovery = "other"
	if err := s.Complete(context.Background(), c); !errors.Is(err, ErrCallbackCompletionStale) {
		t.Fatal(err)
	}
	if _, err := NewCallbackCompleter(nil, s.runtime, s.gate); err == nil {
		t.Fatal("missing runner")
	}
	if _, err := NewCallbackCompleter(s.runner, RuntimeAuthority{}, s.gate); err == nil {
		t.Fatal("untrusted runtime")
	}
	if _, err := NewCallbackCompleter(s.runner, s.runtime, nil); err == nil {
		t.Fatal("missing gate")
	}
}
func TestCallbackCompleteRequiresPreparedMarker(t *testing.T) {
	cases := []CallbackCompletion{
		{Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204},
		{Outcome: "delivery_unknown", State: "ready", Code: "TRANSPORT_OUTCOME_UNKNOWN", NextAt: time.Now()},
		{Outcome: "delivery_unknown", State: "ready", Code: "HTTP_STATUS_INVALID", NextAt: time.Now()},
		{Outcome: "retryable_failure", State: "ready", Code: "HTTP_RETRYABLE", Status: 503, NextAt: time.Now()},
		{Outcome: "permanent_failure", State: "dead_letter", Code: "REDIRECT_DENIED", Status: 302},
		{Outcome: "retryable_failure", State: "ready", Code: "PERMIT_EXPIRED", NextAt: time.Now()},
		{Outcome: "permanent_failure", State: "dead_letter", Code: "SIGNATURE_INPUT_INVALID"},
		{Outcome: "permanent_failure", State: "dead_letter", Code: "TRANSPORT_POLICY_DENIED"},
	}
	for _, c := range cases {
		t.Run(c.Code, func(t *testing.T) {
			s, db, base := callbackFixture(t)
			c.Handle = base.Handle
			db.attempt.prepared = nil
			if err := s.Complete(context.Background(), c); !errors.Is(err, ErrCallbackCompletionIntegrity) {
				t.Fatal(err)
			}
			if len(callbackDML(db)) != 0 || db.commits != 0 {
				t.Fatal("unprepared write")
			}
		})
	}
}
func TestCallbackCompleteFinalAuthorityAndClock(t *testing.T) {
	for _, mode := range []string{"post_lock_gate", "lease_equal", "hard_equal", "expiry_equal", "marker_future", "marker_before_start", "start_before_claim", "wrong_runtime"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			switch mode {
			case "post_lock_gate":
				db.denyGateAt = 2
			case "lease_equal":
				db.queryHook = func(sql string) {
					if sql == callbackReadSlots {
						db.now = *db.delivery.lease
					}
				}
			case "hard_equal":
				db.delivery.lease = callbackTime(*db.delivery.hard)
				for i := range db.slots {
					db.slots[i].lease = *db.delivery.lease
				}
				db.now = *db.delivery.hard
			case "expiry_equal":
				db.delivery.hard = callbackTime(db.delivery.expires)
				db.delivery.lease = callbackTime(db.delivery.expires)
				db.now = db.delivery.expires
			case "marker_future":
				db.attempt.prepared = callbackTime(db.now.Add(time.Second))
			case "marker_before_start":
				db.attempt.prepared = callbackTime(db.attempt.started.Add(-time.Nanosecond))
			case "start_before_claim":
				db.attempt.started = db.delivery.claimed.Add(-time.Second)
			case "wrong_runtime":
				db.runtime.boot = "wrong_boot"
			}
			if err := s.Complete(context.Background(), c); err == nil {
				t.Fatal("authority/time accepted")
			}
			if len(callbackDML(db)) != 0 || db.commits != 0 || db.slots[0].delivery == "" {
				t.Fatal("failed authority mutated state")
			}
		})
	}
}
func TestCallbackCompleteExactSlots(t *testing.T) {
	for _, mode := range []string{"missing", "extra", "owner", "generation", "delivery_fence", "slot_fence", "scope", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			switch mode {
			case "missing":
				db.slots = db.slots[:1]
			case "extra":
				db.slots = append(db.slots, db.slots[1])
			case "owner":
				db.slots[1].owner = "successor"
			case "generation":
				db.slots[1].recovery = "other"
			case "delivery_fence":
				db.slots[1].fence++
			case "slot_fence":
				db.slots[1].slotFence = 0
			case "scope":
				db.slots[1].id = "other"
			case "deadline":
				db.slots[1].lease = db.slots[1].lease.Add(time.Second)
			}
			if err := s.Complete(context.Background(), c); !errors.Is(err, ErrCallbackCompletionIntegrity) {
				t.Fatal(err)
			}
			if len(callbackDML(db)) != 0 || db.commits != 0 {
				t.Fatal("malformed slots wrote")
			}
		})
	}
}
func TestCallbackCompleteRetryStateUsesPG(t *testing.T) {
	for attempt := 1; attempt <= 4; attempt++ {
		s, db, c := callbackFixture(t)
		db.delivery.attempts = attempt
		db.attempt.number = attempt
		c.Handle.Attempt = attempt
		c = callbackRetry(c, "DNS_UNAVAILABLE")
		if attempt == 4 {
			c.State = "dead_letter"
			c.NextAt = time.Time{}
		}
		if err := s.Complete(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if attempt < 4 && !db.delivery.available.Equal(db.now.Add(time.Second<<(attempt-1))) {
			t.Fatal("retry schedule")
		}
		if attempt == 4 && db.delivery.state != "dead_letter" {
			t.Fatal("attempt cap")
		}
	}
	for _, mode := range []string{"premature_deadletter", "ready_after_horizon"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			c = callbackRetry(c, "DNS_UNAVAILABLE")
			if mode == "premature_deadletter" {
				c.State = "dead_letter"
				c.NextAt = time.Time{}
			} else {
				db.delivery.expires = db.now.Add(time.Second)
				db.delivery.hard = callbackTime(db.now.Add(500 * time.Millisecond))
				db.delivery.lease = callbackTime(*db.delivery.hard)
				for i := range db.slots {
					db.slots[i].lease = *db.delivery.lease
				}
			}
			if err := s.Complete(context.Background(), c); !errors.Is(err, ErrCallbackCompletionStale) {
				t.Fatal(err)
			}
			if len(callbackDML(db)) != 0 {
				t.Fatal("silently changed proposed state")
			}
		})
	}
}
func TestCallbackCompleteDuplicateReadOnlyReassignedSlots(t *testing.T) {
	s, db, c := callbackFixture(t)
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	db.slots[0] = callbackSlotRow{"tenant", "tenant_cb", 2, "other_worker", "other_delivery", 40, "other_generation", 18, db.now.Add(time.Hour)}
	db.slots[1] = callbackSlotRow{"destination", "destination_cb", 0, "other_worker", "other_delivery", 40, "other_generation", 22, db.now.Add(time.Hour)}
	before := append([]callbackSlotRow{}, db.slots...)
	db.calls = nil
	db.now = db.now.Add(time.Hour)
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(callbackDML(db)) != 0 || db.audits != 1 || !reflect.DeepEqual(before, db.slots) {
		t.Fatal("duplicate touched reassigned slots")
	}
	for _, call := range db.calls {
		if call.sql == callbackReadSlots {
			t.Fatal("duplicate locked other owner slots")
		}
	}
}
func TestCallbackCompleteDuplicateConflictAndSuccessor(t *testing.T) {
	for _, mode := range []string{"different_status", "different_outcome", "successor", "old_slot", "delivery_code", "bad_finished"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			if err := s.Complete(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			db.calls = nil
			switch mode {
			case "different_status":
				c.Status = 200
			case "different_outcome":
				c.Outcome = "permanent_failure"
				c.State = "dead_letter"
				c.Code = "HTTP_PERMANENT"
				c.Status = 400
			case "successor":
				db.delivery.fence++
				db.delivery.owner = callbackString("successor")
			case "old_slot":
				db.slots[0].owner = c.Handle.Owner
				db.slots[0].delivery = c.Handle.DeliveryID
				db.slots[0].recovery = c.Handle.Recovery
				db.slots[0].fence = c.Handle.Fence
			case "delivery_code":
				db.delivery.lastCode = callbackString("other")
			case "bad_finished":
				db.attempt.finished = callbackTime(db.now.Add(time.Second))
			}
			if err := s.Complete(context.Background(), c); err == nil {
				t.Fatal("conflicting duplicate succeeded")
			}
			if len(callbackDML(db)) != 0 || db.audits != 1 {
				t.Fatal("duplicate overwrote graph")
			}
		})
	}
}
func TestCallbackCompleteRollbackAtEveryDML(t *testing.T) {
	for _, sql := range []string{callbackUpdateAttempt, callbackUpdateDelivery, callbackReleaseSlot, callbackInsertAudit} {
		for _, zero := range []bool{false, true} {
			s, db, c := callbackFixture(t)
			if zero {
				db.zeroSQL = sql
			} else {
				db.failSQL = sql
			}
			err := s.Complete(context.Background(), c)
			if err == nil || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("missing/redaction error")
			}
			if db.commits != 0 || db.delivery.state != "leased" || db.attempt.finished != nil || db.audits != 0 || db.slots[0].delivery != "delivery_cb" || db.quota != 90112 {
				t.Fatal("fake rollback graph changed")
			}
		}
	}
}
func TestCallbackCompleteCommitUnknownNoRetry(t *testing.T) {
	s, db, c := callbackFixture(t)
	db.commitErr = errors.New("PRIVATE lost commit response")
	err := s.Complete(context.Background(), c)
	if !errors.Is(err, ErrCallbackCompletionUnknown) || strings.Contains(err.Error(), "PRIVATE") || db.commits != 1 || db.discards != 1 || db.releases != 1 || db.acquires != 1 {
		t.Fatal("commit uncertainty lifecycle", err)
	}
	if len(callbackDML(db)) != 5 {
		t.Fatal("inline retry")
	}
}
func TestCallbackCompleteGraphAndReadFailures(t *testing.T) {
	for _, mode := range []string{"reservation", "event_body", "digest", "too_large", "wrong_max", "wrong_attempt", "wrong_owner", "marker_corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			switch mode {
			case "reservation":
				db.delivery.graph = false
			case "event_body":
				db.delivery.eventBody = []byte(`{}`)
			case "digest":
				db.delivery.digest = strings.Repeat("0", 64)
			case "too_large":
				db.delivery.body = make([]byte, 4097)
			case "wrong_max":
				db.delivery.maxAttempts = 3
			case "wrong_attempt":
				db.attempt.number = 2
			case "wrong_owner":
				db.attempt.owner = "other"
			case "marker_corrupt":
				db.attempt.status = callbackInt(204)
			}
			if err := s.Complete(context.Background(), c); err == nil {
				t.Fatal("bad graph accepted")
			}
			if len(callbackDML(db)) != 0 {
				t.Fatal("bad graph wrote")
			}
		})
	}
	for _, sql := range []string{callbackLockTenant, callbackDestinationHint, callbackLockDestination, callbackReadDelivery, callbackReadAttempt, callbackReadSlots, callbackFinalTime} {
		s, db, c := callbackFixture(t)
		db.failSQL = sql
		if err := s.Complete(context.Background(), c); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("read error leaked")
		}
		if len(callbackDML(db)) != 0 {
			t.Fatal("read failure wrote")
		}
	}
}
func TestCallbackCompleteDuplicateReadyKeepsOriginalSchedule(t *testing.T) {
	s, db, c := callbackFixture(t)
	c = callbackRetry(c, "DNS_UNAVAILABLE")
	db.attempt.prepared = nil
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	original := db.delivery.available
	db.now = db.now.Add(30 * time.Second)
	db.calls = nil
	c.NextAt = db.now.Add(time.Hour)
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !db.delivery.available.Equal(original) || len(callbackDML(db)) != 0 {
		t.Fatal("duplicate extended retry")
	}
}
