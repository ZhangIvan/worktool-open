package gateway

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Children of existing CB02. Local runs skip before constructing any resources.
func callbackObservationIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, mode := range []string{"unfinished", "confirmed", "missing_audit", "conflicting_audit", "conflicting_status", "successor_fence", "new_generation", "busy_audit"} {
		t.Run("observe_"+mode, func(t *testing.T) {
			f := newGWWithStoreConstructor(t, callbackSyntheticStore)
			store, _ := callbackSetup(t, f)
			callbackCompletedCommand(t, f)
			d, ok, err := store.Claim(f.ctx)
			if err != nil || !ok {
				t.Fatal("claim")
			}
			if _, err = store.Prepare(f.ctx, callbackPrepared(d)); err != nil {
				t.Fatal("prepare")
			}
			c := cb.Completion{Handle: d.Handle, Outcome: "delivered", State: "delivered", Code: "HTTP_ACKNOWLEDGED", Status: 204}
			if mode != "unfinished" {
				if err = store.Complete(f.ctx, c); err != nil {
					t.Fatal("complete")
				}
			}
			switch mode {
			case "missing_audit":
				execOwner(t, f.owner, f.ctx, `DELETE FROM audit WHERE tenant=$1 AND action='callback.completed' AND target=$2`, d.Handle.Tenant, d.Handle.DeliveryID)
			case "conflicting_audit":
				execOwner(t, f.owner, f.ctx, `UPDATE audit SET created=created+1 WHERE tenant=$1 AND action='callback.completed' AND target=$2`, d.Handle.Tenant, d.Handle.DeliveryID)
			case "conflicting_status":
				c.Status = 200
			case "successor_fence":
				execOwner(t, f.owner, f.ctx, `UPDATE callback_outbox SET fence=fence+1 WHERE tenant=$1 AND delivery_id=$2`, d.Handle.Tenant, d.Handle.DeliveryID)
			case "new_generation":
				execOwner(t, f.owner, f.ctx, `UPDATE runtime_instances SET recovery_epoch='new_generation' WHERE instance_id='worker_demo'`)
			}
			ctx := f.ctx
			if mode == "busy_audit" {
				held, e := f.owner.Begin(f.ctx)
				if e != nil {
					t.Fatal("audit holder")
				}
				defer func() {
					cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					if err := held.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
						t.Error("audit holder cleanup failed")
					}
				}()
				if _, e = held.Exec(f.ctx, `SELECT id FROM audit WHERE tenant=$1 AND action='callback.completed' AND target=$2 FOR UPDATE`, d.Handle.Tenant, d.Handle.DeliveryID); e != nil {
					t.Fatal("hold audit")
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(f.ctx, 100*time.Millisecond)
				defer cancel()
			}
			// xmin snapshots prove that observation did not rewrite even identical rows.
			snapshot := func() string {
				var value string
				err := f.owner.QueryRow(f.ctx, `SELECT jsonb_build_array(
     (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(o)) ORDER BY delivery_id) FROM callback_outbox o),
     (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(a)) ORDER BY delivery_id,fence) FROM callback_attempts a),
     (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(s)) ORDER BY scope_kind,scope_id,slot_no) FROM scope_slots s),
     (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(a)) ORDER BY id) FROM audit a WHERE action='callback.completed'),
     (SELECT jsonb_agg(jsonb_build_array(xmin::text,to_jsonb(s)) ORDER BY scope_kind,scope_id) FROM scope_limits s)
    )::text`).Scan(&value)
				if err != nil {
					t.Fatal("snapshot")
				}
				return value
			}
			before := snapshot()
			err = store.ObserveCompletion(ctx, c)
			if (err == nil) != (mode == "confirmed") {
				t.Fatal("observation proof classification")
			}
			if after := snapshot(); after != before {
				t.Fatal("observation changed durable graph")
			}
			if mode == "unfinished" && f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != 2 {
				t.Fatal("unfinished observation released old slots")
			}
		})
	}
}

// Test-only transaction fault: deterministic error after real COMMIT, or after
// real ROLLBACK, of the completion DML only. This is not a TCP/crash fault proxy.
// Claim, Prepare and observation use the real Runner/store without injection.
type callbackCompletionFaultPool struct {
	base     pg.Pool
	rollback bool
	injected atomic.Bool
}

func (p *callbackCompletionFaultPool) Acquire(ctx context.Context) (pg.Connection, error) {
	c, err := p.base.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return callbackCompletionFaultConn{Connection: c, pool: p}, nil
}

type callbackCompletionFaultConn struct {
	pg.Connection
	pool *callbackCompletionFaultPool
}

func (c callbackCompletionFaultConn) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	tx, err := c.Connection.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &callbackCompletionFaultTx{Tx: tx, pool: c.pool}, nil
}

