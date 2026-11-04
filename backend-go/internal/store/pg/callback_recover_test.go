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
				first.steps[7].values[0] = time.Date(2026, 11, 4, 3, 59, 0, 0, time.UTC)
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
			first.steps[7].values[0] = time.Date(2026, 11, 4, 3, 59, 0, 0, time.UTC)
		}
		s, _ := callbackClaimStore(t, discovery, first)
		if ok, err := s.RecoverOneExpired(context.Background()); ok || err != nil {
			t.Fatal("no-op counted as committed recovery")
		}
	}
}
