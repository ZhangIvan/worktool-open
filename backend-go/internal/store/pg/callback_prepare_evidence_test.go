package pg

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
)

// Actual Prepare and Runner, with scripted pgx rows. These tests do not execute
// SQL, exercise PostgreSQL constraints/locks, or open a listener or connection.
func callbackPrepareEvidenceTx(t *testing.T, now time.Time) *callbackClaimTx {
	t.Helper()
	tx := callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
	tx.steps = tx.steps[:13]
	tx.steps[0].sql, tx.steps[1].sql = callbackLockTenant, callbackLockDestination
	owner, recovery, lease := "worker_cb", "recovery_cb", now.Add(10*time.Second)
	d := tx.steps[11].values
	d[1], d[2], d[3], d[5], d[6], d[7], d[8], d[9] = "leased", int64(1), 1, &owner, &recovery, &lease, &now, &lease
	tx.steps = append(tx.steps,
		callbackClaimStep{op: "row", sql: callbackReadAttempt, args: []any{"tenant_cb", "delivery_a", int64(1)}, values: []any{1, owner, recovery, now, nil, nil, "in_progress", nil, nil}},
		callbackClaimStep{op: "rows", sql: callbackReadSlots, args: []any{"tenant_cb", "delivery_a"}, rows: [][]any{{"tenant", "tenant_cb", 0, owner, "delivery_a", int64(1), recovery, int64(3), lease}, {"destination", "destination_a", 0, owner, "delivery_a", int64(1), recovery, int64(4), lease}}},
		callbackClaimStep{op: "row", sql: callbackFinalTime, values: []any{now}},
		callbackClaimStep{op: "row", sql: "SELECT enabled AND (blocked_until", args: []any{"tenant", "tenant_cb", "tenant_cb", now}, values: []any{true}},
		callbackClaimStep{op: "row", sql: "SELECT enabled AND (blocked_until", args: []any{"destination", "tenant_cb", "destination_a", now}, values: []any{true}},
		callbackClaimStep{op: "exec", sql: "UPDATE callback_attempts SET send_prepared_at=", args: []any{"tenant_cb", "delivery_a", int64(1), owner, recovery, now, 1}},
		callbackClaimStep{op: "exec", sql: "INSERT INTO audit("})
	return tx
}

// Unlike the Claim helper, allow an unconsumed success suffix: assertions below
// count every consumed Exec, including the audit, and require the exact stop
// before that suffix. This also lets the baseline RED expose a real Fresh permit.
func callbackPrepareEvidenceStore(t *testing.T, now time.Time, txs ...*callbackClaimTx) (*CallbackStore, *callbackClaimPool) {
	t.Helper()
	pool := &callbackClaimPool{t: t, txs: txs}
	runner, err := NewRunner(pool, Timeouts{time.Second, time.Second, 3 * time.Second, time.Second, time.Second})
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
	s, err := NewCallbackStore(runner, runtime, func(context.Context) error { return nil }, policies)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	t.Cleanup(func() {
		for _, tx := range txs[:pool.next] {
			if tx.rollbacks != 1 || !tx.rollbackLive || !tx.rollbackBounded || tx.releases != 1 {
				t.Error("transaction did not receive exactly one live bounded rollback and release")
			}
		}
	})
	return s, pool
}

func callbackPrepareEvidenceInput(s *CallbackStore, tx *callbackClaimTx, now time.Time) cb.Prepared {
	return cb.Prepared{Handle: cb.Handle{Tenant: "tenant_cb", DeliveryID: "delivery_a", Owner: "worker_cb", Recovery: "recovery_cb", Fence: 1, Attempt: 1, MaxAttempts: 4}, Binding: s.policies["tenant_cb\x00destination_a"].Binding, EventID: "event_cb", Digest: tx.steps[11].values[14].(string), IP: netip.MustParseAddr("93.184.216.34"), Deadline: now.Add(2 * time.Second)}
}

func callbackPrepareEvidenceWrites(tx *callbackClaimTx) int {
	writes := 0
	for _, step := range tx.steps[:tx.next] {
		if step.op == "exec" {
			writes++
		}
	}
	return writes
}

var callbackPrepareEvidenceFaults = []string{
	"missing_claim", "zero_start", "start_before_claim", "start_after_final_clock", "clock_regression_after_claim",
	"status_present", "zero_status_present", "error_present", "empty_error_present", "leased_delivered",
	"negative_db_time", "unsafe_db_time", "lease_after_hard", "hard_after_expiry", "hard_before_claim",
}

