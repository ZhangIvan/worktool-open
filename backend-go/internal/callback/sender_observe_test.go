package callback

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type observingFixtureStore struct {
	*fixture
	observe  func(context.Context, Completion) error
	complete func(context.Context, Completion) error
}

func (s *observingFixtureStore) ObserveCompletion(ctx context.Context, c Completion) error {
	s.log = append(s.log, "observe")
	return s.observe(ctx, c)
}
func (s *observingFixtureStore) Complete(ctx context.Context, c Completion) error {
	if s.complete != nil {
		return s.complete(ctx, c)
	}
	return s.fixture.Complete(ctx, c)
}

func TestSenderObservesOnlyUnknownCompletionOnce(t *testing.T) {
	for _, mode := range []string{"delivered", "ready", "dead_letter", "receiver_unknown", "no_send", "unresolved", "read_unknown", "stale", "expired_observation", "original_cleanup_failure", "ordinary_complete_failure", "claim_unknown", "claim_unknown_cleanup", "prepare_unknown"} {
		t.Run(mode, func(t *testing.T) {
			f, s := newFixture(t)
			f.completeErr = ErrCommitUnknown
			observed := 0
			var observedCtx context.Context
			store := &observingFixtureStore{fixture: f}
			store.observe = func(ctx context.Context, c Completion) error {
				observed++
				observedCtx = ctx
				if ctx.Err() != nil || len(f.completed) != 1 || c != f.completed[0] {
					t.Fatal("observation lost exact intent or live independent context")
				}
				switch mode {
				case "unresolved":
					return errors.New("PRIVATE proof absent")
				case "read_unknown":
					return ErrCommitUnknown
				case "stale":
					return ErrStale
				case "expired_observation":
					f.clock.advance(time.Second)
				}
				return nil
			}
			s.d.Store = store
			switch mode {
			case "ready":
				f.status = 503
			case "dead_letter":
				f.status = 503
				f.claim.Handle.Attempt = 4
			case "receiver_unknown":
				f.transportErr = errors.New("response lost")
			case "no_send":
				f.dnsErr = errors.New("DNS failed")
			case "original_cleanup_failure":
				f.completeErr = errors.Join(ErrCommitUnknown, ErrCleanupUnconfirmed)
			case "ordinary_complete_failure":
				f.completeErr = errors.New("PRIVATE complete")
			case "claim_unknown":
				f.claimErr = ErrCommitUnknown
			case "claim_unknown_cleanup":
				f.claimErr = errors.Join(ErrCommitUnknown, ErrCleanupUnconfirmed)
			case "prepare_unknown":
				f.prepareErr = ErrCommitUnknown
			}
			r := s.RunOne(context.Background())
			wantObservation := mode != "original_cleanup_failure" && mode != "ordinary_complete_failure" && mode != "claim_unknown" && mode != "claim_unknown_cleanup" && mode != "prepare_unknown"
			confirmed := mode == "delivered" || mode == "ready" || mode == "dead_letter" || mode == "receiver_unknown" || mode == "no_send"
			if (observed == 1) != wantObservation || observed > 1 || r.Reconcile == confirmed {
				t.Fatal("observation eligibility/result", r, observed)
			}
			if confirmed {
				c := f.completed[0]
				if r.State != c.State || r.Outcome != c.Outcome || r.Code != c.Code {
					t.Fatal("changed result intent", r)
				}
				if mode == "receiver_unknown" && (r.Outcome != "delivery_unknown" || r.State != "ready") {
					t.Fatal("confused DB confirmation with receiver effect")
				}
			} else if r.State != "" || r.Outcome != "" {
				t.Fatal("reported unconfirmed state")
			}
			if observedCtx != nil && observedCtx.Err() == nil {
				t.Fatal("observation context leaked")
			}
			counts := map[string]int{}
			for _, v := range f.log {
				counts[v]++
			}
			for _, v := range []string{"claim", "prepare", "send", "complete", "observe"} {
				if counts[v] > 1 {
					t.Fatal("retried", v)
				}
			}
			if r.TransportInvoked != (counts["send"] == 1) {
				t.Fatal("lost possible effect")
			}
		})
	}
}

func TestSenderObservationGetsFreshBudgetAfterCancellation(t *testing.T) {
	f, s := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.sendHook = func(context.Context) { cancel() }
	var completeCtx, observeCtx context.Context
	store := &observingFixtureStore{fixture: f}
	store.complete = func(ctx context.Context, c Completion) error {
		completeCtx = ctx
		if err := f.Complete(ctx, c); err != nil {
			t.Fatal(err)
		}
		f.clock.advance(time.Second)
		return ErrCommitUnknown
	}
	store.observe = func(ctx context.Context, c Completion) error {
		observeCtx = ctx
		if completeCtx.Err() == nil || ctx.Err() != nil || c != f.completed[0] {
			t.Fatal("not a fresh bounded exact observation")
		}
		last := f.clock.timers[len(f.clock.timers)-1]
		if last.at.Sub(f.clock.Now()) != time.Second {
			t.Fatal("observation budget")
		}
		return nil
	}
	s.d.Store = store
	r := s.RunOne(ctx)
	if r.Reconcile || r.Outcome != "delivery_unknown" || !r.TransportInvoked || observeCtx == nil || observeCtx.Err() == nil {
		t.Fatal(r)
	}
	if !reflect.DeepEqual(f.log, []string{"claim", "gate", "key", "dns", "prepare", "gate", "send", "complete", "observe"}) {
		t.Fatal(f.log)
	}
}

type observingWorkerStore struct {
	*workerTestStore
	observe func(context.Context, Completion) error
}

func (s *observingWorkerStore) ObserveCompletion(ctx context.Context, c Completion) error {
	s.log = append(s.log, "observe")
	return s.observe(ctx, c)
}

func TestWorkerObservationJoinsAndUnresolvedStaysHalted(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "unresolved"}[confirmed], func(t *testing.T) {
			f, sender := newFixture(t)
			f.completeErr = ErrCommitUnknown
			entered, release := make(chan struct{}), make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()
			store := &observingWorkerStore{workerTestStore: &workerTestStore{fixture: f, recover: func(context.Context) (bool, error) { return false, nil }}}
			store.observe = func(ctx context.Context, c Completion) error {
				close(entered)
				<-release
				if confirmed {
					return nil
				}
				return ErrStale
			}
			sender.d.Store = store
			w, err := NewWorker(sender, workerConfig())
			if err != nil {
				t.Fatal(err)
			}
			if err = w.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			workerWait(t, entered)
			cancelled, stop := context.WithCancel(context.Background())
			stop()
			if err = w.Stop(cancelled); err != ErrWorkerJoinPending || !w.Health().Active {
				t.Fatal("stop abandoned observation")
			}
			close(release)
			released = true
			if err = w.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			h := w.Health()
			want := "halted"
			if confirmed {
				want = "stopped"
			}
			if h.State != want || h.Active || h.Claimed != 1 || h.TransportInvoked != 1 || len(f.completed) != 1 {
				t.Fatal(h)
			}
			if w.Start(context.Background()) != ErrWorkerStarted {
				t.Fatal("restarted terminal worker")
			}
			counts := map[string]int{}
			for _, v := range f.log {
				counts[v]++
			}
			for _, v := range []string{"recover", "claim", "prepare", "send", "complete", "observe"} {
				if counts[v] != 1 {
					t.Fatal("unexpected calls", counts)
				}
			}
		})
	}
}
