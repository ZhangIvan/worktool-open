package callback

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type retryFixtureStore struct {
	*fixture
	settle      func(context.Context, RetryCompletionIntent) (RetryCompletionReceipt, error)
	settlements int
}

func (s *retryFixtureStore) SettleRetry(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
	s.settlements++
	return s.settle(ctx, i)
}

type retryObservingFixtureStore struct {
	*retryFixtureStore
	observe                          func(context.Context, RetryCompletionIntent) (RetryCompletionReceipt, error)
	observations, legacyObservations int
}

func (s *retryObservingFixtureStore) ObserveRetrySettlement(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
	s.observations++
	return s.observe(ctx, i)
}
func (s *retryObservingFixtureStore) ObserveCompletion(context.Context, Completion) error {
	s.legacyObservations++
	return nil
}

type retryLegacyObserverOnly struct {
	*retryFixtureStore
	observations int
}

func (s *retryLegacyObserverOnly) ObserveCompletion(context.Context, Completion) error {
	s.observations++
	return nil
}

func TestSenderRetrySettlementAndObservation(t *testing.T) {
	for _, mode := range []string{"ready", "dead_letter", "receiver_unknown", "no_send", "unknown_proven", "unknown_unproven", "unknown_no_observer", "unknown_legacy_observer_only", "cleanup_veto", "ordinary_error", "receipt_with_error", "wrong_intent", "invalid_state", "ready_without_time", "dead_letter_with_time", "ready_max_attempt", "negative_time", "unsafe_time", "late_settle", "late_observe", "observer_cleanup", "observer_wrong_intent", "terminal_legacy"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			f.status = 503
			var original RetryCompletionIntent
			base := &retryFixtureStore{fixture: f}
			observed := &retryObservingFixtureStore{retryFixtureStore: base}
			legacyOnly := &retryLegacyObserverOnly{retryFixtureStore: base}
			receipt := func(i RetryCompletionIntent) RetryCompletionReceipt {
				return RetryCompletionReceipt{Intent: i, State: "ready", NextAt: f.clock.Now().Add(time.Second)}
			}
			base.settle = func(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
				if ctx.Err() != nil {
					t.Fatal("settlement context not independent/live")
				}
				original = i
				r := receipt(i)
				switch mode {
				case "dead_letter":
					r.State = "dead_letter"
					r.NextAt = time.Time{}
				case "unknown_proven", "unknown_unproven", "unknown_no_observer", "unknown_legacy_observer_only", "late_observe", "observer_cleanup", "observer_wrong_intent":
					return r, ErrCommitUnknown
				case "cleanup_veto":
					return r, errors.Join(ErrCommitUnknown, ErrCleanupUnconfirmed)
				case "ordinary_error", "receipt_with_error":
					return r, ErrStale
				case "wrong_intent":
					r.Intent.Status = 429
				case "invalid_state":
					r.State = "delivered"
				case "ready_without_time":
					r.NextAt = time.Time{}
				case "dead_letter_with_time":
					r.State = "dead_letter"
				case "negative_time":
					r.NextAt = time.UnixMilli(-1)
				case "unsafe_time":
					r.NextAt = time.UnixMilli(9007199254740992)
				case "late_settle":
					f.clock.advance(time.Second)
				}
				return r, nil
			}
			observed.observe = func(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
				if i != original || ctx.Err() != nil {
					t.Fatal("lost immutable intent or live context")
				}
				r := receipt(i)
				switch mode {
				case "unknown_unproven":
					return r, ErrStale
				case "late_observe":
					f.clock.advance(time.Second)
				case "observer_cleanup":
					return r, ErrCleanupUnconfirmed
				case "observer_wrong_intent":
					r.Intent.Code = "DNS_UNAVAILABLE"
				}
				return r, nil
			}
			s.d.Store = observed
			switch mode {
			case "unknown_no_observer":
				s.d.Store = base
			case "unknown_legacy_observer_only":
				s.d.Store = legacyOnly
			case "receiver_unknown":
				f.transportErr = errors.New("response lost")
			case "no_send":
				f.dnsErr = errors.New("DNS unavailable")
			case "ready_max_attempt":
				f.claim.Handle.Attempt = 4
			case "terminal_legacy":
				f.status = 204
			}
			r := s.RunOne(context.Background())
			good := mode == "ready" || mode == "dead_letter" || mode == "receiver_unknown" || mode == "no_send" || mode == "unknown_proven" || mode == "terminal_legacy"
			if r.Reconcile == good {
				t.Fatalf("unexpected result %+v", r)
			}
			if mode == "terminal_legacy" {
				if base.settlements != 0 || len(f.completed) != 1 {
					t.Fatal("terminal completion changed")
				}
				return
			}
			if base.settlements != 1 || len(f.completed) != 0 || observed.legacyObservations != 0 || legacyOnly.observations != 0 {
				t.Fatal("fallback/retry/legacy observation")
			}
			wantObserve := mode == "unknown_proven" || mode == "unknown_unproven" || mode == "late_observe" || mode == "observer_cleanup" || mode == "observer_wrong_intent"
			if (observed.observations == 1) != wantObserve || observed.observations > 1 {
				t.Fatal("observation count")
			}
			if good {
				expected := "ready"
				if mode == "dead_letter" {
					expected = "dead_letter"
				}
				if r.State != expected || r.Outcome != original.Outcome || r.Code != original.Code {
					t.Fatal("reported non-authoritative state", r)
				}
				if mode == "receiver_unknown" && r.Outcome != "delivery_unknown" {
					t.Fatal("lost receiver uncertainty")
				}
			} else if r.State != "" || r.Outcome != "" || r.Code != "SETTLEMENT_UNCONFIRMED" {
				t.Fatal("reported unconfirmed receipt")
			}
			sends := 0
			for _, entry := range f.log {
				if entry == "send" {
					sends++
				}
			}
			if sends > 1 || r.TransportInvoked != (sends == 1) {
				t.Fatal("extra send or lost effect")
			}
		})
	}
}

