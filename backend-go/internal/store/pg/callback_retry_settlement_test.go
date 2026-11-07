package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"reflect"
	"testing"
	"time"
)

func retryIntent(c CallbackCompletion) cb.RetryCompletionIntent {
	h := c.Handle
	return cb.RetryCompletionIntent{Handle: cb.Handle{Tenant: h.Tenant, DeliveryID: h.DeliveryID, Owner: h.Owner, Recovery: h.Recovery, Fence: h.Fence, Attempt: h.Attempt, MaxAttempts: h.MaxAttempts}, Outcome: c.Outcome, Code: c.Code, Status: c.Status}
}
func retryStoreFixture(t *testing.T) (*CallbackStore, *callbackTestDB, CallbackCompletion) {
	s, db, c := callbackFixture(t)
	c = callbackRetry(c, "HTTP_RETRYABLE")
	c.Status = 503
	return &CallbackStore{completer: s}, db, c
}
func TestCallbackRetrySettlementAuthoritativeBoundaries(t *testing.T) {
	for attempt := 1; attempt <= 4; attempt++ {
		for _, delta := range []time.Duration{-time.Microsecond, 0, time.Microsecond, time.Hour} {
			t.Run(time.Duration(attempt).String()+"/"+delta.String(), func(t *testing.T) {
				s, db, c := retryStoreFixture(t)
				c.Handle.Attempt = attempt
				db.delivery.attempts = attempt
				db.attempt.number = attempt
				delay := time.Second << (attempt - 1)
				db.delivery.expires = db.now.Add(delay + delta)
				db.delivery.lease = callbackTime(db.delivery.expires)
				db.delivery.hard = callbackTime(db.delivery.expires)
				for i := range db.slots {
					db.slots[i].lease = db.delivery.expires
				}
				intent := retryIntent(c)
				r, e := s.SettleRetry(context.Background(), intent)
				expected := "ready"
				if attempt == 4 || delta <= 0 {
					expected = "dead_letter"
				}
				if e != nil || r.Intent != intent || r.State != expected || db.delivery.state != expected || db.commits != 1 || len(callbackDML(db)) != 5 {
					t.Fatal(r, e)
				}
				if expected == "ready" {
					if !r.NextAt.Equal(db.now.Add(delay)) || !db.delivery.available.Equal(r.NextAt) {
						t.Fatal("wrong schedule")
					}
				} else if !r.NextAt.IsZero() {
					t.Fatal("terminal schedule")
				}
				db.now = db.now.Add(3 * time.Hour)
				db.calls = nil
				observed, e := s.ObserveRetrySettlement(context.Background(), intent)
				if e != nil || observed != r || len(callbackDML(db)) != 0 {
					t.Fatal("observation rescheduled/changed state", observed, e)
				}
			})
		}
	}
}
func TestCallbackRetrySettlementUnknownCommitAndCleanup(t *testing.T) {
	for _, mode := range []string{"committed", "rolled_back", "rollback_cleanup", "discard_cleanup", "observe_commit_unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := retryStoreFixture(t)
			i := retryIntent(c)
			db.commitErr = errors.New("PRIVATE lost ACK")
			db.commitAfterApply = mode != "rolled_back"
			if mode == "rollback_cleanup" {
				db.rollbackErr = errors.New("PRIVATE rollback")
			}
			if mode == "discard_cleanup" {
				db.discardErr = errors.New("PRIVATE discard")
			}
			r, e := s.SettleRetry(context.Background(), i)
			if r != (cb.RetryCompletionReceipt{}) || !errors.Is(e, cb.ErrCommitUnknown) {
				t.Fatal("uncertain receipt escaped", r, e)
			}
			cleanup := mode == "rollback_cleanup" || mode == "discard_cleanup"
			if errors.Is(e, cb.ErrCleanupUnconfirmed) != cleanup {
				t.Fatal("cleanup classification")
			}
			if cleanup {
				return
			} // caller must not observe after failed cleanup
			db.commitErr = nil
			db.calls = nil
			if mode == "observe_commit_unknown" {
				db.commitErr = errors.New("PRIVATE proof ACK")
			}
			r, e = s.ObserveRetrySettlement(context.Background(), i)
			if mode == "committed" {
				if e != nil || r.State != "ready" || r.Intent != i {
					t.Fatal(r, e)
				}
			} else if e == nil || r != (cb.RetryCompletionReceipt{}) {
				t.Fatal("unproven receipt")
			}
			if len(callbackDML(db)) != 0 || db.audits > 1 {
				t.Fatal("observation mutated")
			}
		})
	}
}
func TestCallbackRetrySettlementRejectsInvalidOrUnfinishedEvidence(t *testing.T) {
	for _, mode := range []string{"terminal_intent", "wrong_status", "unknown_code", "wrong_owner", "wrong_recovery", "wrong_fence", "missing_prepare", "expired_lease", "expired_hard", "expired_delivery", "wrong_slot", "graph", "gate", "unfinished_observe"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := retryStoreFixture(t)
			i := retryIntent(c)
			switch mode {
			case "terminal_intent":
				i.Outcome = "delivered"
				i.Code = "HTTP_ACKNOWLEDGED"
				i.Status = 204
			case "wrong_status":
				i.Status = 204
			case "unknown_code":
				i.Code = "PRIVATE arbitrary"
			case "wrong_owner":
				i.Handle.Owner = "other"
			case "wrong_recovery":
				i.Handle.Recovery = "other"
			case "wrong_fence":
				i.Handle.Fence++
			case "missing_prepare":
				db.attempt.prepared = nil
			case "expired_lease":
				db.delivery.lease = callbackTime(db.now)
			case "expired_hard":
				db.delivery.hard = callbackTime(db.now)
			case "expired_delivery":
				db.delivery.expires = db.now
			case "wrong_slot":
				db.slots[0].slotFence = 0
			case "graph":
				db.delivery.graph = false
			case "gate":
				db.denyGateAt = 1
			}
			var r cb.RetryCompletionReceipt
			var e error
			if mode == "unfinished_observe" {
				r, e = s.ObserveRetrySettlement(context.Background(), i)
			} else {
				r, e = s.SettleRetry(context.Background(), i)
			}
			if e == nil || r != (cb.RetryCompletionReceipt{}) || len(callbackDML(db)) != 0 || db.commits != 0 || db.delivery.state != "leased" {
				t.Fatal("bad evidence accepted", r, e)
			}
		})
	}
}

