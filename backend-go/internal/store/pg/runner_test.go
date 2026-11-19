package pg

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
)

// Embed the full driver interface; only the three deliberately used methods are
// implemented. Unexpected driver calls panic instead of silently succeeding.
type fakeTx struct {
	pgx.Tx
	commits, rollbacks            int
	commitErr, rollbackErr        error
	rollbackLive, rollbackBounded bool
}

func (f *fakeTx) Commit(ctx context.Context) error {
	f.commits++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded commit")
	}
	return f.commitErr
}
func (f *fakeTx) Rollback(ctx context.Context) error {
	f.rollbacks++
	f.rollbackLive = ctx.Err() == nil
	_, f.rollbackBounded = ctx.Deadline()
	return f.rollbackErr
}

type fakeConn struct {
	tx                         *fakeTx
	beginErr, discardErr       error
	releases, discards, begins int
}

func (f *fakeConn) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	f.begins++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded begin")
	}
	return f.tx, f.beginErr
}
func (f *fakeConn) Release() { f.releases++ }
func (f *fakeConn) Discard(ctx context.Context) error {
	f.discards++
	if ctx.Err() != nil {
		panic("discard uses cancelled context")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded discard")
	}
	return f.discardErr
}

type fakePool struct {
	conn     *fakeConn
	err      error
	acquires int
}

func (f *fakePool) Acquire(ctx context.Context) (Connection, error) {
	f.acquires++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded acquire")
	}
	return f.conn, f.err
}
func fixture(t *testing.T) (*Runner, *fakePool, *fakeConn, *fakeTx) {
	t.Helper()
	tx := &fakeTx{}
	c := &fakeConn{tx: tx}
	p := &fakePool{conn: c}
	r, e := NewRunner(p, Timeouts{time.Second, time.Second, 2 * time.Second, time.Second, time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return r, p, c, tx
}
func TestRunCommitsOnceAndReleases(t *testing.T) {
	r, p, c, tx := fixture(t)
	calls := 0
	err := r.Run(context.Background(), pgx.TxOptions{}, func(ctx context.Context, _ pgx.Tx) error {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing transaction deadline")
		}
		return nil
	})
	if err != nil || calls != 1 || p.acquires != 1 || c.releases != 1 || tx.commits != 1 || tx.rollbacks != 1 {
		t.Fatalf("unexpected lifecycle %v", err)
	}
}
func TestCancellationRollsBackWithIndependentContext(t *testing.T) {
	r, _, c, tx := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := r.Run(ctx, pgx.TxOptions{}, func(context.Context, pgx.Tx) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || tx.commits != 0 || tx.rollbacks != 1 || !tx.rollbackLive || !tx.rollbackBounded || c.releases != 1 {
		t.Fatalf("cancel cleanup %v", err)
	}
}
func TestCallbackErrorNoRetry(t *testing.T) {
	r, _, c, tx := fixture(t)
	sentinel := errors.New("serialization failure simulation")
	calls := 0
	err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { calls++; return sentinel })
	if !errors.Is(err, sentinel) || calls != 1 || tx.commits != 0 || tx.rollbacks != 1 || c.releases != 1 {
		t.Fatal("unexpected retry/cleanup")
	}
}
func TestCommitErrorIsUnknownAndDiscards(t *testing.T) {
	r, _, c, tx := fixture(t)
	tx.commitErr = context.DeadlineExceeded
	tx.rollbackErr = pgx.ErrTxClosed
	calls := 0
	err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { calls++; return nil })
	if !errors.Is(err, ErrCommitOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) || calls != 1 || c.discards != 1 || c.releases != 1 {
		t.Fatalf("commit ambiguity lost: %v", err)
	}
}
func TestRollbackFailureDiscards(t *testing.T) {
	r, _, c, tx := fixture(t)
	sentinel := errors.New("body failed")
	tx.rollbackErr = errors.New("connection lost")
	err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return sentinel })
	if !errors.Is(err, sentinel) || c.discards != 1 || c.releases != 1 {
		t.Fatal("unsafe connection reuse")
	}
}
func TestPanicStillCleansUp(t *testing.T) {
	r, _, c, tx := fixture(t)
	defer func() {
		if recover() != "panic-test" {
			t.Fatal("panic swallowed")
		}
		if tx.rollbacks != 1 || c.releases != 1 {
			t.Fatal("cleanup missing")
		}
	}()
	_ = r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { panic("panic-test") })
}
func TestAcquireAndBeginFailures(t *testing.T) {
	t.Run("acquire", func(t *testing.T) {
		r, p, c, _ := fixture(t)
		p.err = context.DeadlineExceeded
		err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil })
		if !errors.Is(err, p.err) || c.releases != 0 {
			t.Fatal("acquire failure lifecycle")
		}
	})
	t.Run("begin", func(t *testing.T) {
		r, _, c, _ := fixture(t)
		c.beginErr = context.Canceled
		err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil })
		if !errors.Is(err, c.beginErr) || c.discards != 1 || c.releases != 1 {
			t.Fatal("begin failure lifecycle")
		}
	})
}
func TestTimeoutValidation(t *testing.T) {
	for _, v := range []time.Duration{0, -1, 31 * time.Second} {
		if (Timeouts{v, time.Second, time.Second, time.Second, time.Second}).Validate() == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
	if (Timeouts{time.Second, 2 * time.Second, time.Second, time.Second, time.Second}).Validate() == nil {
		t.Fatal("begin exceeds transaction")
	}
}
func TestBudget(t *testing.T) {
	b := PoolBudget{MaxConnections: 100, SystemReserve: 10, OperationalReserve: 10, Roles: []RolePool{{"api", 2, 10}, {"gateway", 2, 10}, {"worker", 2, 20}}}
	if used, e := b.Validate(); e != nil || used != 100 {
		t.Fatalf("boundary %d %v", used, e)
	}
	b.Roles[2].Replicas = 3
	if _, e := b.Validate(); e == nil {
		t.Fatal("over allocation accepted")
	}
	b.Roles[2].Replicas = math.MaxInt64
	if _, e := b.Validate(); e == nil {
		t.Fatal("overflow accepted")
	}
}
func TestBudgetInvalid(t *testing.T) {
	for _, roles := range [][]RolePool{{{"api", 0, 1}}, {{"api", 1, 1}, {"api", 1, 1}}, {{"unknown", 1, 1}}, {{"worker", 1, math.MaxInt64}}, nil} {
		if _, e := (PoolBudget{MaxConnections: 100, Roles: roles}).Validate(); e == nil {
			t.Fatal("invalid role allocation accepted")
		}
	}
}

func TestAlreadyCancelledDoesNotAcquire(t *testing.T) {
	r, p, _, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Run(ctx, pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("cancelled callback invoked"); return nil })
	if !errors.Is(err, context.Canceled) || p.acquires != 0 {
		t.Fatal("cancelled request acquired connection")
	}
}
func TestClosedTransactionCleanupIsHarmless(t *testing.T) {
	r, _, c, tx := fixture(t)
	tx.rollbackErr = pgx.ErrTxClosed
	if err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil }); err != nil || c.discards != 0 {
		t.Fatalf("successful commit cleanup: %v", err)
	}
}

