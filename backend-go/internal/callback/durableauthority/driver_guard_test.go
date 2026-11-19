package durableauthority

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type guardFixture struct {
	calls                        []string
	hooks                        map[string]func()
	failures                     map[string]error
	acquireNil, beginNil, rowNil bool
}

func (f *guardFixture) call(name string) error {
	f.calls = append(f.calls, name)
	if h := f.hooks[name]; h != nil {
		h()
	}
	return f.failures[name]
}
func (f *guardFixture) Acquire(context.Context) (pg.Connection, error) {
	err := f.call("acquire")
	if f.acquireNil {
		return nil, err
	}
	return &guardFakeConn{f}, err
}

type guardFakeConn struct{ f *guardFixture }

func (c *guardFakeConn) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	err := c.f.call("begin")
	if c.f.beginNil {
		return nil, err
	}
	return &guardFakeTx{f: c.f}, err
}
func (c *guardFakeConn) Release()                      { c.f.call("release") }
func (c *guardFakeConn) Discard(context.Context) error { return c.f.call("discard") }

type guardFakeTx struct {
	pgx.Tx
	f *guardFixture
}

func (t *guardFakeTx) Commit(context.Context) error   { return t.f.call("commit") }
func (t *guardFakeTx) Rollback(context.Context) error { return t.f.call("rollback") }
func (t *guardFakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag("UPDATE 1"), t.f.call("exec")
}
func (t *guardFakeTx) QueryRow(context.Context, string, ...any) pgx.Row {
	t.f.call("queryrow")
	if t.f.rowNil {
		return nil
	}
	return guardFakeRow{t.f}
}

type guardFakeRow struct{ f *guardFixture }

