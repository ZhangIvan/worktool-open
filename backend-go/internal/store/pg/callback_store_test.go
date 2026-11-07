package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"testing"
	"time"
)

func TestCallbackStoreConfigurationAndCopy(t *testing.T) {
	complete, db, _ := callbackFixture(t)
	p := cb.Policy{Binding: cb.Binding{Tenant: "tenant_cb", Destination: "destination_cb", URL: "https://callbacks.example.com/result", KeyRef: "key_cb", PolicyVersion: 1, KeyVersion: 1}, CIDRs: []string{"93.184.216.0/24"}}
	s, e := NewCallbackStore(complete.runner, db.runtime, complete.gate, []cb.Policy{p})
	if e != nil {
		t.Fatal(e)
	}
	p.CIDRs[0] = "127.0.0.0/8"
	if s.policies["tenant_cb\x00destination_cb"].CIDRs[0] != "93.184.216.0/24" {
		t.Fatal("mutable policy")
	}
	if _, e = NewCallbackStore(complete.runner, db.runtime, complete.gate, []cb.Policy{p}); e == nil {
		t.Fatal("private egress policy")
	}
	if db.acquires != 0 {
		t.Fatal("constructor accessed DB")
	}
}
func TestCallbackStoreCompleteAdapter(t *testing.T) {
	complete, db, c := callbackFixture(t)
	s := &CallbackStore{completer: complete}
	h := c.Handle
	e := s.Complete(context.Background(), cb.Completion{Handle: cb.Handle{Tenant: h.Tenant, DeliveryID: h.DeliveryID, Owner: h.Owner, Recovery: h.Recovery, Fence: h.Fence, Attempt: h.Attempt, MaxAttempts: h.MaxAttempts}, Outcome: c.Outcome, State: c.State, Code: c.Code, Status: c.Status})
	if e != nil || db.commits != 1 || db.delivery.state != "delivered" {
		t.Fatal("settlement adapter")
	}
	if !errors.Is(callbackStoreError(ErrCommitOutcomeUnknown), cb.ErrCommitUnknown) {
		t.Fatal("unknown commit lost")
	}
	if callbackStoreError(errors.New("PRIVATE")).Error() == "PRIVATE" {
		t.Fatal("raw cause disclosed")
	}
}
func TestCallbackDeadlineBridgeConservative(t *testing.T) {
	start := time.Now()
	pgNow := time.Unix(1000, 0)
	deadline, e := callbackLocalDeadline(start, start.Add(time.Second), pgNow, pgNow.Add(3*time.Second))
	if e != nil || deadline.Sub(start) != 2900*time.Millisecond {
		t.Fatal("call latency/margin")
	}
	if _, e = callbackLocalDeadline(start, start.Add(3*time.Second), pgNow, pgNow.Add(3*time.Second)); e == nil {
		t.Fatal("expired bridge")
	}
	if _, e = callbackLocalDeadline(start, start.Add(-time.Second), pgNow, pgNow.Add(3*time.Second)); e == nil {
		t.Fatal("backwards monotonic clock")
	}
}
func TestCallbackStoreRevocationAndInvalidPrepareZeroAcquire(t *testing.T) {
	complete, db, _ := callbackFixture(t)
	policy := cb.Policy{Binding: cb.Binding{Tenant: "tenant_cb", Destination: "destination_cb", URL: "https://callbacks.example.com/result", KeyRef: "vault/key_cb", PolicyVersion: 1, KeyVersion: 1}, CIDRs: []string{"93.184.216.0/24"}}
	store, e := NewCallbackStore(complete.runner, db.runtime, func(context.Context) error { return cb.ErrDenied }, []cb.Policy{policy})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = store.Claim(context.Background()); e == nil {
		t.Fatal("revoked claim")
	}
	if _, e = store.Prepare(context.Background(), cb.Prepared{}); e == nil {
		t.Fatal("invalid prepare")
	}
	if db.acquires != 0 {
		t.Fatal("denied input acquired DB")
	}
}
