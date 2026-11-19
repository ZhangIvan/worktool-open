package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Test-only final-clock control. All rows, locks, SQL, role checks and commits
// use PostgreSQL. Only the exact final clock query can be replaced with a typed
// PG timestamp parameter. This is NOT a natural timeout or server-clock test.
// The natural-lock case leaves now zero and executes clock_timestamp unchanged.
type retryPGPool struct {
	base        pg.Pool
	now         time.Time
	clocks, dml atomic.Int64
}

func (p *retryPGPool) Acquire(ctx context.Context) (pg.Connection, error) {
	c, e := p.base.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	return retryPGConn{c, p}, nil
}

type retryPGConn struct {
	pg.Connection
	p *retryPGPool
}

func (c retryPGConn) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, e := c.Connection.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &retryPGTx{tx, c.p}, nil
}

type retryPGTx struct {
	pgx.Tx
	p *retryPGPool
}

func (tx *retryPGTx) QueryRow(ctx context.Context, q string, a ...any) pgx.Row {
	if q == "SELECT clock_timestamp()" {
		tx.p.clocks.Add(1)
		if !tx.p.now.IsZero() {
			return tx.Tx.QueryRow(ctx, "SELECT $1::timestamptz", tx.p.now)
		}
	}
	return tx.Tx.QueryRow(ctx, q, a...)
}
func (tx *retryPGTx) Exec(ctx context.Context, q string, a ...any) (pgconn.CommandTag, error) {
	upper := strings.ToUpper(strings.TrimSpace(q))
	if strings.HasPrefix(upper, "UPDATE ") || strings.HasPrefix(upper, "INSERT ") || strings.HasPrefix(upper, "DELETE ") {
		tx.p.dml.Add(1)
	}
	return tx.Tx.Exec(ctx, q, a...)
}
func retryPGStore(t *testing.T, f *gwFixture, policy cb.Policy, p pg.Pool) *pg.CallbackStore {
	t.Helper()
	r, e := pg.NewRunner(p, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 3 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal("retry runner")
	}
	a, e := pg.RuntimeAuthorityFromTrusted("worker_demo", "boot_worker", "recovery_demo")
	if e != nil {
		t.Fatal("retry authority")
	}
	s, e := pg.NewCallbackStore(r, a, func(context.Context) error { return nil }, []cb.Policy{policy})
	if e != nil {
		t.Fatal("retry store")
	}
	return s
}
func retryPGFixture(t *testing.T, attempt int, ttl time.Duration) (*gwFixture, cb.Policy, cb.RetryCompletionIntent, time.Time) {
	t.Helper()
	f := newGWWithStoreConstructor(t, func(r *pg.Runner, g func(context.Context) error, a pg.RuntimeAuthority) (*pg.GatewayStore, error) {
		return pg.NewSyntheticCallbackGatewayStore(r, g, a, ttl)
	})
	s, policy := callbackSetup(t, f)
	callbackCompletedCommand(t, f)
	// Quiesce the gateway before any whole-graph snapshot. Otherwise background
	// session cleanup could look like settlement/observation business DML.
	// Retain storage on failed join so the isolated resource owns final cleanup.
	join, cancel := context.WithTimeout(f.ctx, 7*time.Second)
	defer cancel()
	if err := f.controller.Shutdown(join); err != nil {
		f.retainStorage()
		t.Fatal("gateway join failed; storage retained for isolated resource cleanup")
	}
	if f.controller.LiveCount() != 0 || f.controller.CleanupErrors() != 0 {
		f.retainStorage()
		t.Fatal("gateway did not finish cleanly; storage retained for isolated resource cleanup")
	}
	d, ok, e := s.Claim(f.ctx)
	if e != nil || !ok {
		t.Fatal("retry claim")
	}
	if _, e = s.Prepare(f.ctx, callbackPrepared(d)); e != nil {
		t.Fatal("retry prepare")
	}
	// Synthetic attempt index, not a claim that earlier transports ran. Preserve
	// immutable expiry and graph; no trigger/permission is disabled.
	execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET attempt_count=$3 WHERE tenant=$1 AND delivery_id=$2`, d.Handle.Tenant, d.Handle.DeliveryID, attempt)
	execOwner(t, f.owner, f.ctx, `UPDATE callback_attempts SET attempt_no=$3 WHERE tenant=$1 AND delivery_id=$2`, d.Handle.Tenant, d.Handle.DeliveryID, attempt)
	var expiry time.Time
	if e = f.owner.QueryRow(f.ctx, `SELECT expires_at FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2`, d.Handle.Tenant, d.Handle.DeliveryID).Scan(&expiry); e != nil {
		t.Fatal("retry expiry")
	}
	if f.count(t, `SELECT count(*) FROM callback_outbox WHERE lease_until=expires_at AND hard_deadline_at=expires_at`) != 1 || f.count(t, `SELECT count(*) FROM scope_slots s JOIN callback_outbox o ON s.tenant=o.tenant AND s.delivery_id=o.delivery_id WHERE s.lease_until=o.expires_at`) != 2 {
		t.Fatal("original claim authority was not clamped to immutable expiry")
	}
	d.Handle.Attempt = attempt
	return f, policy, cb.RetryCompletionIntent{Handle: d.Handle, Outcome: "retryable_failure", Code: "HTTP_RETRYABLE", Status: 503}, expiry
}
func retryPGControlledTime(t *testing.T, f *gwFixture, now time.Time) {
	t.Helper()
	if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.send_prepared_at<=$1 AND a.started_at<=a.send_prepared_at AND $1<o.lease_until AND $1<o.hard_deadline_at AND $1<o.expires_at`, now) != 1 {
		t.Fatal("controlled time outside original prepared authority")
	}
}
func retryPGSnapshot(t *testing.T, f *gwFixture) string {
	t.Helper()
	var v string
	e := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(o)) ORDER BY delivery_id) FROM callback_outbox o),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(a)) ORDER BY delivery_id,fence) FROM callback_attempts a),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(s)) ORDER BY scope_kind,scope_id,slot_no) FROM scope_slots s),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(a)) ORDER BY id) FROM audit a),
 (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(s)) ORDER BY scope_kind,scope_id) FROM scope_limits s)
 )::text`).Scan(&v)
	if e != nil {
		t.Fatal("retry graph snapshot")
	}
	return v
}
func retryPGObserve(t *testing.T, f *gwFixture, s *pg.CallbackStore, p *retryPGPool, i cb.RetryCompletionIntent, want cb.RetryCompletionReceipt, valid bool) {
	t.Helper()
	before := retryPGSnapshot(t, f)
	dml := p.dml.Load()
	got, e := s.ObserveRetrySettlement(f.ctx, i)
	if valid {
		if e != nil || got != want {
			t.Fatal("original retry receipt not proven")
		}
	} else if e == nil || got != (cb.RetryCompletionReceipt{}) {
		t.Fatal("unproven retry receipt escaped")
	}
	if p.dml.Load() != dml || retryPGSnapshot(t, f) != before {
		t.Fatal("retry observation mutated business graph")
	}
}
func callbackRetryBoundaryIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for attempt := 1; attempt <= 4; attempt++ {
		for _, delta := range []time.Duration{-time.Microsecond, 0, time.Microsecond} {
			name := fmt.Sprintf("retry_clock_attempt_%d_delta_%d", attempt, delta.Microseconds())
			t.Run(name, func(t *testing.T) {
				f, policy, i, expiry := retryPGFixture(t, attempt, 10*time.Second)
				delay := time.Second << uint(attempt-1)
				now := expiry.Add(-delay - delta)
				p := &retryPGPool{base: pg.PoolAdapter{Pool: f.pool}, now: now}
				if !p.now.IsZero() {
					retryPGControlledTime(t, f, p.now)
				}
				s := retryPGStore(t, f, policy, p)
				got, e := s.SettleRetry(f.ctx, i)
				state := "ready"
				if attempt == 4 || delta <= 0 {
					state = "dead_letter"
				}
				if e != nil || got.Intent != i || got.State != state || p.clocks.Load() != 1 || p.dml.Load() != 5 {
					t.Fatal("controlled final-clock settlement")
				}
				if state == "ready" {
					if !got.NextAt.Equal(now.Add(delay)) {
						t.Fatal("retry schedule")
					}
				} else if !got.NextAt.IsZero() {
					t.Fatal("terminal schedule")
				}
				if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.finished_at=$1 AND a.finished_at<o.hard_deadline_at AND a.outcome=$2 AND o.state=$3 AND o.lease_owner IS NULL AND (o.state<>'ready' OR o.available_at=a.finished_at+($4::bigint*interval '1 microsecond'))`, now, i.Outcome, state, delay.Microseconds()) != 1 || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 || f.count(t, `SELECT count(*) FROM audit WHERE action='callback.completed'`) != 1 {
					t.Fatal("retry durable graph")
				}
				p.now = expiry.Add(time.Hour)
				retryPGObserve(t, f, s, p, i, got, true)
			})
		}
	}
}
func callbackRetryObservationIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, mode := range []string{"confirmed", "unfinished", "missing_audit", "conflicting_audit", "missing_prepare", "wrong_schedule", "retained_slot", "successor_fence", "successor_attempt", "new_generation", "wrong_status"} {
		t.Run("retry_observe_"+mode, func(t *testing.T) {
			f, policy, i, expiry := retryPGFixture(t, 1, 10*time.Second)
			p := &retryPGPool{base: pg.PoolAdapter{Pool: f.pool}, now: expiry.Add(-2 * time.Second)}
			if !p.now.IsZero() {
				retryPGControlledTime(t, f, p.now)
			}
			s := retryPGStore(t, f, policy, p)
			var receipt cb.RetryCompletionReceipt
			if mode != "unfinished" {
				var e error
				receipt, e = s.SettleRetry(f.ctx, i)
				if e != nil {
					t.Fatal("retry proof fixture")
				}
			}
			switch mode {
			case "missing_audit":
				execOwner(t, f.owner, f.ctx, `DELETE FROM audit WHERE action='callback.completed'`)
			case "conflicting_audit":
				execOwner(t, f.owner, f.ctx, `UPDATE audit SET created=created+1 WHERE action='callback.completed'`)
			case "missing_prepare":
				execOwner(t, f.owner, f.ctx, `UPDATE callback_attempts SET send_prepared_at=NULL`)
			case "wrong_schedule":
				execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET available_at=available_at+interval '1 microsecond'`)
			case "retained_slot":
				execOwner(t, f.owner, f.ctx, `UPDATE scope_slots SET owner_instance=$1,delivery_id=$2,delivery_fence=$3,recovery_epoch=$4,lease_until=$5 WHERE scope_kind='tenant' AND slot_no=0`, i.Handle.Owner, i.Handle.DeliveryID, i.Handle.Fence, i.Handle.Recovery, expiry)
			case "successor_fence":
				execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET fence=fence+1`)
			case "successor_attempt":
				execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET attempt_count=attempt_count+1`)
			case "new_generation":
				execOwner(t, f.owner, f.ctx, `UPDATE runtime_instances SET recovery_epoch='new_generation' WHERE instance_id='worker_demo'`)
			case "wrong_status":
				i.Status = 429
			}
			p.now = expiry.Add(time.Hour)
			retryPGObserve(t, f, s, p, i, receipt, mode == "confirmed")
		})
	}
}
func callbackRetryCommitIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, rollback := range []bool{false, true} {
		for _, outcome := range []string{"retryable_failure", "delivery_unknown"} {
			name := "retry_ack_commit_" + outcome
			if rollback {
				name = "retry_ack_rollback_" + outcome
			}
			t.Run(name, func(t *testing.T) {
				f, policy, i, expiry := retryPGFixture(t, 1, 10*time.Second)
				i.Outcome = outcome
				if outcome == "delivery_unknown" {
					i.Code = "TRANSPORT_OUTCOME_UNKNOWN"
					i.Status = 0
				}
				fault := &callbackCompletionFaultPool{base: pg.PoolAdapter{Pool: f.pool}, rollback: rollback}
				p := &retryPGPool{base: fault, now: expiry.Add(-2 * time.Second)}
				if !p.now.IsZero() {
					retryPGControlledTime(t, f, p.now)
				}
				s := retryPGStore(t, f, policy, p)
				got, e := s.SettleRetry(f.ctx, i)
				if !errors.Is(e, cb.ErrCommitUnknown) || errors.Is(e, cb.ErrCleanupUnconfirmed) || got != (cb.RetryCompletionReceipt{}) || !fault.injected.Load() || p.dml.Load() != 5 || p.clocks.Load() != 1 {
					t.Fatal("retry acknowledgement fault classification")
				}
				want := cb.RetryCompletionReceipt{Intent: i, State: "ready", NextAt: p.now.Add(time.Second)}
				p.now = expiry.Add(time.Hour)
				retryPGObserve(t, f, s, p, i, want, !rollback)
				expected := int64(1)
				if rollback {
					expected = 0
				}
				if f.count(t, `SELECT count(*) FROM audit WHERE action='callback.completed'`) != expected || f.count(t, `SELECT count(*) FROM callback_attempts WHERE finished_at IS NOT NULL`) != expected || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 2*(1-expected) {
					t.Fatal("retry fault durable graph")
				}
			})
		}
	}
}

// Explicit legacy predictions model the two prior disagreement directions;
// this tests concrete PG APIs, not Sender's mapper (covered in unit tests).
func callbackRetryDirectionIntegrationCases(t *testing.T) {
	for _, mode := range []string{"conservative_dead_to_ready", "advanced_ready_to_dead"} {
		t.Run("retry_direction_"+mode, func(t *testing.T) {
			f, policy, i, expiry := retryPGFixture(t, 1, 10*time.Second)
			remaining := 1050 * time.Millisecond
			legacy, want := "dead_letter", "ready"
			if mode == "advanced_ready_to_dead" {
				remaining = 950 * time.Millisecond
				legacy, want = "ready", "dead_letter"
			}
			p := &retryPGPool{base: pg.PoolAdapter{Pool: f.pool}, now: expiry.Add(-remaining)}
			if !p.now.IsZero() {
				retryPGControlledTime(t, f, p.now)
			}
			s := retryPGStore(t, f, policy, p)
			old := cb.Completion{Handle: i.Handle, Outcome: i.Outcome, Code: i.Code, Status: i.Status, State: legacy}
			if legacy == "ready" {
				old.NextAt = p.now.Add(time.Second)
			}
			before := retryPGSnapshot(t, f)
			if e := s.Complete(f.ctx, old); e == nil || p.dml.Load() != 0 || retryPGSnapshot(t, f) != before {
				t.Fatal("legacy mismatch was not strict")
			}
			got, e := s.SettleRetry(f.ctx, i)
			if e != nil || got.State != want || p.clocks.Load() != 2 || p.dml.Load() != 5 {
				t.Fatal("retry authoritative direction")
			}
		})
	}
}

// No final-clock replacement here. A real row lock holds completion across the
// retry horizon, observed through pg_blocking_pids; release is governed by PG
// clock predicates, never an assumed sleep duration. No callback transport runs.
func callbackRetryNaturalLockIntegrationCase(t *testing.T) {
	t.Run("retry_natural_lock_horizon", func(t *testing.T) {
		f, policy, i, expiry := retryPGFixture(t, 3, 6*time.Second)
		p := &retryPGPool{base: pg.PoolAdapter{Pool: f.pool}}
		if !p.now.IsZero() {
			retryPGControlledTime(t, f, p.now)
		}
		s := retryPGStore(t, f, policy, p)
		ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
		defer cancel()
		poll := func(q string, args ...any) {
			t.Helper()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var ok bool
				if e := f.owner.QueryRow(ctx, q, args...).Scan(&ok); e != nil {
					t.Fatal("natural clock predicate failed")
				}
				if ok {
					return
				}
				select {
				case <-ctx.Done():
					t.Fatal("natural clock predicate timed out")
				case <-ticker.C:
				}
			}
		}
		// Enter the wait 700ms before a four-second backoff reaches immutable expiry.
		poll(`SELECT clock_timestamp()>=$1::timestamptz-interval '4.7 seconds'`, expiry)
		held, e := f.owner.Begin(ctx)
		if e != nil {
			t.Fatal("natural holder")
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			if e := held.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
				t.Error("natural holder cleanup")
			}
		}()
		var holder int
		if e = held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder); e != nil {
			t.Fatal("natural holder pid")
		}
		if _, e = held.Exec(ctx, `SELECT 1 FROM callback_attempts WHERE tenant=$1 AND delivery_id=$2 FOR UPDATE`, i.Handle.Tenant, i.Handle.DeliveryID); e != nil {
			t.Fatal("natural attempt lock")
		}
		var ready bool
		if e = f.owner.QueryRow(ctx, `SELECT clock_timestamp()+interval '4 seconds'<$1::timestamptz`, expiry).Scan(&ready); e != nil || !ready {
			t.Fatal("natural pre-lock ready horizon missed")
		}
		type result struct {
			r cb.RetryCompletionReceipt
			e error
		}
		done := make(chan result, 1)
		go func() { r, e := s.SettleRetry(ctx, i); done <- result{r, e} }()
		// Always join the bounded call, even when a predicate/assertion fails.
		defer func() {
			cancel()
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Error("natural settlement did not join")
			}
		}()
		poll(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, holder)
		poll(`SELECT clock_timestamp()>=$1::timestamptz-interval '3.9 seconds'`, expiry)
		if e = held.Rollback(ctx); e != nil {
			t.Fatal("natural lock release")
		}
		var got result
		select {
		case got = <-done:
			done <- got
		case <-ctx.Done():
			t.Fatal("natural settlement timeout")
		}
		if got.e != nil || got.r.State != "dead_letter" || !got.r.NextAt.IsZero() || p.clocks.Load() != 1 || p.dml.Load() != 5 {
			t.Fatal("natural final clock did not settle terminal")
		}
		if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.finished_at+interval '4 seconds'>=o.expires_at AND a.finished_at<o.hard_deadline_at AND a.finished_at<o.expires_at AND o.state='dead_letter' AND o.lease_owner IS NULL`) != 1 {
			t.Fatal("natural finished clock outside expected live authority")
		}
		retryPGObserve(t, f, s, p, i, got.r, true)
		poll(`SELECT clock_timestamp()>=$1::timestamptz`, expiry)
		retryPGObserve(t, f, s, p, i, got.r, true)
	})
}