func callbackPrepareEvidenceFault(t *testing.T, tx *callbackClaimTx, mode string, now time.Time) {
	t.Helper()
	d, a := tx.steps[11].values, tx.steps[13].values
	switch mode {
	case "missing_claim":
		d[8] = nil
	case "zero_start":
		a[3] = time.Time{}
	case "start_before_claim":
		a[3] = now.Add(-time.Microsecond)
	case "start_after_final_clock":
		a[3] = now.Add(time.Microsecond)
	case "clock_regression_after_claim":
		tx.steps[15].values[0] = now.Add(-time.Microsecond)
	case "status_present", "zero_status_present":
		status := 204
		if mode == "zero_status_present" {
			status = 0
		}
		a[7] = &status
	case "error_present":
		a[8] = callbackString("PRIVATE residual failure")
	case "empty_error_present":
		a[8] = callbackString("")
	case "leased_delivered":
		d[11] = callbackTime(now)
	case "negative_db_time":
		tx.steps[15].values[0] = time.UnixMilli(-1)
	case "unsafe_db_time":
		tx.steps[15].values[0] = time.UnixMilli(maxSafeInteger + 1)
	case "lease_after_hard":
		d[9] = callbackTime(now.Add(5 * time.Second))
	case "hard_after_expiry":
		d[9] = callbackTime(now.Add(2 * time.Hour))
	case "hard_before_claim":
		d[9] = callbackTime(now.Add(-time.Microsecond))
	default:
		t.Fatal("unknown evidence fault")
	}
	// If the old code advances, let it reach its actual marker/audit and permit
	// rather than failing only because the expected scripted clock changed.
	decision := tx.steps[15].values[0].(time.Time)
	tx.steps[16].args[3], tx.steps[17].args[3], tx.steps[18].args[5] = decision, decision, decision
}

func TestCallbackPrepareRejectsContradictoryEvidenceBeforeMutation(t *testing.T) {
	for _, mode := range callbackPrepareEvidenceFaults {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			if mode == "negative_db_time" {
				// Keep chronology coherent to isolate the lower clock-range guard.
				now = time.UnixMilli(-1)
			}
			tx := callbackPrepareEvidenceTx(t, now)
			callbackPrepareEvidenceFault(t, tx, mode, now)
			s, pool := callbackPrepareEvidenceStore(t, now, tx)
			permit, err := s.Prepare(context.Background(), callbackPrepareEvidenceInput(s, tx, now))
			writes := callbackPrepareEvidenceWrites(tx)
			if !errors.Is(err, cb.ErrStale) || permit != (cb.Permit{}) || pool.next != 1 || tx.next != 16 || writes != 0 || tx.commits != 0 || tx.discards != 0 {
				t.Fatalf("evidence rejection: err=%v fresh=%v steps=%d writes=%d commits=%d", err, permit.Fresh, tx.next, writes, tx.commits)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("stored private evidence leaked")
			}
		})
	}
}

func TestCallbackPrepareValidEvidenceBoundaries(t *testing.T) {
	for _, mode := range []string{"claim_start_clock_equal", "ordered_start", "lease_hard_expiry_equal", "deadline_equals_mapped", "prior_attempt_error_retained", "epoch_clock_zero", "safe_clock_maximum"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			if mode == "epoch_clock_zero" {
				now = time.UnixMilli(0)
			} else if mode == "safe_clock_maximum" {
				now = time.UnixMilli(maxSafeInteger)
			}
			tx := callbackPrepareEvidenceTx(t, now)
			switch mode {
			case "ordered_start":
				tx.steps[11].values[8] = callbackTime(now.Add(-2 * time.Second))
				tx.steps[13].values[3] = now.Add(-time.Second)
			case "lease_hard_expiry_equal":
				tx.steps[11].values[10] = now.Add(10 * time.Second)
			case "deadline_equals_mapped":
				lease := now.Add(2100 * time.Millisecond)
				tx.steps[11].values[7] = &lease
				for i := range tx.steps[14].rows {
					tx.steps[14].rows[i][8] = lease
				}
			case "prior_attempt_error_retained":
				tx.steps[11].values[15] = callbackString("HTTP_RETRYABLE")
			}
			s, pool := callbackPrepareEvidenceStore(t, now, tx)
			p := callbackPrepareEvidenceInput(s, tx, now)
			permit, err := s.Prepare(context.Background(), p)
			if err != nil || permit != (cb.Permit{Prepared: p, Fresh: true}) || pool.next != 1 || tx.next != 20 || callbackPrepareEvidenceWrites(tx) != 2 || tx.commits != 1 || tx.discards != 0 {
				t.Fatalf("valid evidence did not prepare exactly once: fresh=%v err=%v", permit.Fresh, err)
			}
		})
	}
}

