package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"strings"
	"testing"
	"time"
)

func callbackRecoveryDiscovery(t *testing.T, rows [][]any) *callbackClaimTx {
	return &callbackClaimTx{t: t, readOnly: true, steps: []callbackClaimStep{{op: "rows", sql: callbackExpiredCandidates, args: []any{[]string{"tenant_cb", "tenant_cb"}, []string{"destination_a", "destination_b"}, "recovery_cb"}, rows: rows}}}
}
func callbackRecoveryTx(t *testing.T, dest, id string, prepared bool) *callbackClaimTx {
	now := time.Date(2026, 11, 4, 4, 0, 0, 0, time.UTC)
	lease := now.Add(-time.Second)
	body := []byte(`{"eventId":"event_cb"}`)
	var marker *time.Time
	if prepared {
		marker = callbackTime(now.Add(-2 * time.Second))
	}
	row := func(sql string, args, values []any) callbackClaimStep {
		return callbackClaimStep{op: "row", sql: sql, args: args, values: values}
	}
	exec := func(sql string, args []any) callbackClaimStep {
		return callbackClaimStep{op: "exec", sql: sql, args: args}
	}
	outcome, code := "lease_lost", "LEASE_EXPIRED_BEFORE_PREPARE"
	if prepared {
		outcome, code = "delivery_unknown", "LEASE_EXPIRED_AFTER_PREPARE"
	}
	// Old owner is intentionally different from the currently authorized worker.
	return &callbackClaimTx{t: t, steps: []callbackClaimStep{
		row(callbackDestinationHint, []any{"tenant_cb", id}, []any{dest}),
		row(callbackLockTenant+" SKIP LOCKED", []any{"tenant_cb"}, []any{"tenant_cb"}),
		row(callbackLockDestination+" SKIP LOCKED", []any{"tenant_cb", dest}, []any{dest}),
		row("SELECT boot_id,recovery_epoch,state", []any{"worker_cb", "worker"}, []any{"boot_cb", "recovery_cb", "active", true}),
		row(callbackReadDelivery, []any{"tenant_cb", id}, []any{dest, "leased", int64(9), 1, 4, callbackString("old_worker"), callbackString("recovery_cb"), &lease, callbackTime(now.Add(-3 * time.Second)), &lease, now.Add(time.Hour), nil, now.Add(-time.Minute), body, hashText(string(body)), nil, true, body}),
		row(callbackReadAttempt, []any{"tenant_cb", id, int64(9)}, []any{1, "old_worker", "recovery_cb", now.Add(-3 * time.Second), marker, nil, "in_progress", nil, nil}),
		{op: "rows", sql: callbackReadSlots, args: []any{"tenant_cb", id}, rows: [][]any{{"tenant", "tenant_cb", 0, "old_worker", id, int64(9), "recovery_cb", int64(3), lease}, {"destination", dest, 0, "old_worker", id, int64(9), "recovery_cb", int64(4), lease}}},
		row(callbackFinalTime, nil, []any{now}),
		exec(callbackUpdateAttempt, []any{"tenant_cb", id, int64(9), "old_worker", "recovery_cb", now, outcome, nil, code, 1}),
		exec(callbackUpdateDelivery, []any{"tenant_cb", id, int64(9), "old_worker", "recovery_cb", "ready", now.Add(time.Second), now, code, 1, 4}),
		exec(callbackReleaseSlot, []any{"tenant", "tenant_cb", "tenant_cb", 0, "old_worker", id, int64(9), "recovery_cb", int64(3), lease}),
		exec(callbackReleaseSlot, []any{"destination", "tenant_cb", dest, 0, "old_worker", id, int64(9), "recovery_cb", int64(4), lease}),
		exec("INSERT INTO audit(", nil),
	}}
}
func TestCallbackRecoverySelectionCommitsOnlyOne(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}, {"tenant_cb", "destination_b", "delivery_b"}})
		recovered := callbackRecoveryTx(t, "destination_a", "delivery_a", prepared)
		unused := callbackRecoveryTx(t, "destination_b", "delivery_b", false)
		s, p := callbackClaimStore(t, discovery, recovered, unused)
		ok, err := s.RecoverOneExpired(context.Background())
		if err != nil || !ok || p.next != 2 || recovered.commits != 1 || unused.next != 0 {
			t.Fatal("recovery did not stop after one confirmed settlement", ok, err)
		}
	}
}
func TestCallbackRecoveryCleanBusyAndStaleHintsSkip(t *testing.T) {
	for _, mode := range []string{"tenant_busy", "destination_busy", "terminal", "unexpired"} {
		t.Run(mode, func(t *testing.T) {
			discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}, {"tenant_cb", "destination_b", "delivery_b"}})
			first := callbackRecoveryTx(t, "destination_a", "delivery_a", false)
			switch mode {
			case "tenant_busy":
				first.steps = first.steps[:2]
				first.steps[1].values = nil
				first.steps[1].err = pgx.ErrNoRows
			case "destination_busy":
				first.steps = first.steps[:3]
				first.steps[2].values = nil
				first.steps[2].err = pgx.ErrNoRows
			case "terminal":
				first.steps = first.steps[:5]
				first.steps[4].values[1] = "delivered"
			case "unexpired":
				first.steps = first.steps[:8]
				// Keep the database clock after the recorded attempt start. A
				// future lease, not a backwards clock, represents an unexpired row.
				future := first.steps[7].values[0].(time.Time).Add(time.Second)
				first.steps[4].values[7], first.steps[4].values[9] = &future, &future
				for i := range first.steps[6].rows {
					first.steps[6].rows[i][8] = future
				}
			}
			second := callbackRecoveryTx(t, "destination_b", "delivery_b", false)
			s, p := callbackClaimStore(t, discovery, first, second)
			ok, err := s.RecoverOneExpired(context.Background())
			if err != nil || !ok || p.next != 3 || second.commits != 1 {
				t.Fatal("clean skip blocked ready tail", ok, err)
			}
		})
	}
}
func TestCallbackRecoveryFailuresNeverContinueOrReportRecovered(t *testing.T) {
	for _, mode := range []string{"selection_unknown", "selection_query", "outside_policy", "too_many", "rollback", "discard", "mutation_unknown", "generation", "runtime", "gate_after_selection", "gate_after_locks", "audit"} {
		t.Run(mode, func(t *testing.T) {
			discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}, {"tenant_cb", "destination_b", "delivery_b"}})
			first := callbackRecoveryTx(t, "destination_a", "delivery_a", false)
			switch mode {
			case "selection_unknown":
				discovery.commitErr = errors.New("PRIVATE commit response")
			case "selection_query":
				discovery.steps[0].err = errors.New("PRIVATE query")
			case "outside_policy":
				discovery.steps[0].rows = [][]any{{"tenant_other", "destination_a", "delivery_a"}}
			case "too_many":
				discovery.steps[0].rows = append(discovery.steps[0].rows, []any{"tenant_cb", "destination_a", "delivery_c"})
			case "rollback", "discard":
				first.steps = first.steps[:2]
				first.steps[1].err = pgx.ErrNoRows
				first.rollbackErr = errors.New("PRIVATE rollback")
				if mode == "discard" {
					first.discardErr = errors.New("PRIVATE discard")
				}
			case "mutation_unknown":
				first.commitErr = errors.New("PRIVATE response")
			case "generation":
				first.steps = first.steps[:5]
				first.steps[4].values[6] = callbackString("different_epoch")
			case "runtime":
				first.steps = first.steps[:4]
				first.steps[3].values[1] = "different_epoch"
			case "gate_after_selection":
			case "gate_after_locks":
				first.steps = first.steps[:8]
			case "audit":
				first.steps[len(first.steps)-1].err = errors.New("PRIVATE audit")
			}
			unused := callbackRecoveryTx(t, "destination_b", "delivery_b", false)
			s, p := callbackClaimStore(t, discovery, first, unused)
			calls := 0
			s.gate = func(context.Context) error {
				calls++
				if mode == "gate_after_selection" && calls == 2 || mode == "gate_after_locks" && calls == 3 {
					return cb.ErrStale
				}
				return nil
			}
			ok, err := s.RecoverOneExpired(context.Background())
			if err == nil || ok || p.next > 2 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("uncertainty reported recovery or continued", ok, err)
			}
			if (mode == "selection_unknown" || mode == "mutation_unknown") && !errors.Is(err, cb.ErrCommitUnknown) {
				t.Fatal("lost commit classification")
			}
		})
	}
}
func TestCallbackRecoveryEmptyAndPreflight(t *testing.T) {
	discovery := callbackRecoveryDiscovery(t, nil)
	s, p := callbackClaimStore(t, discovery)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if ok, err := s.RecoverOneExpired(ctx); ok || err == nil || p.next != 0 {
			t.Fatal("invalid context acquired")
		}
	}
	s.gate = func(context.Context) error { return cb.ErrStale }
	if ok, err := s.RecoverOneExpired(context.Background()); ok || err == nil || p.next != 0 {
		t.Fatal("closed gate acquired")
	}
	s.gate = func(context.Context) error { return nil }
	if ok, err := s.RecoverOneExpired(context.Background()); ok || err != nil || p.next != 1 {
		t.Fatal("empty selection counted as recovery")
	}
}
func TestCallbackRecoveryNoopIsNotRecovered(t *testing.T) {
	for _, mode := range []string{"terminal", "unexpired"} {
		discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}})
		first := callbackRecoveryTx(t, "destination_a", "delivery_a", false)
		if mode == "terminal" {
			first.steps = first.steps[:5]
			first.steps[4].values[1] = "delivered"
		} else {
			first.steps = first.steps[:8]
			future := first.steps[7].values[0].(time.Time).Add(time.Second)
			first.steps[4].values[7], first.steps[4].values[9] = &future, &future
			for i := range first.steps[6].rows {
				first.steps[6].rows[i][8] = future
			}
		}
		s, _ := callbackClaimStore(t, discovery, first)
		if ok, err := s.RecoverOneExpired(context.Background()); ok || err != nil {
			t.Fatal("no-op counted as committed recovery")
		}
	}
}