func (r guardFakeRow) Scan(...any) error { return r.f.call("scan") }
func guardTimeouts() pg.Timeouts {
	return pg.Timeouts{Acquire: 10 * time.Millisecond, Begin: 10 * time.Millisecond, Transaction: 100 * time.Millisecond, Commit: 10 * time.Millisecond, Cleanup: 10 * time.Millisecond}
}
func guardFixtureNew() *guardFixture {
	return &guardFixture{hooks: map[string]func(){}, failures: map[string]error{}}
}
func assertGuardCalls(t *testing.T, f *guardFixture, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls=%v want=%v", f.calls, want)
	}
}
func assertGuardFlags(t *testing.T, err error, unknown, cleanup, unconfirmed bool) {
	t.Helper()
	for _, p := range []struct {
		e    error
		want bool
	}{{pg.ErrCommitOutcomeUnknown, unknown}, {pg.ErrCleanupUnconfirmed, cleanup}, {errDriverUnconfirmed, unconfirmed}} {
		if errors.Is(err, p.e) != p.want {
			t.Errorf("error %v classification %v want %v", err, p.e, p.want)
		}
	}
	if err != nil && strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("driver detail escaped: %v", err)
	}
}
func TestGuardRunnerSuccessAndNoRetries(t *testing.T) {
	f := guardFixtureNew()
	n := 0
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		n++
		_, err := tx.Exec(ctx, "fixed")
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "fixed").Scan()
	})
	if err != nil || n != 1 {
		t.Fatalf("error=%v callback count=%d", err, n)
	}
	assertGuardCalls(t, f, "acquire", "begin", "exec", "queryrow", "scan", "commit", "rollback", "release")
}
func TestGuardRunnerPanics(t *testing.T) {
	cases := []struct {
		stage            string
		unknown, cleanup bool
		calls            []string
	}{
		{"acquire", false, true, []string{"acquire"}},
		{"begin", false, false, []string{"acquire", "begin", "discard", "release"}},
		{"exec", false, true, []string{"acquire", "begin", "exec", "rollback", "discard", "release"}},
		{"queryrow", false, true, []string{"acquire", "begin", "queryrow", "rollback", "discard", "release"}},
		{"scan", false, true, []string{"acquire", "begin", "queryrow", "scan", "rollback", "discard", "release"}},
		{"commit", true, false, []string{"acquire", "begin", "commit", "rollback", "discard", "release"}},
		{"rollback", false, true, []string{"acquire", "begin", "commit", "rollback", "discard", "release"}},
		{"release", false, true, []string{"acquire", "begin", "commit", "rollback", "release"}},
	}
	for _, tc := range cases {
		t.Run(tc.stage, func(t *testing.T) {
			f := guardFixtureNew()
			f.hooks[tc.stage] = func() { panic("SECRET") }
			err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				switch tc.stage {
				case "exec":
					_, err := tx.Exec(ctx, "fixed")
					return err
				case "queryrow", "scan":
					return tx.QueryRow(ctx, "fixed").Scan()
				}
				return nil
			})
			if err == nil {
				t.Fatal("panic accepted")
			}
			assertGuardFlags(t, err, tc.unknown, tc.cleanup, true)
			assertGuardCalls(t, f, tc.calls...)
		})
	}
}
func TestGuardRunnerLateReturns(t *testing.T) {
	for _, stage := range []string{"acquire", "begin", "commit", "rollback", "discard", "release"} {
		t.Run(stage, func(t *testing.T) {
			f := guardFixtureNew()
			f.hooks[stage] = func() { time.Sleep(25 * time.Millisecond) }
			if stage == "discard" {
				f.failures["commit"] = errors.New("SECRET")
			}
			err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
			cleanup := stage == "rollback" || stage == "discard" || stage == "release"
			assertGuardFlags(t, err, stage == "commit" || stage == "discard", cleanup, true)
			switch stage {
			case "acquire":
				assertGuardCalls(t, f, "acquire", "discard", "release")
			case "begin":
				assertGuardCalls(t, f, "acquire", "begin", "discard", "release")
			case "commit", "rollback", "discard":
				assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "discard", "release")
			case "release":
				assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "release")
			}
		})
	}
}
func TestGuardRunnerCombinedUncertainty(t *testing.T) {
	for _, cleanupStage := range []string{"rollback", "discard", "release"} {
		for _, mode := range []string{"panic", "late"} {
			t.Run(cleanupStage+mode, func(t *testing.T) {
				f := guardFixtureNew()
				f.failures["commit"] = errors.New("SECRET commit")
				if mode == "panic" {
					f.hooks[cleanupStage] = func() { panic("SECRET cleanup") }
				} else {
					f.hooks[cleanupStage] = func() { time.Sleep(25 * time.Millisecond) }
				}
				err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
				assertGuardFlags(t, err, true, true, true)
				assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "discard", "release")
			})
		}
	}
}
func TestGuardAcquiredConnectionWithErrorCleanedExactlyOnce(t *testing.T) {
	f := guardFixtureNew()
	f.failures["acquire"] = errors.New("SECRET acquire")
	f.hooks["discard"] = func() { panic("SECRET") }
	f.hooks["release"] = func() { panic("SECRET") }
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback called"); return nil })
	assertGuardFlags(t, err, false, true, true)
	assertGuardCalls(t, f, "acquire", "discard", "release")
}
func TestGuardCallbackPanicPreservesCleanup(t *testing.T) {
	f := guardFixtureNew()
	f.failures["rollback"] = errors.New("SECRET rollback")
	f.failures["discard"] = errors.New("SECRET discard")
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { panic("SECRET callback") })
	assertGuardFlags(t, err, false, true, true)
	assertGuardCalls(t, f, "acquire", "begin", "rollback", "discard", "release")
}
func TestGuardRollbackLateClosedStillUnconfirmed(t *testing.T) {
	f := guardFixtureNew()
	f.hooks["rollback"] = func() { time.Sleep(25 * time.Millisecond) }
	f.failures["rollback"] = pgx.ErrTxClosed
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	assertGuardFlags(t, err, false, true, true)
	assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "discard", "release")
}
func TestGuardCanceledSQLCannotCommit(t *testing.T) {
	for _, stage := range []string{"exec", "queryrow", "scan"} {
		t.Run(stage, func(t *testing.T) {
			f := guardFixtureNew()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.hooks[stage] = cancel
			err := runGuarded(ctx, f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				if stage == "exec" {
					_, err := tx.Exec(ctx, "fixed")
					return err
				}
				return tx.QueryRow(ctx, "fixed").Scan()
			})
			assertGuardFlags(t, err, false, true, true)
			want := []string{"acquire", "begin"}
			if stage == "exec" {
				want = append(want, "exec")
			} else {
				want = append(want, "queryrow")
				if stage == "scan" {
					want = append(want, "scan")
				}
			}
			want = append(want, "rollback", "discard", "release")
			assertGuardCalls(t, f, want...)
		})
	}
}
func TestGuardNilDriverValuesFailClosed(t *testing.T) {
	for _, stage := range []string{"pool", "acquire", "begin", "row"} {
		t.Run(stage, func(t *testing.T) {
			f := guardFixtureNew()
			var pool pg.Pool = f
			switch stage {
			case "pool":
				var nilPool *guardFixture
				pool = nilPool
			case "acquire":
				f.acquireNil = true
			case "begin":
				f.beginNil = true
			case "row":
				f.rowNil = true
			}
			err := runGuarded(context.Background(), pool, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, "fixed").Scan() })
			assertGuardFlags(t, err, false, stage == "row", true)
		})
	}
}
func TestGuardSafeCallbackSentinelPreserved(t *testing.T) {
	f := guardFixtureNew()
	fixed := errors.New("fixed callback refusal")
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return fixed })
	if !errors.Is(err, fixed) {
		t.Fatal("callback classification lost")
	}
	assertGuardCalls(t, f, "acquire", "begin", "rollback", "release")
}
func TestGuardPublicDriverClassificationsAndRedaction(t *testing.T) {
	for _, cause := range []error{pgx.ErrNoRows, errors.New("SECRET SQL arguments")} {
		t.Run(fmt.Sprint(cause == pgx.ErrNoRows), func(t *testing.T) {
			f := guardFixtureNew()
			f.failures["scan"] = cause
			err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error { return tx.QueryRow(ctx, "fixed").Scan() })
			assertGuardFlags(t, err, false, false, false)
			if errors.Is(err, pgx.ErrNoRows) != (cause == pgx.ErrNoRows) {
				t.Fatal("no rows classification lost")
			}
			if cause != pgx.ErrNoRows && errors.Is(err, cause) {
				t.Fatal("raw cause survived")
			}
		})
	}
}
func TestGuardParallelOperationsHaveIndependentStatus(t *testing.T) {
	for i := 0; i < 12; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			f := guardFixtureNew()
			err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGuardSQLLateNilFailsClosed(t *testing.T) {
	for _, stage := range []string{"exec", "queryrow", "scan"} {
		t.Run(stage, func(t *testing.T) {
			f := guardFixtureNew()
			f.hooks[stage] = func() { time.Sleep(25 * time.Millisecond) }
			limits := guardTimeouts()
			limits.Transaction = 10 * time.Millisecond
			err := runGuarded(context.Background(), f, limits, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				if stage == "exec" {
					_, err := tx.Exec(ctx, "fixed")
					return err
				}
				return tx.QueryRow(ctx, "fixed").Scan()
			})
			assertGuardFlags(t, err, false, true, true)
			for _, call := range f.calls {
				if call == "commit" {
					t.Fatal("late SQL committed")
				}
			}
			if n := len(f.calls); n < 3 || !reflect.DeepEqual(f.calls[n-3:], []string{"rollback", "discard", "release"}) {
				t.Fatalf("cleanup calls: %v", f.calls)
			}
		})
	}
}
func TestGuardSQLPanicCannotBeSwallowedIntoSuccess(t *testing.T) {
	f := guardFixtureNew()
	f.hooks["exec"] = func() { panic("SECRET") }
	f.failures["rollback"] = pgx.ErrTxClosed
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error { tx.Exec(ctx, "fixed"); return nil })
	assertGuardFlags(t, err, true, true, true)
	// The guarded commit refuses without invoking the raw driver.
	assertGuardCalls(t, f, "acquire", "begin", "exec", "rollback", "discard", "release")
}
func TestGuardBeginFailureAndCleanupFailure(t *testing.T) {
	f := guardFixtureNew()
	f.failures["begin"] = errors.New("SECRET begin")
	f.failures["discard"] = errors.New("SECRET discard")
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil })
	assertGuardFlags(t, err, false, true, false)
	assertGuardCalls(t, f, "acquire", "begin", "discard", "release")
}
func TestGuardAcquireErrorWithHandleCleanCleanup(t *testing.T) {
	f := guardFixtureNew()
	f.failures["acquire"] = errors.New("SECRET")
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil })
	if err == nil {
		t.Fatal("accepted error")
	}
	assertGuardFlags(t, err, false, false, false)
	assertGuardCalls(t, f, "acquire", "discard", "release")
}
func TestGuardCleanAlreadyClosedRollback(t *testing.T) {
	f := guardFixtureNew()
	f.failures["rollback"] = pgx.ErrTxClosed
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "release")
}