// Unmodified clock, ready schedule, then real expiry: using observation time
// instead of original finished_at would incorrectly turn this into dead_letter.
func callbackRetryNaturalReadyObservationIntegrationCase(t *testing.T) {
	t.Run("retry_natural_ready_observe_after_expiry", func(t *testing.T) {
		f, policy, i, expiry := retryPGFixture(t, 1, 6*time.Second)
		p := &retryPGPool{base: pg.PoolAdapter{Pool: f.pool}}
		if !p.now.IsZero() {
			retryPGControlledTime(t, f, p.now)
		}
		s := retryPGStore(t, f, policy, p)
		receipt, e := s.SettleRetry(f.ctx, i)
		if e != nil || receipt.State != "ready" || receipt.Intent != i || !receipt.NextAt.Before(expiry) || p.clocks.Load() != 1 || p.dml.Load() != 5 {
			t.Fatal("natural ready settlement")
		}
		if f.count(t, `SELECT count(*) FROM callback_attempts a JOIN callback_outbox o USING(tenant,delivery_id) WHERE a.finished_at+interval '1 second'=o.available_at AND o.available_at=$1 AND a.finished_at<o.hard_deadline_at AND o.available_at<o.expires_at`, receipt.NextAt) != 1 {
			t.Fatal("natural original schedule")
		}
		ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
		defer cancel()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			var expired bool
			if e = f.owner.QueryRow(ctx, `SELECT clock_timestamp()>=$1::timestamptz`, expiry).Scan(&expired); e != nil {
				t.Fatal("natural ready expiry read")
			}
			if expired {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("natural ready expiry timeout")
			case <-ticker.C:
			}
		}
		retryPGObserve(t, f, s, p, i, receipt, true)
		if p.clocks.Load() != 2 {
			t.Fatal("natural ready observation final clock")
		}
	})
}