// Contradictory unfinished evidence must not be rewritten into a fresh retry.
// Each script ends before the first DML statement, so even an attempted write
// fails the test. These are pgx control-flow doubles, not PostgreSQL execution.
func TestCallbackRecoveryRejectsContradictoryEvidenceBeforeMutation(t *testing.T) {
	for _, path := range []string{"explicit", "selector"} {
		for _, mode := range []string{"missing_claim", "zero_start", "start_before_claim", "start_after_now", "prepare_before_start", "prepare_after_now", "status_present", "error_present", "empty_error_present", "leased_delivered", "negative_db_time", "unsafe_db_time", "unexpired_bad_status"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				first := callbackRecoveryTx(t, "destination_a", "delivery_a", false)
				first.steps = first.steps[:8] // No attempt, delivery, slot or audit DML.
				now := first.steps[7].values[0].(time.Time)
				switch mode {
				case "missing_claim":
					first.steps[4].values[8] = nil
				case "zero_start":
					first.steps[5].values[3] = time.Time{}
				case "start_before_claim":
					first.steps[5].values[3] = now.Add(-4 * time.Second)
				case "start_after_now":
					first.steps[5].values[3] = now.Add(time.Microsecond)
				case "prepare_before_start":
					first.steps[5].values[4] = callbackTime(now.Add(-4 * time.Second))
				case "prepare_after_now":
					first.steps[5].values[4] = callbackTime(now.Add(time.Microsecond))
				case "status_present":
					status := 204
					first.steps[5].values[7] = &status
				case "error_present":
					first.steps[5].values[8] = callbackString("PRIVATE prior outcome")
				case "empty_error_present":
					first.steps[5].values[8] = callbackString("")
				case "leased_delivered":
					first.steps[4].values[11] = callbackTime(now.Add(-2 * time.Second))
				case "negative_db_time":
					first.steps[7].values[0] = time.UnixMilli(-1)
				case "unsafe_db_time":
					first.steps[7].values[0] = time.UnixMilli(maxSafeInteger + 1)
				case "unexpired_bad_status":
					future := now.Add(time.Second)
					first.steps[4].values[7], first.steps[4].values[9] = &future, &future
					for i := range first.steps[6].rows {
						first.steps[6].rows[i][8] = future
					}
					status := 204
					first.steps[5].values[7] = &status
				}
				var s *CallbackStore
				var pool *callbackClaimPool
				var err error
				var recovered bool
				wantAcquires := 1
				if path == "explicit" {
					first.steps[1].sql = callbackLockTenant
					first.steps[2].sql = callbackLockDestination
					s, pool = callbackClaimStore(t, first)
					err = s.RecoverExpired(context.Background(), "tenant_cb", "delivery_a")
				} else {
					discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}, {"tenant_cb", "destination_b", "delivery_b"}})
					unused := callbackRecoveryTx(t, "destination_b", "delivery_b", false)
					s, pool = callbackClaimStore(t, discovery, first, unused)
					recovered, err = s.RecoverOneExpired(context.Background())
					wantAcquires = 2
					if unused.next != 0 || unused.commits != 0 {
						t.Fatal("contradictory evidence skipped to another delivery")
					}
				}
				if !errors.Is(err, cb.ErrStale) || recovered || pool.next != wantAcquires || first.commits != 0 || first.next != 8 || first.rollbacks != 1 || first.releases != 1 || first.discards != 0 {
					t.Fatalf("contradictory recovery was not refused cleanly: recovered=%v err=%v", recovered, err)
				}
				if strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal("private stored evidence escaped the error boundary")
				}
			})
		}
	}
}

