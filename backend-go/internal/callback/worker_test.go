package callback

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type workerTestStore struct {
	*fixture
	recover      func(context.Context) (bool, error)
	claimHook    func(context.Context)
	completeHook func(context.Context)
}

func (s *workerTestStore) RecoverOneExpired(ctx context.Context) (bool, error) {
	s.log = append(s.log, "recover")
	return s.recover(ctx)
}
func (s *workerTestStore) Claim(ctx context.Context) (Delivery, bool, error) {
	if s.claimHook != nil {
		s.claimHook(ctx)
	}
	return s.fixture.Claim(ctx)
}
func (s *workerTestStore) Complete(ctx context.Context, c Completion) error {
	if s.completeHook != nil {
		s.completeHook(ctx)
	}
	return s.fixture.Complete(ctx, c)
}
func workerConfig() WorkerConfig {
	return WorkerConfig{Enabled: true, PollInterval: 100 * time.Millisecond, RecoveryTimeout: time.Second, RunTimeout: time.Second, ShutdownTimeout: time.Second}
}
func workerFixture(t *testing.T) (*fixture, *workerTestStore, *Worker) {
	t.Helper()
	f, sender := newFixture(t)
	store := &workerTestStore{fixture: f, recover: func(context.Context) (bool, error) { return false, nil }}
	sender.d.Store = store
	w, err := NewWorker(sender, workerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(context.Background()); err != nil {
			t.Error("worker failed to join")
		}
	})
	return f, store, w
}
func workerWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("bounded worker test timed out")
	}
}

