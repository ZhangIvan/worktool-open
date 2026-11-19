package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"strings"
	"testing"
	"time"
)

// Common baseline-compatible regression: no sockets, PG or receiver effects.
// Claim snapshot uses the exact production deadline mapper; Prepare is a mock.
// Sender and completion validation/Runner are production implementations.
// Embedding exposes optional settlement only where production implements it.
type boundaryAuditStore struct {
	*CallbackStore
	d      cb.Delivery
	c      *CallbackCompleter
	intent cb.Completion
}

func (s *boundaryAuditStore) Claim(context.Context) (cb.Delivery, bool, error) { return s.d, true, nil }
func (s *boundaryAuditStore) Prepare(_ context.Context, p cb.Prepared) (cb.Permit, error) {
	return cb.Permit{Prepared: p, Fresh: true}, nil
}
func (s *boundaryAuditStore) Complete(ctx context.Context, c cb.Completion) error {
	s.intent = c
	return callbackStoreError(s.c.Complete(ctx, callbackCompletionIntent(c)))
}

type boundaryAuditTransport struct{ calls int }

func (a *boundaryAuditTransport) Send(context.Context, cb.Request) (cb.Response, error) {
	a.calls++
	return cb.Response{Status: 503}, nil
}

func TestCallbackRetrySettlementSenderBoundaries(t *testing.T) {
	for _, fixture := range []struct {
		name             string
		remaining, delay time.Duration
		mismatch         bool
		intent           string
	}{
		{"conservative_margin", 1050 * time.Millisecond, 0, true, "dead_letter"},
		{"ready_control", 1200 * time.Millisecond, 0, false, "ready"},
		{"dead_letter_control", 900 * time.Millisecond, 0, false, "dead_letter"},
		{"completion_clock_advanced", 1200 * time.Millisecond, 250 * time.Millisecond, true, "ready"},
		{"margin_unknown_committed", 1050 * time.Millisecond, 0, true, "dead_letter"},
		{"late_unknown_committed", 1200 * time.Millisecond, 250 * time.Millisecond, true, "ready"},
		{"unknown_rolled_back", 1200 * time.Millisecond, 0, false, "ready"},
		{"unknown_conflicting_audit", 1200 * time.Millisecond, 0, false, "ready"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			remaining := fixture.remaining
			completer, db, base := callbackFixture(t)
			now := db.now
			if fixture.delay != 0 {
				db.queryHook = func(sql string) {
					if sql == callbackFinalTime && db.now.Before(now.Add(fixture.delay)) {
						db.now = now.Add(fixture.delay)
					}
				}
			}
			unknown := strings.Contains(fixture.name, "unknown")
			if unknown {
				db.commitErr = errors.New("synthetic acknowledgement lost")
				db.commitAfterApply = fixture.name != "unknown_rolled_back"
				db.rollbackHook = func() {
					db.commitErr = nil
					db.now = now.Add(3 * time.Hour)
					if fixture.name == "unknown_conflicting_audit" && db.audit != nil {
						db.audit[3] = "ready:DNS_UNAVAILABLE"
					}
				}
			}
			db.delivery.expires = now.Add(remaining)
			db.delivery.lease = callbackTime(db.delivery.expires)
			db.delivery.hard = callbackTime(db.delivery.expires)
			for i := range db.slots {
				db.slots[i].lease = db.delivery.expires
			}
			localExpiry, err := callbackLocalDeadline(now, now, now, db.delivery.expires)
			if err != nil {
				t.Fatal(err)
			}
			binding := cb.Binding{Tenant: "tenant_cb", Destination: "destination_cb", URL: "https://callbacks.example.com/result", KeyRef: "key_cb", PolicyVersion: 1, KeyVersion: 1}
			h := base.Handle
			store := &boundaryAuditStore{CallbackStore: &CallbackStore{completer: completer}, c: completer, d: cb.Delivery{Handle: cb.Handle{Tenant: h.Tenant, DeliveryID: h.DeliveryID, Owner: h.Owner, Recovery: h.Recovery, Fence: h.Fence, Attempt: h.Attempt, MaxAttempts: h.MaxAttempts}, Binding: binding, EventID: "callback_cb", Digest: db.delivery.digest, Body: db.delivery.body, LeaseUntil: localExpiry, HardDeadline: localExpiry, ExpiresAt: localExpiry}}
			adapters := &callbackPrepareSenderAdapters{t: t, now: now}
			transport := &boundaryAuditTransport{}
			sender, err := cb.New(cb.Dependencies{Store: store, Clock: adapters, Gate: adapters, DNS: callbackPrepareSenderDNS{adapters}, Keys: adapters, Transport: transport}, []cb.Policy{{Binding: binding, CIDRs: []string{"93.184.216.0/24"}}})
			if err != nil {
				t.Fatal(err)
			}
			result := sender.RunOne(context.Background())
			if transport.calls != 1 {
				t.Fatal("did not reach retryable HTTP response")
			}
			t.Logf("PG remaining=%s mapped remaining=%s local intent=%s result=%+v DB=%s DML=%d commits=%d", remaining, localExpiry.Sub(now), store.intent.State, result, db.delivery.state, len(callbackDML(db)), db.commits)
			expected := "ready"
			if remaining == 900*time.Millisecond || fixture.delay != 0 {
				expected = "dead_letter"
			}
			if fixture.name == "unknown_rolled_back" || fixture.name == "unknown_conflicting_audit" {
				if !result.Reconcile || result.State != "" || db.audits > 1 || db.acquires != 2 || len(callbackDML(db)) != 5 {
					t.Fatal("unknown/conflicting settlement confirmed or retried", result)
				}
				return
			}
			commits := 1
			if unknown {
				commits = 2
			}
			if result.Reconcile || result.State != expected || result.Outcome != "retryable_failure" || result.Code != "HTTP_RETRYABLE" || db.delivery.state != expected || db.commits != commits || len(callbackDML(db)) != 5 {
				t.Fatalf("valid response failed authoritative settlement: result=%+v state=%s commits=%d", result, db.delivery.state, db.commits)
			}

		})
	}
}