type callbackCompletionFaultTx struct {
	pgx.Tx
	pool       *callbackCompletionFaultPool
	completion bool
}

func (tx *callbackCompletionFaultTx) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(q, "INSERT INTO audit") && strings.Contains(q, "'callback.completed'") {
		tx.completion = true
	}
	return tx.Tx.Exec(ctx, q, args...)
}
func (tx *callbackCompletionFaultTx) Commit(ctx context.Context) error {
	if tx.completion && tx.pool.injected.CompareAndSwap(false, true) {
		var err error
		if tx.pool.rollback {
			err = tx.Tx.Rollback(ctx)
		} else {
			err = tx.Tx.Commit(ctx)
		}
		if err != nil {
			return err
		}
		return errors.New("synthetic completion acknowledgement lost")
	}
	return tx.Tx.Commit(ctx)
}

type callbackObservationCountingStore struct {
	*pg.CallbackStore
	completes, observations int
}

func (s *callbackObservationCountingStore) Complete(ctx context.Context, c cb.Completion) error {
	s.completes++
	return s.CallbackStore.Complete(ctx, c)
}
func (s *callbackObservationCountingStore) ObserveCompletion(ctx context.Context, c cb.Completion) error {
	s.observations++
	return s.CallbackStore.ObserveCompletion(ctx, c)
}

type callbackObservationTransport struct {
	callbackWorkerSyntheticTransport
	calls int
}

func (s *callbackObservationTransport) Send(ctx context.Context, r cb.Request) (cb.Response, error) {
	s.calls++
	return s.callbackWorkerSyntheticTransport.Send(ctx, r)
}

// Children of existing CB05, with no additional receiver/socket. Actual Sender,
// Runner, completion COMMIT fault and observer read the real PostgreSQL graph.
func callbackObservationCommitIntegrationCases(t *testing.T) {
	gatewayGate(t)
	for _, rollback := range []bool{false, true} {
		name := "observe_after_real_commit"
		if rollback {
			name = "observe_after_real_rollback"
		}
		t.Run(name, func(t *testing.T) {
			f := newGWWithStoreConstructor(t, callbackSyntheticStore)
			_, policy := callbackSetup(t, f)
			callbackCompletedCommand(t, f)
			pool := &callbackCompletionFaultPool{base: pg.PoolAdapter{Pool: f.pool}, rollback: rollback}
			runner, err := pg.NewRunner(pool, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 3 * time.Second, Commit: time.Second, Cleanup: time.Second})
			if err != nil {
				t.Fatal("runner")
			}
			runtime, err := pg.RuntimeAuthorityFromTrusted("worker_demo", "boot_worker", "recovery_demo")
			if err != nil {
				t.Fatal("runtime")
			}
			actual, err := pg.NewCallbackStore(runner, runtime, func(context.Context) error { return nil }, []cb.Policy{policy})
			if err != nil {
				t.Fatal("store")
			}
			store := &callbackObservationCountingStore{CallbackStore: actual}
			key := []byte(randHex(t, 16))
			defer clear(key)
			transport := &callbackObservationTransport{callbackWorkerSyntheticTransport: callbackWorkerSyntheticTransport{key: key}}
			sender, err := cb.New(cb.Dependencies{Store: store, Clock: callbackWallClock{}, Gate: callbackLocalGate{}, DNS: callbackFixtureDNS{}, Keys: callbackFixtureKey{policy.Binding, key}, Transport: transport}, []cb.Policy{policy})
			if err != nil {
				t.Fatal("sender")
			}
			r := sender.RunOne(f.ctx)
			if !pool.injected.Load() || transport.calls != 1 || store.completes != 1 || store.observations != 1 || !r.TransportInvoked || r.Reconcile != rollback {
				t.Fatal("commit observation orchestration")
			}
			state, finished, slots, audits := "delivered", int64(1), int64(0), int64(1)
			if rollback {
				state, finished, slots, audits = "leased", 0, 2, 0
				if r.State != "" {
					t.Fatal("unconfirmed state")
				}
			} else if r.State != "delivered" || r.Outcome != "delivered" {
				t.Fatal("durable result")
			}
			if f.count(t, `SELECT count(*) FROM callback_outbox WHERE state=$1`, state) != 1 || f.count(t, `SELECT count(*) FROM callback_attempts WHERE finished_at IS NOT NULL`) != finished || f.count(t, `SELECT count(*) FROM callback_attempts`) != 1 || f.count(t, `SELECT count(*) FROM scope_slots WHERE owner_instance IS NOT NULL`) != slots || f.count(t, `SELECT count(*) FROM audit WHERE action='callback.completed'`) != audits {
				t.Fatal("completion graph")
			}
		})
	}
}
