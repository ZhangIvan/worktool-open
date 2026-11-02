// Package pg contains bounded PostgreSQL transaction plumbing, not business retry policy.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connection must be exclusively acquired until Release; Discard prevents reuse.
type Connection interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Release()
	Discard(context.Context) error
}
type Pool interface {
	Acquire(context.Context) (Connection, error)
}

type PoolAdapter struct{ Pool *pgxpool.Pool }

func (p PoolAdapter) Acquire(ctx context.Context) (Connection, error) {
	if p.Pool == nil {
		return nil, errors.New("nil pgx pool")
	}
	c, err := p.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return poolConnection{c}, nil
}

type poolConnection struct{ *pgxpool.Conn }

func (c poolConnection) Discard(ctx context.Context) error { return c.Conn.Conn().Close(ctx) }

// Timeouts are mandatory and bounded; no zero-value unbounded operation.
type Timeouts struct{ Acquire, Begin, Transaction, Commit, Cleanup time.Duration }

func (t Timeouts) Validate() error {
	for name, value := range map[string]time.Duration{"acquire": t.Acquire, "begin": t.Begin, "transaction": t.Transaction, "commit": t.Commit, "cleanup": t.Cleanup} {
		if value <= 0 || value > 30*time.Second {
			return fmt.Errorf("%s timeout must be >0 and <=30s", name)
		}
	}
	if t.Begin > t.Transaction || t.Commit > t.Transaction {
		return errors.New("begin/commit timeout exceeds transaction timeout")
	}
	return nil
}

var ErrCommitOutcomeUnknown = errors.New("transaction commit outcome unknown; reconcile durable idempotency key before retry")

// OutcomeError preserves driver cause for classification without claiming rollback
// proves a failed Commit did not become durable. Do not log SQL/parameter secrets.
type OutcomeError struct{ Cause error }

func (e *OutcomeError) Error() string        { return ErrCommitOutcomeUnknown.Error() }
func (e *OutcomeError) Unwrap() error        { return e.Cause }
func (e *OutcomeError) Is(target error) bool { return target == ErrCommitOutcomeUnknown }

type Runner struct {
	pool     Pool
	timeouts Timeouts
}

func NewRunner(pool Pool, timeouts Timeouts) (*Runner, error) {
	if pool == nil {
		return nil, errors.New("nil pool")
	}
	if err := timeouts.Validate(); err != nil {
		return nil, err
	}
	return &Runner{pool: pool, timeouts: timeouts}, nil
}

// Run invokes fn exactly once. fn MUST perform only bounded DB work using ctx;
// no HTTP/WS effects, waits, nested transaction retry, or goroutines retaining tx.
// Context cancellation does not automatically roll back pgx transactions: cleanup
// uses its own bounded context, even on panic. This cannot preempt a callback
// ignoring ctx; callers must obey the contract. No SQLSTATE auto-retry is provided.
func (r *Runner) Run(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) (err error) {
	if ctx == nil || fn == nil {
		return errors.New("context and transaction callback required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	acquireCtx, acquireCancel := context.WithTimeout(ctx, r.timeouts.Acquire)
	conn, err := r.pool.Acquire(acquireCtx)
	acquireCancel()
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()
	txCtx, txCancel := context.WithTimeout(ctx, r.timeouts.Transaction)
	defer txCancel()
	beginCtx, beginCancel := context.WithTimeout(txCtx, r.timeouts.Begin)
	tx, err := conn.BeginTx(beginCtx, opts)
	beginCancel()
	if err != nil {
		// BEGIN failure can leave connection health ambiguous; discard conservatively.
		discardCtx, cancel := context.WithTimeout(context.Background(), r.timeouts.Cleanup)
		discardErr := conn.Discard(discardCtx)
		cancel()
		return errors.Join(fmt.Errorf("begin: %w", err), discardErr)
	}
	commitUnknown := false
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), r.timeouts.Cleanup)
		rollbackErr := tx.Rollback(cleanupCtx)
		cancel()
		rollbackFailed := rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed)
		if rollbackFailed || commitUnknown {
			discardCtx, discardCancel := context.WithTimeout(context.Background(), r.timeouts.Cleanup)
			discardErr := conn.Discard(discardCtx)
			discardCancel()
			if rollbackFailed {
				err = errors.Join(err, fmt.Errorf("rollback cleanup: %w", rollbackErr))
			}
			err = errors.Join(err, discardErr)
		}
	}()
	if err = txCtx.Err(); err != nil {
		return err
	}
	if err = fn(txCtx, tx); err != nil {
		return err
	}
	if err = txCtx.Err(); err != nil {
		return err
	}
	commitCtx, commitCancel := context.WithTimeout(txCtx, r.timeouts.Commit)
	commitErr := tx.Commit(commitCtx)
	commitCancel()
	if commitErr != nil {
		commitUnknown = true
		return &OutcomeError{Cause: commitErr}
	}
	return nil
}
