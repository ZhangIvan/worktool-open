package durableauthority

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errDriverUnconfirmed = errors.New("database operation unconfirmed")
var errGuardedDriver = errors.New("database operation failed")

// driverStatus belongs to exactly one synchronous Run. No state is shared between
// adapter operations. These guards detect non-cooperative late returns; they do
// not preempt drivers or release a connection still in use by another goroutine.
type driverStatus struct{ unconfirmed, cleanup, tainted bool }

func nilDriver(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}
func contextFinished(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return true
	}
	d, ok := ctx.Deadline()
	return ok && !time.Now().Before(d)
}

// guardedCall never returns a driver cause or panic value. A deadline is checked
// against the wall clock as well as ctx.Err(), including successful late calls.
func guardedCall(ctx context.Context, fn func() error) (err error, uncertain bool) {
	defer func() {
		if recover() != nil {
			err, uncertain = errDriverUnconfirmed, true
		}
		if contextFinished(ctx) {
			err, uncertain = errors.Join(err, errDriverUnconfirmed), true
		}
	}()
	if contextFinished(ctx) {
		return errDriverUnconfirmed, true
	}
	cause := fn()
	if cause != nil {
		// Preserve only fixed public classifications, never the driver cause.
		// Uncertainty dominates ErrTxClosed: the latter must not let Runner
		// skip discard when the same result also reports uncertain cleanup.
		unknown := errors.Is(cause, pg.ErrCommitOutcomeUnknown)
		cleanup := errors.Is(cause, pg.ErrCleanupUnconfirmed)
		switch {
		case unknown || cleanup:
			err = errGuardedDriver
			if unknown {
				err = errors.Join(err, pg.ErrCommitOutcomeUnknown)
			}
			if cleanup {
				err = errors.Join(err, pg.ErrCleanupUnconfirmed)
			}
		case errors.Is(cause, pgx.ErrTxClosed):
			err = pgx.ErrTxClosed
		case errors.Is(cause, pgx.ErrNoRows):
			err = pgx.ErrNoRows
		default:
			err = errGuardedDriver
		}
	}
	return
}

func runGuarded(ctx context.Context, pool pg.Pool, t pg.Timeouts, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	if ctx == nil || fn == nil || nilDriver(pool) || t.Validate() != nil {
		return errDriverUnconfirmed
	}
	status := &driverStatus{}
	runner, err := pg.NewRunner(&guardedPool{pool: pool, t: t, status: status}, t)
	if err != nil {
		return errDriverUnconfirmed
	}
	err = runner.Run(ctx, opts, func(ctx context.Context, tx pgx.Tx) (err error) {
		defer func() {
			if recover() != nil {
				status.tainted, status.unconfirmed = true, true
				err = errDriverUnconfirmed
			}
			if contextFinished(ctx) {
				status.tainted, status.unconfirmed = true, true
				err = errors.Join(err, errDriverUnconfirmed)
			}
		}()
		return fn(ctx, tx)
	})
	if contextFinished(ctx) {
		status.unconfirmed = true
	}
	if status.unconfirmed {
		err = errors.Join(err, errDriverUnconfirmed)
	}
	if status.cleanup {
		err = errors.Join(err, pg.ErrCleanupUnconfirmed)
	}
	return err
}

type guardedPool struct {
	pool   pg.Pool
	t      pg.Timeouts
	status *driverStatus
}

func (p *guardedPool) Acquire(ctx context.Context) (pg.Connection, error) {
	var raw pg.Connection
	err, uncertain := guardedCall(ctx, func() (err error) { raw, err = p.pool.Acquire(ctx); return })
	if uncertain {
		p.status.unconfirmed = true
	}
	if nilDriver(raw) {
		p.status.unconfirmed = true
		// A panicking/late acquisition may have acquired a connection without
		// returning its handle. Its cleanup cannot be certified.
		if uncertain {
			p.status.cleanup = true
		}
		return nil, errors.Join(err, errDriverUnconfirmed)
	}
	c := &guardedConnection{raw: raw, t: p.t, status: p.status}
	if err != nil {
		cleanCtx, cancel := context.WithTimeout(context.Background(), p.t.Cleanup)
		discardErr := c.Discard(cleanCtx)
		cancel()
		c.Release()
		if discardErr != nil {
			p.status.cleanup = true
		}
		return nil, errors.Join(err, discardErr)
	}
	return c, nil
}

type guardedConnection struct {
	raw                 pg.Connection
	t                   pg.Timeouts
	status              *driverStatus
	released, discarded bool
}

