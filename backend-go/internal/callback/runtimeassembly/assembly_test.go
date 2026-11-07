package runtimeassembly

import (
	"context"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/composition"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActualOwnerRetryObservationClosureAndJoin(t *testing.T) {
	p, ds, r := testConfig(t)
	script := newAssemblyScript(t, time.Now().UTC())
	d, keys, transport := testDeps(t, script)
	var released atomic.Int32
	d.Release = func(context.Context) error {
		if script.snapshot().Releases[5] != 1 {
			return errors.New("release before actual cleanup")
		}
		released.Add(1)
		return nil
	}
	journal := &testJournal{}
	cap := testCapability(t, p, ds, r, journal)
	owner, err := newOwner(p, ds, r, d, cap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		script.releaseObservation()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := owner.Stop(ctx); err != nil {
			t.Error("fixture owner not joined", err)
		}
	})
	if err := owner.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	healthDone, healthExit := make(chan struct{}), make(chan struct{})
	var healthViolation atomic.Bool
	go func() {
		defer close(healthExit)
		for {
			select {
			case <-healthDone:
				return
			default:
			}
			h := owner.Health()
			if (h.ResourcesReleased || h.State == "joined") && h.Active {
				healthViolation.Store(true)
			}
			runtime.Gosched()
		}
	}()
	t.Cleanup(func() {
		close(healthDone)
		select {
		case <-healthExit:
		case <-time.After(time.Second):
			t.Error("health reader did not join")
		}
		if healthViolation.Load() {
			t.Error("joined health retained stale Active")
		}
	})

	select {
	case <-script.observationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("observation barrier not reached")
	}
	before := script.snapshot()
	if before.Phase != "scripted-completed-retry" || before.Execs[5] != 0 || transport.calls.Load() != 1 {
		t.Fatal("unexpected scripted observation state")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if owner.Stop(cancelled) != ErrPending {
		t.Fatal("held actual cleanup reported joined")
	}
	h := owner.Health()
	if !h.Active || !h.JoinPending || h.ResourcesReleased || released.Load() != 0 || cap.lease.live.Load() {
		t.Fatal("pending join lost dependencies or reopened gate")
	}
	script.releaseObservation()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := owner.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	script.assertFinished(t)
	after := script.snapshot()
	h = owner.Health()
	if after.Phase != before.Phase || after.Execs != before.Execs || h.Active || h.JoinPending || !h.ResourcesReleased || h.State != "halted" || h.Code != "RECONCILIATION_REQUIRED" || released.Load() != 1 || transport.calls.Load() != 1 {
		t.Fatal("closure failed to preserve halt or join")
	}
	for _, b := range keys.bytes {
		if b != 0 {
			t.Fatal("transferred key not cleared after actual Sender return")
		}
	}
	if owner.Stop(nil) != nil || released.Load() != 1 || owner.Start(context.Background()) != ErrConsumed {
		t.Fatal("terminal owner restarted/released twice")
	}
	// A new object/config/epoch is never a way around the stable test scope.
	for _, variant := range []string{"same", "epoch", "boot", "owner", "configuration"} {
		rr := r
		pp, dd := p, ds
		if variant == "configuration" {
			var err error
			pp, err = composition.Parse([]byte(strings.Replace(testManifest, "revision_001", "revision_002", 1)), "")
			if err != nil {
				t.Fatal(err)
			}
			desc, err := registration.Decode([]byte(strings.Replace(testDescriptor, "revision_001", "revision_002", 1)))
			if err != nil {
				t.Fatal(err)
			}
			dd = []registration.Descriptor{desc}
		}
		switch variant {
		case "epoch":
			rr.Epoch = "other_epoch"
		case "boot":
			rr.Boot = "other_boot"
		case "owner":
			rr.Instance = "other_worker"
		}
		pool := &noPool{}
		deps, _, _ := testDeps(t, pool)
		nextCap := testCapability(t, pp, dd, rr, journal)
		next, e := newOwner(pp, dd, rr, deps, nextCap)
		if e != nil {
			t.Fatal(e)
		}
		if next.Start(context.Background()) != ErrBlocked || pool.calls.Load() != 0 || next.Health().State != "halted" {
			t.Fatal("replacement bypassed occupied scope")
		}
		if next.Stop(context.Background()) != nil {
			t.Fatal("replacement cleanup")
		}
	}
}

func TestRecordFailureUnknownAndReentryNeverClearOwnership(t *testing.T) {
	for _, mode := range []string{"error", "wrong_receipt", "expired_receipt", "reentrant"} {
		t.Run(mode, func(t *testing.T) {
			p, ds, r := testConfig(t)
			pool := &noPool{}
			d, _, _ := testDeps(t, pool)
			journal := &testJournal{occupied: true}
			c := testCapability(t, p, ds, r, journal)
			var owner *Owner
			calls := 0
			c.record = func(ctx context.Context, i incidentRecord) (incidentRecord, error) {
				calls++
				switch mode {
				case "error":
					return i, errors.New("PRIVATE")
				case "wrong_receipt":
					i.Identity = "other"
					return i, nil
				case "expired_receipt":
					<-ctx.Done()
					return i, nil
				case "reentrant":
					if owner.Stop(context.Background()) != ErrPending {
						t.Error("recorder self-wait")
					}
					return i, nil
				}
				panic("unreachable")
			}
			var e error
			owner, e = newOwner(p, ds, r, d, c)
			if e != nil {
				t.Fatal(e)
			}
			if owner.Start(context.Background()) != ErrBlocked {
				t.Fatal("occupied scope admitted")
			}
			want := ErrRecord
			if mode == "reentrant" {
				want = nil
			}
			if owner.Stop(context.Background()) != want || owner.Stop(context.Background()) != want || calls != 1 || !journal.occupied || pool.calls.Load() != 0 {
				t.Fatal("record uncertainty cleared/retried")
			}
		})
	}
}

// Same six-transaction evidence, without revocation during cleanup. This makes
// the negative scenario discriminate gate loss from a malformed proof fixture.
func TestActualOwnerRetryObservationPositiveControl(t *testing.T) {
	_, ds, r := testConfig(t)
	p, e := composition.Parse([]byte(strings.Replace(testManifest, `"poll_ms":100`, `"poll_ms":60000`, 1)), "")
	if e != nil {
		t.Fatal(e)
	}
	script := newAssemblyScript(t, time.Now().UTC())
	d, _, transport := testDeps(t, script)
	journal := &testJournal{}
	cap := testCapability(t, p, ds, r, journal)
	owner, e := newOwner(p, ds, r, d, cap)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		script.releaseObservation()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if owner.Stop(ctx) != nil {
			t.Error("positive owner not joined")
		}
	})
	if owner.Start(context.Background()) != nil {
		t.Fatal("positive start")
	}
	select {
	case <-script.observationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("positive barrier")
	}
	script.releaseObservation()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for owner.worker.Health().TransportInvoked != 1 {
		select {
		case <-timeout.C:
			t.Fatal("Sender did not return")
		case <-tick.C:
		}
	}
	if !cap.lease.live.Load() {
		t.Fatal("positive generation closed before observation returned")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if owner.Stop(ctx) != nil {
		t.Fatal("positive join")
	}
	script.assertFinished(t)
	h := owner.Health()
	if h.State != "joined" || h.Code != "JOINED" || h.Active || !h.ResourcesReleased || transport.calls.Load() != 1 {
		t.Fatal("same proof did not confirm without gate closure")
	}
}