func TestCallbackPrepareEvidenceGuardPreservesAuthorityAndDeadlines(t *testing.T) {
	for _, mode := range []string{"authority_revoked", "authority_expires_at_clock", "generation_lost_after_clock", "lease_expires_at_clock", "deadline_after_mapped", "scope_disabled", "already_prepared"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			tx := callbackPrepareEvidenceTx(t, now)
			s, pool := callbackPrepareEvidenceStore(t, now, tx)
			want, steps := cb.ErrStale, 16
			switch mode {
			case "authority_revoked":
				tx.steps[7].values[0] = false
				want = cb.ErrDenied
			case "authority_expires_at_clock":
				tx.steps[4].values[2] = now.UnixMilli()
				want = cb.ErrDenied
			case "generation_lost_after_clock":
				calls := 0
				s.gate = func(context.Context) error {
					calls++
					if calls == 2 {
						return cb.ErrStale
					}
					return nil
				}
			case "lease_expires_at_clock", "deadline_after_mapped":
				lease := now
				if mode == "deadline_after_mapped" {
					lease = now.Add(2 * time.Second)
				}
				tx.steps[11].values[7] = &lease
				for i := range tx.steps[14].rows {
					tx.steps[14].rows[i][8] = lease
				}
			case "scope_disabled":
				tx.steps[16].values[0] = false
				steps = 17
			case "already_prepared":
				tx.steps[13].values[4] = &now
				steps = 14
			}
			permit, err := s.Prepare(context.Background(), callbackPrepareEvidenceInput(s, tx, now))
			if !errors.Is(err, want) || permit != (cb.Permit{}) || pool.next != 1 || tx.next != steps || callbackPrepareEvidenceWrites(tx) != 0 || tx.commits != 0 || tx.discards != 0 {
				t.Fatalf("existing refusal changed: err=%v fresh=%v steps=%d", err, permit.Fresh, tx.next)
			}
		})
	}
}

func TestCallbackPrepareEvidenceRejectionCleanup(t *testing.T) {
	for _, mode := range []string{"already_closed", "rollback_failure", "rollback_and_discard_failure"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			tx := callbackPrepareEvidenceTx(t, now)
			callbackPrepareEvidenceFault(t, tx, "error_present", now)
			tx.rollbackErr = pgx.ErrTxClosed
			want, discards := cb.ErrStale, 0
			if mode != "already_closed" {
				tx.rollbackErr = errors.New("PRIVATE rollback failure")
				want, discards = cb.ErrCleanupUnconfirmed, 1
			}
			if mode == "rollback_and_discard_failure" {
				tx.discardErr = errors.New("PRIVATE discard failure")
			}
			s, pool := callbackPrepareEvidenceStore(t, now, tx)
			permit, err := s.Prepare(context.Background(), callbackPrepareEvidenceInput(s, tx, now))
			if !errors.Is(err, want) || permit != (cb.Permit{}) || pool.next != 1 || tx.next != 16 || callbackPrepareEvidenceWrites(tx) != 0 || tx.commits != 0 || tx.discards != discards || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("rejection cleanup changed or granted permission: %v", err)
			}
		})
	}
}

// Claim and Prepare are the real store/Runner methods. Complete is only a
// counting stub: the positive control proves Sender reaches transport, not a
// durable completion. All other adapters are in-memory and perform no I/O.
type callbackPrepareSenderStore struct {
	*CallbackStore
	completions int
}

func (s *callbackPrepareSenderStore) Complete(context.Context, cb.Completion) error {
	s.completions++
	return nil
}

type callbackPrepareSenderAdapters struct {
	t                  *testing.T
	now                time.Time
	key                []byte
	dnsCalls, keyCalls int
	transportCalls     int
	body               []byte
}