func (c *guardedConnection) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	var tx pgx.Tx
	err, uncertain := guardedCall(ctx, func() (err error) { tx, err = c.raw.BeginTx(ctx, opts); return })
	if uncertain {
		c.status.unconfirmed = true
	}
	if err != nil {
		return nil, err
	}
	if nilDriver(tx) {
		c.status.unconfirmed = true
		return nil, errDriverUnconfirmed
	}
	return &guardedTx{raw: tx, status: c.status}, nil
}
func (c *guardedConnection) Discard(ctx context.Context) error {
	if c.discarded {
		c.status.cleanup = true
		return errDriverUnconfirmed
	}
	c.discarded = true
	err, uncertain := guardedCall(ctx, func() error { return c.raw.Discard(ctx) })
	if uncertain {
		c.status.unconfirmed = true
	}
	if err != nil {
		c.status.cleanup = true
	}
	return err
}
func (c *guardedConnection) Release() {
	if c.released {
		c.status.cleanup, c.status.unconfirmed = true, true
		return
	}
	c.released = true
	ctx, cancel := context.WithTimeout(context.Background(), c.t.Cleanup)
	defer cancel()
	err, uncertain := guardedCall(ctx, func() error { c.raw.Release(); return nil })
	if uncertain {
		c.status.unconfirmed = true
	}
	if err != nil {
		c.status.cleanup = true
	}
}

type guardedTx struct {
	raw    pgx.Tx
	status *driverStatus
}

func (t *guardedTx) sqlCall(ctx context.Context, fn func() error) error {
	err, uncertain := guardedCall(ctx, fn)
	if uncertain {
		t.status.tainted, t.status.unconfirmed = true, true
	}
	if errors.Is(err, pg.ErrCommitOutcomeUnknown) || errors.Is(err, pg.ErrCleanupUnconfirmed) {
		t.status.tainted = true
	}
	return err
}
func (t *guardedTx) Commit(ctx context.Context) error {
	if t.status.tainted {
		return errDriverUnconfirmed
	}
	err, uncertain := guardedCall(ctx, func() error { return t.raw.Commit(ctx) })
	if uncertain {
		t.status.unconfirmed = true
	}
	return err
}
func (t *guardedTx) Rollback(ctx context.Context) error {
	err, uncertain := guardedCall(ctx, func() error { return t.raw.Rollback(ctx) })
	if uncertain {
		t.status.unconfirmed = true
	}
	// Even an apparently successful rollback cannot cleanse a driver panic or a
	// timed-out SQL call. Returning a fixed failure forces Runner's discard path.
	if uncertain || t.status.tainted {
		if errors.Is(err, pgx.ErrTxClosed) {
			err = nil
		}
		return errors.Join(err, errDriverUnconfirmed)
	}
	return err
}
func (t *guardedTx) Exec(ctx context.Context, sql string, args ...any) (tag pgconn.CommandTag, err error) {
	err = t.sqlCall(ctx, func() (err error) { tag, err = t.raw.Exec(ctx, sql, args...); return })
	return
}
func (t *guardedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	var row pgx.Row
	err := t.sqlCall(ctx, func() error { row = t.raw.QueryRow(ctx, sql, args...); return nil })
	if err == nil && nilDriver(row) {
		t.status.tainted, t.status.unconfirmed = true, true
		err = errDriverUnconfirmed
	}
	return &guardedRow{raw: row, tx: t, ctx: ctx, err: err}
}

type guardedRow struct {
	raw pgx.Row
	tx  *guardedTx
	ctx context.Context
	err error
}

func (r *guardedRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return r.tx.sqlCall(r.ctx, func() error { return r.raw.Scan(dest...) })
}

// This adapter uses only Exec and QueryRow. Reject the remaining pgx interface
// rather than expose an unguarded transaction, connection, batch or row stream.
func (t *guardedTx) unsupported() error {
	t.status.tainted, t.status.unconfirmed = true, true
	return errDriverUnconfirmed
}
func (t *guardedTx) Begin(context.Context) (pgx.Tx, error) { return nil, t.unsupported() }
func (t *guardedTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, t.unsupported()
}
func (t *guardedTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	t.unsupported()
	return nil
}
func (t *guardedTx) LargeObjects() pgx.LargeObjects { t.unsupported(); return pgx.LargeObjects{} }
func (t *guardedTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, t.unsupported()
}
func (t *guardedTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, t.unsupported()
}
func (t *guardedTx) Conn() *pgx.Conn { t.unsupported(); return nil }