func TestCleanupUncertaintyCannotBeHiddenByCommitUnknown(t *testing.T) {
	for _, mode := range []string{"rollback", "discard", "begin_discard"} {
		t.Run(mode, func(t *testing.T) {
			r, _, c, tx := fixture(t)
			tx.commitErr = errors.New("commit acknowledgement lost")
			if mode == "rollback" {
				tx.rollbackErr = errors.New("rollback failed")
			} else {
				c.discardErr = errors.New("discard failed")
			}
			if mode == "begin_discard" {
				c.beginErr = errors.New("begin failed")
			}
			err := r.Run(context.Background(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
			if !errors.Is(err, ErrCleanupUnconfirmed) || c.discards != 1 || c.releases != 1 {
				t.Fatal("cleanup uncertainty lost")
			}
			if mode != "begin_discard" && !errors.Is(err, ErrCommitOutcomeUnknown) {
				t.Fatal("commit evidence lost")
			}
			classified := callbackStoreError(callbackClassify(err))
			if !errors.Is(classified, cb.ErrCleanupUnconfirmed) || errors.Is(classified, cb.ErrCommitUnknown) != (mode != "begin_discard") || !errors.Is(callbackStoreError(err), cb.ErrCleanupUnconfirmed) {
				t.Fatal("cleanup veto or commit uncertainty lost")
			}
		})
	}
}