func (a *callbackPrepareSenderAdapters) Now() time.Time { return a.now }
func (a *callbackPrepareSenderAdapters) Deadline(ctx context.Context, until time.Time) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, until.Sub(a.now))
}
func (*callbackPrepareSenderAdapters) Check(context.Context, cb.Handle) error { return nil }
func (a *callbackPrepareSenderAdapters) Resolve(_ context.Context, binding cb.Binding) (cb.Key, error) {
	a.keyCalls++
	a.key = []byte("0123456789abcdef0123456789abcdef")
	return cb.Key{Binding: binding, Bytes: a.key, ExpiresAt: a.now.Add(time.Hour)}, nil
}

// Go cannot overload Resolve for DNS and keys, so keep a separate DNS adapter.
type callbackPrepareSenderDNS struct {
	a *callbackPrepareSenderAdapters
}

func (d callbackPrepareSenderDNS) Resolve(_ context.Context, host string) ([]netip.Addr, error) {
	if host != "callbacks.example.com" {
		d.a.t.Fatal("unexpected synthetic DNS name")
	}
	d.a.dnsCalls++
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func (a *callbackPrepareSenderAdapters) Send(_ context.Context, req cb.Request) (cb.Response, error) {
	a.transportCalls++
	if req.Method != "POST" || req.URL != "https://callbacks.example.com/result" || req.Host != "callbacks.example.com" || req.IP != netip.MustParseAddr("93.184.216.34") || !reflect.DeepEqual(req.Body, a.body) || req.Signature == "" {
		a.t.Fatal("unexpected synthetic transport request")
	}
	return cb.Response{Status: 204}, nil
}

func TestCallbackPrepareEvidenceSenderStopsBeforeTransport(t *testing.T) {
	modes := append([]string{"valid_claim_prepare_control"}, callbackPrepareEvidenceFaults...)
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
			discovery := callbackClaimDiscoveryTx(t)
			claim := callbackClaimReadyTx(t, "destination_a", "delivery_a", now)
			prepare := callbackPrepareEvidenceTx(t, now)
			valid := mode == "valid_claim_prepare_control"
			if !valid {
				callbackPrepareEvidenceFault(t, prepare, mode, now)
			}
			s, pool := callbackPrepareEvidenceStore(t, now, discovery, claim, prepare)
			store := &callbackPrepareSenderStore{CallbackStore: s}
			a := &callbackPrepareSenderAdapters{t: t, now: now, body: claim.steps[11].values[13].([]byte)}
			sender, err := cb.New(cb.Dependencies{Store: store, Clock: a, Gate: a, DNS: callbackPrepareSenderDNS{a}, Keys: a, Transport: a}, []cb.Policy{s.policies["tenant_cb\x00destination_a"]})
			if err != nil {
				t.Fatal(err)
			}
			result := sender.RunOne(context.Background())
			if pool.next != 3 || discovery.next != len(discovery.steps) || discovery.commits != 1 || claim.next != len(claim.steps) || claim.commits != 1 || a.keyCalls != 1 || a.dnsCalls != 1 {
				t.Fatalf("test did not reach Prepare through a real valid Claim: %+v", result)
			}
			if valid {
				if !result.Claimed || !result.TransportInvoked || result.Reconcile || result.Code != "HTTP_ACKNOWLEDGED" || a.transportCalls != 1 || store.completions != 1 || prepare.next != 20 || callbackPrepareEvidenceWrites(prepare) != 2 || prepare.commits != 1 {
					t.Fatalf("valid sender control did not send once: %+v", result)
				}
			} else if !result.Claimed || result.TransportInvoked || !result.Reconcile || result.Code != "PREPARE_UNCERTAIN_OR_STALE" || a.transportCalls != 0 || store.completions != 0 || prepare.next != 16 || callbackPrepareEvidenceWrites(prepare) != 0 || prepare.commits != 0 {
				t.Fatalf("bad evidence reached transport/settlement: result=%+v sends=%d completions=%d prepare_steps=%d writes=%d commits=%d", result, a.transportCalls, store.completions, prepare.next, callbackPrepareEvidenceWrites(prepare), prepare.commits)
			}
			if prepare.discards != 0 || !reflect.DeepEqual(a.key, make([]byte, 32)) {
				t.Fatal("unexpected discard or uncleared synthetic key")
			}
		})
	}
}