func TestCallbackRecoveryValidTimeBoundariesPreserveSettlement(t *testing.T) {
	for _, path := range []string{"explicit", "selector"} {
		for _, mode := range []string{"unprepared_start_equals_claim", "prepared_equals_start", "expiry_equals_now"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				prepared := strings.HasPrefix(mode, "prepared_")
				first := callbackRecoveryTx(t, "destination_a", "delivery_a", prepared)
				now := first.steps[7].values[0].(time.Time)
				switch mode {
				case "prepared_equals_start":
					first.steps[5].values[4] = callbackTime(first.steps[5].values[3].(time.Time))
				case "expiry_equals_now":
					first.steps[4].values[7], first.steps[4].values[9] = &now, &now
					for i := range first.steps[6].rows {
						first.steps[6].rows[i][8] = now
						first.steps[10+i].args[9] = now
					}
				}
				var err error
				if path == "explicit" {
					first.steps[1].sql, first.steps[2].sql = callbackLockTenant, callbackLockDestination
					s, _ := callbackClaimStore(t, first)
					err = s.RecoverExpired(context.Background(), "tenant_cb", "delivery_a")
				} else {
					discovery := callbackRecoveryDiscovery(t, [][]any{{"tenant_cb", "destination_a", "delivery_a"}})
					s, _ := callbackClaimStore(t, discovery, first)
					var recovered bool
					recovered, err = s.RecoverOneExpired(context.Background())
					if !recovered {
						t.Fatal("valid recovery not reported")
					}
				}
				if err != nil || first.commits != 1 || first.next != len(first.steps) {
					t.Fatal("valid expired recovery changed", err)
				}
			})
		}
	}
}