type guardExpiredWallContext struct{ context.Context }

func (guardExpiredWallContext) Deadline() (time.Time, bool) {
	return time.Now().Add(-time.Second), true
}
func TestGuardExplicitWallDeadlineAndInvalidInput(t *testing.T) {
	f := guardFixtureNew()
	if err := runGuarded(guardExpiredWallContext{context.Background()}, f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil }); err == nil {
		t.Fatal("expired wall deadline accepted")
	}
	if len(f.calls) != 0 {
		t.Fatalf("expired context touched driver: %v", f.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runGuarded(ctx, f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil }); err == nil {
		t.Fatal("canceled context accepted")
	}
	for _, err := range []error{
		runGuarded(nil, f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil }),
		runGuarded(context.Background(), f, pg.Timeouts{}, pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil }),
		runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, nil),
	} {
		if err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("invalid input touched driver: %v", f.calls)
	}
}

func TestGuardDriverUncertaintyClassificationsSurviveRedaction(t *testing.T) {
	for _, stage := range []string{"acquire", "acquire_nil", "begin", "exec", "scan", "commit", "rollback", "discard"} {
		t.Run(stage, func(t *testing.T) {
			f := guardFixtureNew()
			cause := errors.Join(errors.New("SECRET cause"), pg.ErrCommitOutcomeUnknown, pg.ErrCleanupUnconfirmed)
			if stage == "acquire_nil" {
				f.acquireNil = true
				f.failures["acquire"] = cause
			} else {
				f.failures[stage] = cause
			}
			if stage == "discard" {
				f.failures["begin"] = errors.New("SECRET begin")
			}
			err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				switch stage {
				case "exec":
					_, err := tx.Exec(ctx, "fixed")
					return err
				case "scan":
					return tx.QueryRow(ctx, "fixed").Scan()
				}
				return nil
			})
			assertGuardFlags(t, err, true, true, stage == "acquire_nil" || stage == "exec" || stage == "scan")
			if errors.Is(err, cause) {
				t.Fatal("raw cause escaped")
			}
		})
	}
}
func TestGuardLateErrorPreservesExistingUncertainty(t *testing.T) {
	f := guardFixtureNew()
	f.hooks["acquire"] = func() { time.Sleep(25 * time.Millisecond) }
	f.failures["acquire"] = errors.Join(pg.ErrCommitOutcomeUnknown, pg.ErrCleanupUnconfirmed, errors.New("SECRET"))
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { t.Fatal("callback ran"); return nil })
	assertGuardFlags(t, err, true, true, true)
	assertGuardCalls(t, f, "acquire", "discard", "release")
}
func TestGuardRollbackClosedCannotMaskUncertainty(t *testing.T) {
	f := guardFixtureNew()
	f.failures["rollback"] = errors.Join(pgx.ErrTxClosed, pg.ErrCleanupUnconfirmed, errors.New("SECRET"))
	err := runGuarded(context.Background(), f, guardTimeouts(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error { return nil })
	assertGuardFlags(t, err, false, true, false)
	assertGuardCalls(t, f, "acquire", "begin", "commit", "rollback", "discard", "release")
}