func TestWorkerDefaultOffAndConfiguration(t *testing.T) {
	w, err := NewWorker(nil, WorkerConfig{})
	if err != nil || w.Health().State != "disabled" || w.Health().Active {
		t.Fatal("zero config not disabled")
	}
	if !errors.Is(w.Start(context.Background()), ErrWorkerDisabled) || w.Stop(nil) != nil {
		t.Fatal("disabled lifecycle")
	}
	f, sender := newFixture(t)
	if _, err := NewWorker(sender, workerConfig()); err != ErrWorkerConfiguration {
		t.Fatal("accepted untyped recovery store")
	}
	sender.d.Store = &workerTestStore{fixture: f}
	for _, mutate := range []func(*WorkerConfig){
		func(c *WorkerConfig) { c.PollInterval = 0 }, func(c *WorkerConfig) { c.PollInterval = time.Minute + 1 },
		func(c *WorkerConfig) { c.RecoveryTimeout = 0 }, func(c *WorkerConfig) { c.RecoveryTimeout = 10*time.Second + 1 },
		func(c *WorkerConfig) { c.RunTimeout = 0 }, func(c *WorkerConfig) { c.RunTimeout = 10*time.Second + 1 },
		func(c *WorkerConfig) { c.ShutdownTimeout = 0 }, func(c *WorkerConfig) { c.ShutdownTimeout = time.Minute + 1 },
	} {
		cfg := workerConfig()
		mutate(&cfg)
		if _, err := NewWorker(sender, cfg); err != ErrWorkerConfiguration {
			t.Fatal("unbounded config accepted")
		}
	}
	if _, err := NewWorker(nil, workerConfig()); err != ErrWorkerConfiguration {
		t.Fatal("nil sender")
	}
	if len(f.log) != 0 {
		t.Fatal("construction did work")
	}
}
func TestWorkerActualSenderRecoveryBeforeClaimAndJoin(t *testing.T) {
	f, store, w := workerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.recover = func(ctx context.Context) (bool, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("recovery unbounded")
		}
		return true, nil
	}
	store.claimHook = func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("claim unbounded")
		}
	}
	// An already-issued send still settles after cancellation, with unknown effect.
	f.sendHook = func(context.Context) { cancel() }
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	workerWait(t, w.done)
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"recover", "claim", "gate", "key", "dns", "prepare", "gate", "send", "complete"}
	if !reflect.DeepEqual(f.log, want) {
		t.Fatal(f.log)
	}
	h := w.Health()
	if h.State != "stopped" || h.Active || h.Recovered != 1 || h.Claimed != 1 || h.TransportInvoked != 1 || len(f.completed) != 1 || f.completed[0].Outcome != "delivery_unknown" {
		t.Fatal(h)
	}
	assertZeroKey(t, f)
	if w.Start(context.Background()) != ErrWorkerStarted {
		t.Fatal("restarted terminal worker")
	}
}
func TestWorkerUncertaintyLatchesNoNextClaimOrRecovery(t *testing.T) {
	for _, mode := range []string{"recovery", "claim_unknown", "claim_stale", "prepare", "settlement", "generation"} {
		t.Run(mode, func(t *testing.T) {
			f, store, w := workerFixture(t)
			switch mode {
			case "recovery":
				store.recover = func(context.Context) (bool, error) {
					return false, errors.New("PRIVATE https://credential.example/key")
				}
			case "claim_unknown":
				f.claimErr = ErrCommitUnknown
			case "claim_stale":
				f.claimErr = ErrStale
			case "prepare":
				f.prepareErr = ErrCommitUnknown
			case "settlement":
				f.completeErr = ErrCommitUnknown
			case "generation":
				f.failGate = 1
				f.gateError = ErrStale
			}
			if err := w.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			workerWait(t, w.done)
			h := w.Health()
			if h.State != "halted" || h.Active {
				t.Fatal(h)
			}
			blob, _ := json.Marshal(h)
			for _, secret := range []string{"PRIVATE", "https:", "tenant_fixture", "delivery_fixture", "key"} {
				if strings.Contains(string(blob), secret) {
					t.Fatal("health disclosed adapter data")
				}
			}
			n := 0
			for _, v := range f.log {
				if v == "recover" {
					n++
				}
			}
			if n != 1 || w.Start(context.Background()) != ErrWorkerStarted {
				t.Fatal("uncertainty retried")
			}
			if mode == "recovery" && len(f.log) != 1 {
				t.Fatal("claimed after recovery uncertainty")
			}
		})
	}
}
func TestWorkerStopBeforeStartAndConcurrentStartStop(t *testing.T) {
	_, _, w := workerFixture(t)
	if w.Start(nil) != ErrWorkerContext {
		t.Fatal("nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w.Start(ctx) != ErrWorkerContext {
		t.Fatal("closed context")
	}
	if w.Stop(context.Background()) != nil || w.Start(context.Background()) != ErrWorkerStarted {
		t.Fatal("stop before start")
	}
	if w.Health().Active {
		t.Fatal("started after stop")
	}
	_, store, w2 := workerFixture(t)
	entered := make(chan struct{})
	store.recover = func(ctx context.Context) (bool, error) { close(entered); <-ctx.Done(); return false, nil }
	var started atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w2.Start(context.Background()) == nil {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	workerWait(t, entered)
	if started.Load() != 1 {
		t.Fatal("overlapping start")
	}
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w2.Health()
			if w2.Stop(context.Background()) != nil {
				t.Error("concurrent stop")
			}
		}()
	}
	wg.Wait()
	if w2.Health().Active {
		t.Fatal("not joined")
	}
}
func TestWorkerStopTimeoutDoesNotAbandonCleanup(t *testing.T) {
	f, store, w := workerFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	store.completeHook = func(context.Context) { close(entered); <-release }
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	workerWait(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if w.Stop(ctx) != ErrWorkerJoinPending {
		t.Fatal("abandoned live cleanup")
	}
	h := w.Health()
	if !h.Active || h.State != "stopping" {
		t.Fatal("false joined health", h)
	}
	if w.Start(context.Background()) != ErrWorkerStarted {
		t.Fatal("started replacement loop")
	}
	once.Do(func() { close(release) })
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Health().Active || len(f.completed) != 1 {
		t.Fatal("cleanup did not finish")
	}
}
func TestWorkerOperationBudgetsAndCooperativeCancellation(t *testing.T) {
	for _, mode := range []string{"recovery", "claim"} {
		t.Run(mode, func(t *testing.T) {
			f, store, w := workerFixture(t)
			w.config.RecoveryTimeout = 10 * time.Millisecond
			w.config.RunTimeout = 10 * time.Millisecond
			if mode == "recovery" {
				store.recover = func(ctx context.Context) (bool, error) { <-ctx.Done(); return false, ctx.Err() }
			} else {
				store.claimHook = func(ctx context.Context) { <-ctx.Done(); f.claimErr = ctx.Err() }
			}
			if err := w.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			workerWait(t, w.done)
			if h := w.Health(); h.State != "halted" || h.Active || h.TransportInvoked != 0 {
				t.Fatal(h)
			}
		})
	}
}
func TestWorkerPacesAndRechecksAfterWait(t *testing.T) {
	f, store, w := workerFixture(t)
	f.found = false
	var calls int
	var first time.Time
	store.recover = func(ctx context.Context) (bool, error) {
		calls++
		if calls == 1 {
			first = time.Now()
			return false, nil
		}
		if time.Since(first) < w.config.PollInterval {
			t.Error("hot loop")
		}
		return false, ErrStale
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	workerWait(t, w.done)
	if !reflect.DeepEqual(f.log, []string{"recover", "claim", "recover"}) || w.Health().State != "halted" {
		t.Fatal("no closed-gate recheck before next claim", f.log)
	}
}

func TestWorkerStopDuringPrepareSettlesWithoutSend(t *testing.T) {
	f, _, w := workerFixture(t)
	entered := make(chan struct{})
	f.prepareHook = func(ctx context.Context) { close(entered); <-ctx.Done() }
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	workerWait(t, entered)
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.request != nil || len(f.completed) != 1 || f.completed[0].Code != "PERMIT_EXPIRED" || w.Health().TransportInvoked != 0 || w.Health().Active {
		t.Fatal("prepare cancellation lost settlement or invoked transport")
	}
	assertZeroKey(t, f)
}

func TestWorkerHealthCountersSaturate(t *testing.T) {
	n := ^uint64(0)
	workerIncrement(&n)
	if n != ^uint64(0) {
		t.Fatal("health counter wrapped")
	}
}
