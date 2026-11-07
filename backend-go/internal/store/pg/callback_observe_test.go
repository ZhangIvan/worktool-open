package pg

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
)

func TestCallbackObserveExactDurableSettlement(t *testing.T) {
	for _, mode := range []string{"delivered", "retry", "cancelled", "exhausted", "slots_reassigned", "past_deadline"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			switch mode {
			case "retry":
				c = callbackRetry(c, "DNS_UNAVAILABLE")
			case "cancelled":
				c.Outcome, c.State, c.Code, c.Status = "permanent_failure", "cancelled", "AUTHORITY_DENIED", 0
			case "exhausted":
				c = callbackRetry(c, "DNS_UNAVAILABLE")
				c.Handle.Attempt, db.delivery.attempts, db.attempt.number = 4, 4, 4
				c.State, c.NextAt = "dead_letter", time.Time{}
			}
			// Scripted durable commit followed by a lost acknowledgement. Not real PG.
			db.commitErr, db.commitAfterApply = errors.New("PRIVATE lost acknowledgement"), true
			if err := s.Complete(context.Background(), c); err != ErrCallbackCompletionUnknown {
				t.Fatal(err)
			}
			db.commitErr, db.calls = nil, nil
			if mode == "slots_reassigned" {
				for i := range db.slots {
					db.slots[i].delivery, db.slots[i].owner, db.slots[i].fence = "other_delivery", "other_owner", 99
				}
			}
			if mode == "past_deadline" {
				db.now = db.now.Add(2 * time.Hour)
			}
			beforeDelivery, beforeAttempt, beforeSlots := db.delivery, db.attempt, append([]callbackSlotRow(nil), db.slots...)
			if err := s.ObserveCompletion(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if len(callbackDML(db)) != 0 || db.audits != 1 || db.acquires != 2 || db.commits != 2 || db.discards != 1 || !reflect.DeepEqual(db.delivery, beforeDelivery) || !reflect.DeepEqual(db.attempt, beforeAttempt) || !reflect.DeepEqual(db.slots, beforeSlots) || db.quota != 90112 {
				t.Fatal("observation mutated or retried")
			}
			for _, q := range db.calls {
				if q.sql == callbackReadSlots {
					t.Fatal("locked reassigned slots")
				}
			}
		})
	}
}

func TestCallbackObserveRefusesMissingOrConflictingProof(t *testing.T) {
	cases := map[string]func(*callbackTestDB, *CallbackCompletion){
		"unfinished":        func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.finished = nil },
		"successor_fence":   func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.fence++ },
		"successor_attempt": func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.attempts++ },
		"wrong_owner":       func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.owner = "successor" },
		"new_epoch":         func(d *callbackTestDB, _ *CallbackCompletion) { d.runtime.epoch = "new_epoch" },
		"new_boot":          func(d *callbackTestDB, _ *CallbackCompletion) { d.runtime.boot = "new_boot" },
		"intent_status":     func(_ *callbackTestDB, c *CallbackCompletion) { c.Status = 200 },
		"missing_prepared":  func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.prepared = nil },
		"wrong_outcome":     func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.outcome = "delivery_unknown" },
		"wrong_code":        func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.code = callbackString("HTTP_PERMANENT") },
		"wrong_state":       func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.state = "dead_letter" },
		"wrong_delivered_time": func(d *callbackTestDB, _ *CallbackCompletion) {
			d.delivery.delivered = callbackTime(d.now.Add(-time.Second))
		},
		"missing_graph":       func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.graph = false },
		"missing_audit":       func(d *callbackTestDB, _ *CallbackCompletion) { d.audit = nil },
		"wrong_audit_id":      func(d *callbackTestDB, _ *CallbackCompletion) { d.auditID = "other" },
		"wrong_audit_tenant":  func(d *callbackTestDB, _ *CallbackCompletion) { d.auditTenant = "other" },
		"wrong_audit_actor":   func(d *callbackTestDB, _ *CallbackCompletion) { d.audit[0] = "other" },
		"wrong_audit_action":  func(d *callbackTestDB, _ *CallbackCompletion) { d.audit[1] = "other" },
		"wrong_audit_target":  func(d *callbackTestDB, _ *CallbackCompletion) { d.audit[2] = "other" },
		"wrong_audit_outcome": func(d *callbackTestDB, _ *CallbackCompletion) { d.audit[3] = "other" },
		"wrong_audit_time":    func(d *callbackTestDB, _ *CallbackCompletion) { d.audit[4] = d.now.UnixMilli() - 1 },
		"retained_lease":      func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.owner = callbackString("worker_cb") },
		"retained_old_slot": func(d *callbackTestDB, c *CallbackCompletion) {
			d.slots[0].delivery, d.slots[0].owner, d.slots[0].recovery, d.slots[0].fence = c.Handle.DeliveryID, c.Handle.Owner, c.Handle.Recovery, c.Handle.Fence
		},
		"audit_read_failure":         func(d *callbackTestDB, _ *CallbackCompletion) { d.failSQL = callbackReadCompletionAudit },
		"observation_commit_unknown": func(d *callbackTestDB, _ *CallbackCompletion) { d.commitErr = errors.New("PRIVATE commit") },
		"rollback_failure":           func(d *callbackTestDB, _ *CallbackCompletion) { d.rollbackErr = errors.New("PRIVATE cleanup") },
		"discard_failure": func(d *callbackTestDB, _ *CallbackCompletion) {
			d.commitErr, d.discardErr = errors.New("PRIVATE commit"), errors.New("PRIVATE discard")
		},
		"gate_before":        func(d *callbackTestDB, _ *CallbackCompletion) { d.denyGateAt = d.gateCalls + 1 },
		"gate_after_locks":   func(d *callbackTestDB, _ *CallbackCompletion) { d.denyGateAt = d.gateCalls + 2 },
		"gate_after_cleanup": func(d *callbackTestDB, _ *CallbackCompletion) { d.denyGateAt = d.gateCalls + 3 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			if err := s.Complete(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			mutate(db, &c)
			db.calls = nil
			if err := s.ObserveCompletion(context.Background(), c); err == nil {
				t.Fatal("unproven settlement confirmed")
			}
			acquires := 2
			if name == "gate_before" {
				acquires = 1
			}
			if len(callbackDML(db)) != 0 || db.audits != 1 || db.acquires != acquires {
				t.Fatal("observation mutated/retried")
			}
		})
	}
}