func TestSenderRetryObservationFreshBudgetAfterCancellation(t *testing.T) {
	f, s := newFixture(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.sendHook = func(context.Context) { cancel() }
	var completeCtx, observeCtx context.Context
	base := &retryFixtureStore{fixture: f}
	observed := &retryObservingFixtureStore{retryFixtureStore: base}
	var exact RetryCompletionIntent
	base.settle = func(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
		completeCtx = ctx
		exact = i
		f.clock.advance(time.Second)
		return RetryCompletionReceipt{}, ErrCommitUnknown
	}
	observed.observe = func(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
		observeCtx = ctx
		if i != exact || ctx.Err() != nil || completeCtx.Err() == nil || f.clock.timers[len(f.clock.timers)-1].at.Sub(f.clock.Now()) != time.Second {
			t.Fatal("not exact fresh observation")
		}
		return RetryCompletionReceipt{Intent: i, State: "ready", NextAt: f.clock.Now().Add(time.Second)}, nil
	}
	s.d.Store = observed
	r := s.RunOne(parent)
	if r.Reconcile || r.Outcome != "delivery_unknown" || !r.TransportInvoked || observeCtx == nil || observeCtx.Err() == nil || base.settlements != 1 || observed.observations != 1 || !reflect.DeepEqual(f.log, []string{"claim", "gate", "key", "dns", "prepare", "gate", "send"}) {
		t.Fatal(r, f.log)
	}
}

type retryWorkerStore struct{ *retryObservingFixtureStore }

func (s *retryWorkerStore) RecoverOneExpired(context.Context) (bool, error) {
	s.log = append(s.log, "recover")
	return false, nil
}
func TestWorkerRetryObservationJoinsAndUnconfirmedHalts(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "unconfirmed"}[confirmed], func(t *testing.T) {
			f, sender := newFixture(t)
			f.status = 503
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			base := &retryFixtureStore{fixture: f}
			observer := &retryObservingFixtureStore{retryFixtureStore: base}
			store := &retryWorkerStore{observer}
			base.settle = func(context.Context, RetryCompletionIntent) (RetryCompletionReceipt, error) {
				return RetryCompletionReceipt{}, ErrCommitUnknown
			}
			observer.observe = func(ctx context.Context, i RetryCompletionIntent) (RetryCompletionReceipt, error) {
				close(entered)
				<-release
				if !confirmed {
					return RetryCompletionReceipt{}, ErrStale
				}
				return RetryCompletionReceipt{Intent: i, State: "ready", NextAt: f.clock.Now().Add(time.Second)}, nil
			}
			sender.d.Store = store
			w, e := NewWorker(sender, workerConfig())
			if e != nil {
				t.Fatal(e)
			}
			if e = w.Start(context.Background()); e != nil {
				t.Fatal(e)
			}
			workerWait(t, entered)
			stopped, cancel := context.WithCancel(context.Background())
			cancel()
			if e = w.Stop(stopped); e != ErrWorkerJoinPending || !w.Health().Active {
				t.Fatal("observation abandoned", e)
			}
			close(release)
			released = true
			if e = w.Stop(context.Background()); e != nil {
				t.Fatal(e)
			}
			h := w.Health()
			expected := "halted"
			if confirmed {
				expected = "stopped"
			}
			if h.State != expected || h.Active || h.Claimed != 1 || h.TransportInvoked != 1 || base.settlements != 1 || observer.observations != 1 || len(f.completed) != 0 || observer.legacyObservations != 0 {
				t.Fatal("unexpected worker settlement", h)
			}
			if w.Start(context.Background()) != ErrWorkerStarted {
				t.Fatal("restarted worker")
			}
		})
	}
}
