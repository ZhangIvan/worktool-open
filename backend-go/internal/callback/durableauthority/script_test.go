package durableauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"reflect"
	"testing"
	"time"
)

// Frozen statement hashes, no generic SQL interpreter or database emulation.
const hashsearchPathSQL = "5b012532f4a3de5253a20bdccd406ffecc5b494937c5d93ddbfc2c3e42191974"
const hashregistrySQL = "d78ec08d07ca9fcaf7c49b0395baf30fa69e4f7d0a00cad1759cad056a56c42f"
const hashledgerSQL = "874d89ad64ec5a634c74b5d5592d928f61d152714125d001ef3ba54a2296e2dc"
const hashinsertIntentSQL = "ebd2b34075842edd08eb1283f5f5ac056356b6bbcf14f903e501817afd4bcee5"
const hashhaltSQL = "b101693b2abcd99e74b272c463baf32a802277f14ffe055c936da51d1b856b76"
const hasheventSQL = "ccfe534009b8e53c1b322613086342f0537a5825f898f26a79523df01f40cd69"
const hashinsertEventSQL = "adefc563b39039b1c019163b29dd461004ac13c4b91ccc32cc8e0ffd9ca17022"
const hashanyEventsSQL = "c24f3eaf76a80a147485bddce48073c72bef587d742432b0e91dc34ca5503b23"
const hashincidentsSQL = "8698eb26a1273e40cf56e8756868745362c0a624f497d130ca14c9d69bc71248"
const hashconstraintsSQL = "11bd90418a1000de688748966e9008e4f70ad3cbd2563da0544a0135fda5fee2"

type step struct {
	op, hash     string
	args, values []any
	err          error
	affected     int64
}
type script struct {
	pgx.Tx
	t                                            *testing.T
	steps                                        []step
	next, commits, rollbacks, releases, discards int
	commitErr, rollbackErr, discardErr           error
}

func (s *script) take(op, q string, args []any) step {
	s.t.Helper()
	if s.next >= len(s.steps) {
		s.t.Fatalf("unexpected %s at %d", op, s.next)
	}
	want := s.steps[s.next]
	s.next++
	sum := sha256.Sum256([]byte(q))
	if op != want.op || hex.EncodeToString(sum[:]) != want.hash || !reflect.DeepEqual(args, want.args) {
		s.t.Fatalf("script mismatch at %d op=%s", s.next, op)
	}
	return want
}
func (s *script) Acquire(ctx context.Context) (pg.Connection, error) {
	if !alive(ctx) {
		return nil, errors.New("script context")
	}
	return s, nil
}
func (s *script) BeginTx(ctx context.Context, o pgx.TxOptions) (pgx.Tx, error) {
	if !alive(ctx) || o.IsoLevel != pgx.ReadCommitted {
		return nil, errors.New("script options")
	}
	return s, nil
}
func (s *script) Release()                      { s.releases++ }
func (s *script) Discard(context.Context) error { s.discards++; return s.discardErr }
func (s *script) Commit(context.Context) error  { s.commits++; return s.commitErr }
func (s *script) Rollback(context.Context) error {
	s.rollbacks++
	if s.rollbackErr != nil {
		return s.rollbackErr
	}
	return pgx.ErrTxClosed
}
func (s *script) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	x := s.take("exec", q, args)
	if x.err != nil {
		return pgconn.CommandTag{}, x.err
	}
	if x.affected == 1 {
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}
	return pgconn.NewCommandTag("UPDATE 0"), nil
}
func (s *script) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	return row{s.take("row", q, args)}
}

type row struct{ step }

func (r row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("script scan arity")
	}
	for i, v := range r.values {
		// Model pgx NULL semantics only; this script does not evaluate SQL CASE.
		if v == nil {
			if bytes, ok := dest[i].(*[]byte); ok {
				*bytes = nil
				continue
			}
			return errors.New("script NULL cannot scan into required scalar")
		}
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}
func baseSteps(b Binding) []step {
	i := b.identity
	return []step{{op: "exec", hash: hashsearchPathSQL}, {op: "row", hash: hashregistrySQL, args: []any{i.Scope}, values: []any{i.Scope, i.Allocation, i.Instance, i.Boot, i.Epoch, i.ProvenanceRef, i.Revision, b.planIdentity, b.digest, []byte(testManifest), append([]byte(nil), b.descriptors...)}}}
}
func ledgerStep(b Binding, state, code string) step {
	return step{op: "row", hash: hashledgerSQL, args: []any{b.identity.Scope}, values: append(bindingArgs(b), state, code, state == "halted", state == "halted")}
}
func absentLedger(b Binding) step {
	return step{op: "row", hash: hashledgerSQL, args: []any{b.identity.Scope}, err: pgx.ErrNoRows}
}
func eventStep(b Binding, e event) step {
	return step{op: "row", hash: hasheventSQL, args: []any{e.operation}, values: append(bindingArgs(b), e.kind, e.code, e.reference)}
}
func absentEvent(e event) step {
	return step{op: "row", hash: hasheventSQL, args: []any{e.operation}, err: pgx.ErrNoRows}
}
func adapter(t *testing.T, steps []step) (*Adapter, *script) {
	t.Helper()
	expanded := make([]step, 0, len(steps)+1)
	for n, x := range steps {
		expanded = append(expanded, x)
		if x.hash == hashledgerSQL && errors.Is(x.err, pgx.ErrNoRows) && (n+1 == len(steps) || steps[n+1].hash != hashanyEventsSQL) {
			expanded = append(expanded, step{op: "row", hash: hashanyEventsSQL, args: x.args, values: []any{false}})
		}
	}
	s := &script{t: t, steps: expanded}
	a, e := New(s, pg.Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 3 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return a, s
}
func (s *script) done(commits int) {
	s.t.Helper()
	if s.next != len(s.steps) || s.commits != commits || s.rollbacks != 1 || s.releases != 1 {
		s.t.Fatalf("incomplete script next=%d/%d commits=%d rollback=%d release=%d", s.next, len(s.steps), s.commits, s.rollbacks, s.releases)
	}
}
func intentSteps(b Binding) []step {
	ss := baseSteps(b)
	return append(ss, absentLedger(b), step{op: "exec", hash: hashinsertIntentSQL, args: bindingArgs(b), affected: 1}, step{op: "exec", hash: hashconstraintsSQL}, ledgerStep(b, "running", ""))
}
func eventSteps(b Binding, e event, beforeState, beforeCode string) []step {
	ss := append(baseSteps(b), ledgerStep(b, beforeState, beforeCode), absentEvent(e))
	args := append([]any{e.operation}, bindingArgs(b)...)
	args = append(args, e.kind, e.code, e.reference)
	ss = append(ss, step{op: "exec", hash: hashinsertEventSQL, args: args, affected: 1})
	afterState, afterCode := beforeState, beforeCode
	if e.kind != "reconciliation_fact" && beforeState == "running" {
		ss = append(ss, step{op: "exec", hash: hashhaltSQL, args: append(bindingArgs(b), e.code), affected: 1})
		afterState, afterCode = "halted", e.code
	}
	return append(ss, step{op: "exec", hash: hashconstraintsSQL}, eventStep(b, e), ledgerStep(b, afterState, afterCode))
}