func TestCallbackObserveContextAndGateAfterAudit(t *testing.T) {
	for _, mode := range []string{"cancel_at_audit", "gate_at_audit", "cancel_in_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := callbackFixture(t)
			if err := s.Complete(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db.calls = nil
			if mode == "cancel_in_cleanup" {
				db.rollbackHook = cancel
			} else {
				db.queryHook = func(q string) {
					if q == callbackReadCompletionAudit {
						if mode == "cancel_at_audit" {
							cancel()
						} else {
							db.denyGateAt = db.gateCalls + 1
						}
					}
				}
			}
			if err := s.ObserveCompletion(ctx, c); err == nil || len(callbackDML(db)) != 0 {
				t.Fatal("lost authority confirmed")
			}
		})
	}
}

func TestCallbackObserveDoesNotCompleteUnfinishedAttempt(t *testing.T) {
	s, db, c := callbackFixture(t)
	if err := s.ObserveCompletion(context.Background(), c); err == nil || db.attempt.finished != nil || len(callbackDML(db)) != 0 || db.commits != 0 {
		t.Fatal("observation settled unfinished attempt")
	}
	if db.slots[0].delivery == "" || db.slots[1].delivery == "" {
		t.Fatal("observation released slots")
	}
}

func TestCallbackObserveAdapterAndDuplicateCompatibility(t *testing.T) {
	s, db, c := callbackFixture(t)
	store := &CallbackStore{completer: s}
	h := c.Handle
	intent := cb.Completion{Handle: cb.Handle{Tenant: h.Tenant, DeliveryID: h.DeliveryID, Owner: h.Owner, Recovery: h.Recovery, Fence: h.Fence, Attempt: h.Attempt, MaxAttempts: h.MaxAttempts}, Outcome: c.Outcome, State: c.State, Code: c.Code, Status: c.Status}
	if err := store.ObserveCompletion(context.Background(), intent); err == nil {
		t.Fatal("unfinished adapter confirmed")
	}
	if err := store.Complete(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveCompletion(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	db.audit = nil
	if err := store.Complete(context.Background(), intent); err != nil {
		t.Fatal("existing finished-duplicate behavior changed", err)
	}
	if err := store.ObserveCompletion(context.Background(), intent); err == nil {
		t.Fatal("missing audit confirmed")
	}
	for _, cleanup := range []string{"rollback", "discard"} {
		s, db, c := callbackFixture(t)
		db.commitErr, db.commitAfterApply = errors.New("PRIVATE commit"), true
		if cleanup == "rollback" {
			db.rollbackErr = errors.New("PRIVATE rollback")
		} else {
			db.discardErr = errors.New("PRIVATE discard")
		}
		if err := callbackStoreError(s.Complete(context.Background(), c)); err == nil || !errors.Is(err, cb.ErrCommitUnknown) || !errors.Is(err, cb.ErrCleanupUnconfirmed) {
			t.Fatal("original cleanup failure may be cleared by observation")
		}
	}
}

func TestCallbackObserveRetryUsesOriginalDurableSchedule(t *testing.T) {
	s, db, c := callbackFixture(t)
	c = callbackRetry(c, "DNS_UNAVAILABLE")
	if err := s.Complete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveCompletion(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	db.delivery.available = db.delivery.available.Add(time.Millisecond)
	if err := s.ObserveCompletion(context.Background(), c); err == nil {
		t.Fatal("changed retry schedule confirmed")
	}
}