func TestCallbackRetryObservationRefusesMissingOrConflictingProof(t *testing.T) {
	cases := map[string]func(*callbackTestDB, *CallbackCompletion){
		"unfinished":        func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.finished = nil },
		"successor_fence":   func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.fence++ },
		"successor_attempt": func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.attempts++ },
		"wrong_owner":       func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.owner = "successor" },
		"new_epoch":         func(d *callbackTestDB, _ *CallbackCompletion) { d.runtime.epoch = "new_epoch" },
		"new_boot":          func(d *callbackTestDB, _ *CallbackCompletion) { d.runtime.boot = "new_boot" },
		"intent_status":     func(_ *callbackTestDB, c *CallbackCompletion) { c.Status = 429 },
		"missing_prepared":  func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.prepared = nil },
		"wrong_schedule": func(d *callbackTestDB, _ *CallbackCompletion) {
			d.delivery.available = d.delivery.available.Add(time.Microsecond)
		},
		"wrong_status": func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.status = callbackInt(429) },
		"wrong_last_code": func(d *callbackTestDB, _ *CallbackCompletion) {
			d.delivery.lastCode = callbackString("DNS_UNAVAILABLE")
		},
		"wrong_outcome": func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.outcome = "delivery_unknown" },
		"wrong_code":    func(d *callbackTestDB, _ *CallbackCompletion) { d.attempt.code = callbackString("HTTP_PERMANENT") },
		"wrong_state":   func(d *callbackTestDB, _ *CallbackCompletion) { d.delivery.state = "dead_letter" },
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
			s, db, c := retryStoreFixture(t)
			if _, err := s.SettleRetry(context.Background(), retryIntent(c)); err != nil {
				t.Fatal(err)
			}
			mutate(db, &c)
			db.calls = nil
			if receipt, err := s.ObserveRetrySettlement(context.Background(), retryIntent(c)); err == nil || receipt != (cb.RetryCompletionReceipt{}) {
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

func TestCallbackRetryObservationContextAndGateAfterAudit(t *testing.T) {
	for _, mode := range []string{"cancel_at_audit", "gate_at_audit", "cancel_in_cleanup"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := retryStoreFixture(t)
			if _, err := s.SettleRetry(context.Background(), retryIntent(c)); err != nil {
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
			if receipt, err := s.ObserveRetrySettlement(ctx, retryIntent(c)); err == nil || receipt != (cb.RetryCompletionReceipt{}) || len(callbackDML(db)) != 0 {
				t.Fatal("lost authority confirmed")
			}
		})
	}
}

func TestCallbackRetryObservationReleasedAndReassignedSlots(t *testing.T) {
	s, db, c := retryStoreFixture(t)
	i := retryIntent(c)
	r, e := s.SettleRetry(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	for k := range db.slots {
		db.slots[k].delivery = "other_delivery"
		db.slots[k].owner = "other_owner"
		db.slots[k].fence = 99
	}
	beforeD, beforeA, beforeS := db.delivery, db.attempt, append([]callbackSlotRow(nil), db.slots...)
	db.calls = nil
	got, e := s.ObserveRetrySettlement(context.Background(), i)
	if e != nil || got != r || !reflect.DeepEqual(db.delivery, beforeD) || !reflect.DeepEqual(db.attempt, beforeA) || !reflect.DeepEqual(db.slots, beforeS) || len(callbackDML(db)) != 0 {
		t.Fatal("observation changed unrelated slots", got, e)
	}
	for _, q := range db.calls {
		if q.sql == callbackReadSlots {
			t.Fatal("locked reassigned slots")
		}
	}
}

func TestCallbackRetrySettlementFiniteOutcomesAndDuplicate(t *testing.T) {
	for _, mode := range []string{"dns_before_prepare", "receiver_unknown", "retry_status"} {
		t.Run(mode, func(t *testing.T) {
			s, db, c := retryStoreFixture(t)
			if mode == "dns_before_prepare" {
				c.Code = "DNS_UNAVAILABLE"
				c.Status = 0
				db.attempt.prepared = nil
			}
			if mode == "receiver_unknown" {
				c.Outcome = "delivery_unknown"
				c.Code = "TRANSPORT_OUTCOME_UNKNOWN"
				c.Status = 0
			}
			i := retryIntent(c)
			r, e := s.SettleRetry(context.Background(), i)
			if e != nil || r.Intent != i || r.State != "ready" || db.attempt.outcome != c.Outcome {
				t.Fatal(r, e)
			}
			db.calls = nil
			duplicate, e := s.SettleRetry(context.Background(), i)
			if e != nil || duplicate != r || len(callbackDML(db)) != 0 || db.audits != 1 {
				t.Fatal("duplicate rewrote settlement", duplicate, e)
			}
		})
	}
}
