package gateway

import (
	"context"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"testing"
	"time"
)

// These cases remain inside CB03's existing strict isolated-CI allowlist. They
// are authored SQL/lifecycle cases, NOT_RUN locally. No new top-level PG group.
func callbackWorkerIntegrationCases(t *testing.T) {
	gatewayGate(t)
	t.Run("worker_selection_boundaries", func(t *testing.T) {
		f := newGWWithStoreConstructor(t, callbackSyntheticStore)
		store, policy := callbackSetup(t, f)
		callbackCompletedCommand(t, f)
		d, ok, err := store.Claim(f.ctx)
		if err != nil || !ok {
			t.Fatal("claim")
		}
		if ok, err := store.RecoverOneExpired(f.ctx); err != nil || ok {
			t.Fatal("recovered unexpired lease")
		}
		callbackWorkerExpire(t, f, d.Handle.DeliveryID)
		excluded := policy
		excluded.Binding.Destination = "unconfigured_destination"
		other := callbackWorkerStore(t, f, excluded, "worker_demo", "boot_worker")
		if ok, err := other.RecoverOneExpired(f.ctx); err != nil || ok {
			t.Fatal("selection escaped copied policies")
		}
		// Controlled test-only epoch mismatch. Selection cannot reconcile other epochs.
		execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET lease_recovery_epoch='other_epoch' WHERE delivery_id=$1`, d.Handle.DeliveryID)
		if ok, err := store.RecoverOneExpired(f.ctx); err != nil || ok {
			t.Fatal("cross-generation candidate selected")
		}
		if err := store.RecoverExpired(f.ctx, d.Handle.Tenant, d.Handle.DeliveryID); err == nil {
			t.Fatal("explicit recovery crossed generation")
		}
		execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET lease_recovery_epoch='recovery_demo' WHERE delivery_id=$1`, d.Handle.DeliveryID)
		held, err := f.owner.Begin(f.ctx)
		if err != nil {
			t.Fatal("scope holder")
		}
		defer held.Rollback(f.ctx)
		if _, err = held.Exec(f.ctx, `SELECT scope_id FROM scope_limits WHERE scope_kind='tenant' AND tenant='tenant_demo' FOR UPDATE`); err != nil {
			t.Fatal("scope lock")
		}
		if ok, err := store.RecoverOneExpired(f.ctx); err != nil || ok {
			t.Fatal("busy scope should cleanly skip")
		}
		if err = held.Rollback(f.ctx); err != nil {
			t.Fatal("release scope")
		}
		if ok, err := store.RecoverOneExpired(f.ctx); err != nil || !ok {
			t.Fatal("expired selection did not commit")
		}
		if ok, err := store.RecoverOneExpired(f.ctx); err != nil || ok {
			t.Fatal("settled row reported recovered twice")
		}
		if f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 || f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome='lease_lost'`) != 1 {
			t.Fatal("selection recovery graph")
		}
	})
	for _, prepared := range []bool{false, true} {
		name := "worker_same_epoch_unprepared"
		if prepared {
			name = "worker_same_epoch_prepared_unknown"
		}
		t.Run(name, func(t *testing.T) {
			f := newGWWithStoreConstructor(t, callbackSyntheticStore)
			store, policy := callbackSetup(t, f)
			callbackCompletedCommand(t, f)
			d, ok, err := store.Claim(f.ctx)
			if err != nil || !ok {
				t.Fatal("claim")
			}
			if prepared {
				if _, err = store.Prepare(f.ctx, callbackPrepared(d)); err != nil {
					t.Fatal("prepare")
				}
			}
			callbackWorkerExpire(t, f, d.Handle.DeliveryID)
			// New boot/owner within the SAME externally supplied epoch. No PITR or
			// cross-generation recovery is claimed and no identity is auto-provisioned.
			execOwner(t, f.owner, f.ctx, `UPDATE runtime_instances SET state='stopped' WHERE instance_id='worker_demo';INSERT INTO runtime_instances(instance_id,roles,boot_id,recovery_epoch,state) VALUES('worker_next',ARRAY['worker'],'boot_next','recovery_demo','active')`)
			successor := callbackWorkerStore(t, f, policy, "worker_next", "boot_next")
			key := []byte(randHex(t, 16))
			joined := true // no loop yet; constructor failure can clear the key
			t.Cleanup(func() {
				if joined {
					clear(key)
				}
			})
			workerCtx, stopAfterSettlement := context.WithCancel(f.ctx)
			t.Cleanup(stopAfterSettlement)
			coordinated := callbackWorkerJoinStore{successor, stopAfterSettlement}
			sender, err := cb.New(cb.Dependencies{Store: coordinated, Clock: callbackWallClock{}, Gate: callbackLocalGate{}, DNS: callbackFixtureDNS{}, Keys: callbackFixtureKey{policy.Binding, key}, Transport: callbackWorkerSyntheticTransport{key}}, []cb.Policy{policy})
			if err != nil {
				t.Fatal("sender")
			}
			worker, err := cb.NewWorker(sender, cb.WorkerConfig{Enabled: true, PollInterval: 100 * time.Millisecond, RecoveryTimeout: 2 * time.Second, RunTimeout: 3 * time.Second, ShutdownTimeout: 5 * time.Second})
			if err != nil {
				t.Fatal("worker")
			}
			t.Cleanup(func() {
				if err := worker.Stop(context.Background()); err != nil {
					f.retainStorage() // gateway cleanup cannot override this veto
					t.Error("worker join pending; pool, schema and key retained")
					return
				}
				joined = true
			})
			joined = false
			if err = worker.Start(workerCtx); err != nil {
				t.Fatal("start")
			}
			waitCondition(t, f, func() bool { return !worker.Health().Active })
			if err = worker.Stop(f.ctx); err != nil {
				t.Fatal("joined shutdown")
			}
			joined = true
			h := worker.Health()
			if f.count(t, `SELECT count(*) FROM callback_outbox WHERE state='delivered'`) != 1 {
				t.Fatal("delivery not committed")
			}
			if h.Active || h.State != "stopped" || h.Recovered != 1 || h.Claimed != 1 || h.TransportInvoked != 1 {
				t.Fatal("worker lifecycle evidence")
			}
			outcome := "lease_lost"
			if prepared {
				outcome = "delivery_unknown"
			}
			if f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome=$1`, outcome) != 1 || f.count(t, `SELECT count(*) FROM callback_attempts WHERE outcome='delivered' AND owner_instance='worker_next'`) != 1 || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 0 {
				t.Fatal("successor graph")
			}
			if err = store.Complete(f.ctx, cb.Completion{Handle: d.Handle, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}); err == nil {
				t.Fatal("old owner settled successor")
			}
		})
	}
}
func callbackWorkerExpire(t *testing.T, f *gwFixture, id string) {
	t.Helper()
	execOwner(t, f.owner, f.ctx, `WITH n AS (SELECT clock_timestamp()-interval '1 second' AS expired), u AS (UPDATE callback_outbox SET claimed_at=n.expired-interval '1 second',hard_deadline_at=n.expired,lease_until=n.expired FROM n WHERE tenant='tenant_demo' AND delivery_id=$1 RETURNING lease_until) UPDATE scope_slots SET lease_until=u.lease_until FROM u WHERE tenant='tenant_demo' AND delivery_id=$1`, id)
}
func callbackWorkerStore(t *testing.T, f *gwFixture, policy cb.Policy, instance, boot string) *pg.CallbackStore {
	t.Helper()
	runtime, err := pg.RuntimeAuthorityFromTrusted(instance, boot, "recovery_demo")
	if err != nil {
		t.Fatal("runtime")
	}
	runner, err := pg.NewRunner(pg.PoolAdapter{Pool: f.pool}, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 3 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if err != nil {
		t.Fatal("runner")
	}
	store, err := pg.NewCallbackStore(runner, runtime, func(context.Context) error { return nil }, []cb.Policy{policy})
	if err != nil {
		t.Fatal("store")
	}
	return store
}

// No socket is used by this transport. Existing CB01/CB05 own HTTP evidence.
// This is test-only verification of the real Sender's request and HMAC bytes.
type callbackWorkerSyntheticTransport struct{ key []byte }

func (t callbackWorkerSyntheticTransport) Send(ctx context.Context, r cb.Request) (cb.Response, error) {
	if ctx.Err() != nil || r.URL != callbackFixtureURL || !security.Verify(t.key, r.EventID, r.Timestamp, r.Signature, r.Body, time.Now().UnixMilli()) {
		return cb.Response{}, cb.ErrTransportPolicy
	}
	return cb.Response{Status: 204}, nil
}

// Test-only coordination: cancel only after actual PG Complete and all its
// cleanup return, so successful shutdown cannot race the next recovery cycle.
// Embedding preserves the real Store and same-generation recovery implementation.
type callbackWorkerJoinStore struct {
	*pg.CallbackStore
	stop context.CancelFunc
}

func (s callbackWorkerJoinStore) Complete(ctx context.Context, c cb.Completion) error {
	err := s.CallbackStore.Complete(ctx, c)
	if err == nil && c.State == "delivered" {
		s.stop()
	}
	return err
}
