package pg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ALL verifier facts below are explicit synthetic assumptions, NOT authenticated
// grants, provider mappings, history provenance or a persistent barrier. The
// shared closed bool only models a durable incident across reconstructed fakes.
// These tests execute the actual Runner and pgx.Tx interface, but NO PostgreSQL,
// SQL parsing, lock, trigger, constraint, role, provider or network operation.
type frAssumptions struct{ grant, actor, provider, schema, fence, history bool }
type frBarrierModel struct{ closed bool }
type frVerifier struct {
	t                       *testing.T
	expected                []byte
	facts                   frAssumptions
	expires, now            time.Time
	barrier                 *frBarrierModel
	stages                  []registrationStage
	deny, panicAt           registrationStage
	closeCalls              int
	closeErr                error
	closePanic              bool
	closeWait               bool
	closeLive, closeBounded bool
	hook                    func(registrationStage)
}

func (*frVerifier) registrationVerifierSeal() {}
func (v *frVerifier) verify(ctx context.Context, i registrationIntent, stage registrationStage) error {
	v.stages = append(v.stages, stage)
	frBounded(v.t, ctx)
	if v.hook != nil {
		v.hook(stage)
	}
	if stage == v.panicAt {
		panic("synthetic verifier panic")
	}
	canonical, err := i.canonical()
	observe := stage == registrationObserveBefore || stage == registrationObserveLocked || stage == registrationObserveDecision
	if err != nil || !bytes.Equal(canonical, v.expected) || v.facts != (frAssumptions{true, true, true, true, true, true}) || !v.expires.After(v.now) || v.barrier == nil || (!observe && v.barrier.closed) || stage == v.deny {
		return errors.New("synthetic assumption denied")
	}
	return nil
}
func (v *frVerifier) close(ctx context.Context, _ registrationIntent) error {
	v.closeCalls++
	v.closeLive = ctx.Err() == nil
	_, v.closeBounded = ctx.Deadline()
	if v.closeWait {
		<-ctx.Done()
		return nil
	}
	if v.closePanic {
		panic("synthetic barrier persistence panic")
	}
	if v.closeErr != nil {
		return v.closeErr
	}
	v.barrier.closed = true
	return nil
}

func frPermit(t *testing.T, i registrationIntent) *frVerifier {
	t.Helper()
	canonical, err := i.canonical()
	if err != nil {
		t.Fatal(err)
	}
	return &frVerifier{t: t, expected: canonical, facts: frAssumptions{true, true, true, true, true, true}, now: time.Unix(100, 0), expires: time.Unix(200, 0), barrier: &frBarrierModel{}}
}
func frIntent(t *testing.T) registrationIntent {
	t.Helper()
	v := registration.Value{Schema: registration.SchemaVersion, Tenant: "tenant_synthetic", App: "app_synthetic", Destination: "dest_synthetic", PolicyVersion: 2, KeyVersion: 3, URL: "https://hooks.example.com/callback", CIDRs: []string{"8.8.8.0/24", "2606:4700::/32"}, SignatureProfile: registration.SignatureProfile, KeyRef: "synthetic-key-reference", ProviderAuthorityID: "provider_synthetic", ProviderRevision: registration.Revision{Kind: "exact", ID: "revision_001"}}
	raw, _ := json.Marshal(v)
	d, err := registration.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return registrationIntent{descriptor: d, schema: "wt_synthetic_registry", authorityID: "authority_synthetic", operationID: "op_synthetic", actor: "actor_synthetic", approvalRef: "approval_synthetic", fenceRef: "fence_synthetic", historyRef: "history_synthetic", planIdentity: "cbplan-v1:sha256:" + strings.Repeat("a", 64)}
}
func frChangeDescriptor(t *testing.T, i *registrationIntent, change func(*registration.Value)) {
	t.Helper()
	v := i.descriptor.Value()
	change(&v)
	raw, _ := json.Marshal(v)
	d, err := registration.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	i.descriptor = d
}
func frBounded(t *testing.T, ctx context.Context) {
	t.Helper()
	if ctx == nil || ctx.Err() != nil {
		t.Fatal("unexpected absent or cancelled operation context")
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("unbounded operation context")
	}
}

type frStep struct {
	name, sql string
	args      []any
	values    []any
	err       error
	exec      bool
	scanPanic bool
	rows      int64
	hook      func()
}
type frTx struct {
	*fakeTx
	t             *testing.T
	steps         []frStep
	index, dml    int
	commitPanic   bool
	rollbackPanic bool
}

func (tx *frTx) next(ctx context.Context, sql string, args []any, exec bool) frStep {
	frBounded(tx.t, ctx)
	if tx.index >= len(tx.steps) {
		tx.t.Fatalf("unexpected SQL call %d: %s", tx.index, sql)
	}
	s := tx.steps[tx.index]
	tx.index++
	if s.exec != exec || sql != s.sql || !reflect.DeepEqual(args, s.args) {
		tx.t.Fatalf("strict SQL/args mismatch at %s\ngot SQL %s\nwant SQL %s\ngot args %#v\nwant args %#v", s.name, sql, s.sql, args, s.args)
	}
	if exec && strings.HasPrefix(sql, "INSERT INTO ") {
		tx.dml++
	}
	if s.hook != nil {
		s.hook()
	}
	return s
}
func (tx *frTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	s := tx.next(ctx, sql, args, false)
	if s.scanPanic {
		return frPanicRow{}
	}
	return callbackTestRow{values: s.values, err: s.err}
}
func (tx *frTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	s := tx.next(ctx, sql, args, true)
	return pgconn.NewCommandTag(fmt.Sprintf("INSERT 0 %d", s.rows)), s.err
}
func (tx *frTx) Commit(ctx context.Context) error {
	if tx.commitPanic {
		tx.commits++
		panic("synthetic commit panic")
	}
	return tx.fakeTx.Commit(ctx)
}
func (tx *frTx) Rollback(ctx context.Context) error {
	err := tx.fakeTx.Rollback(ctx)
	if tx.rollbackPanic {
		panic("synthetic rollback panic")
	}
	return err
}

type frPanicRow struct{}

func (frPanicRow) Scan(...any) error { panic("DO_NOT_ECHO synthetic row scan panic") }

type frConn struct {
	beginPanic, beginNil        bool
	t                           *testing.T
	tx                          *frTx
	beginErr, discardErr        error
	begins, releases, discards  int
	discardLive, discardBounded bool
}

func (c *frConn) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	frBounded(c.t, ctx)
	c.begins++
	if c.beginPanic {
		panic("DO_NOT_ECHO synthetic begin panic")
	}
	if c.beginNil {
		return nil, nil
	}
	if options != (pgx.TxOptions{IsoLevel: pgx.ReadCommitted}) {
		c.t.Fatal("unexpected transaction options")
	}
	return c.tx, c.beginErr
}
func (c *frConn) Release() { c.releases++ }
func (c *frConn) Discard(ctx context.Context) error {
	c.discards++
	c.discardLive = ctx.Err() == nil
	_, c.discardBounded = ctx.Deadline()
	return c.discardErr
}

type frPool struct {
	t        *testing.T
	conn     *frConn
	err      error
	acquires int
	hook     func()
}

func (p *frPool) Acquire(ctx context.Context) (Connection, error) {
	frBounded(p.t, ctx)
	p.acquires++
	if p.hook != nil {
		p.hook()
	}
	return p.conn, p.err
}
func frRunner(t *testing.T, steps []frStep) (*Runner, *frPool, *frConn, *frTx) {
	t.Helper()
	tx := &frTx{fakeTx: &fakeTx{}, t: t, steps: steps}
	conn := &frConn{t: t, tx: tx}
	pool := &frPool{t: t, conn: conn}
	r, err := NewRunner(pool, Timeouts{Acquire: time.Second, Begin: time.Second, Transaction: 2 * time.Second, Commit: time.Second, Cleanup: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return r, pool, conn, tx
}

// Independent fixed SQL expectations, not generated by newRegistrationSQL. Full
// string and argument equality are checked for every script step. The long
// history predicate is pinned structurally and textually, not executed as SQL.
func frExpectedSQL() registrationSQL {
	table := func(n string) string { return `"wt_synthetic_registry"."` + n + `"` }
	current, version := table("callback_destinations"), table("callback_destination_versions")
	policy, key, binding, receipt := table("callback_registration_policies"), table("callback_registration_keys"), table("callback_registration_bindings"), table("callback_registration_receipts")
	count := "(CASE WHEN pg_catalog.jsonb_typeof(subscription_snapshot)='array' THEN pg_catalog.jsonb_array_length(subscription_snapshot) ELSE 0 END)"
	bad := []string{"pg_catalog.jsonb_typeof(subscription_snapshot) IS DISTINCT FROM 'array'", count + ">4", "pg_catalog.octet_length(subscription_snapshot::text)>16384"}
	for n := 0; n < 4; n++ {
		index := strconv.Itoa(n)
		x := "(subscription_snapshot->" + index + ")"
		bad = append(bad, "("+count+">"+index+" AND (pg_catalog.jsonb_typeof("+x+") IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof("+x+"->'destinationId') IS DISTINCT FROM 'string' OR ("+x+"->>'destinationId') COLLATE pg_catalog.\"C\" !~ '^[A-Za-z0-9_-]{1,128}$'))")
	}
	obligations := []string{
		"EXISTS(SELECT 1 FROM " + table("scope_limits") + " WHERE tenant=$1 AND (destination_id=$2 OR (scope_kind='destination' AND scope_id=$2)))",
		"EXISTS(SELECT 1 FROM " + table("scope_slots") + " WHERE tenant=$1 AND scope_kind='destination' AND scope_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("callback_subscription_versions") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("admission_reservations") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("callback_outbox") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("commands") + " WHERE tenant=$1 AND (subscription_snapshot @> pg_catalog.jsonb_build_array(pg_catalog.jsonb_build_object('destinationId',$2::text)) OR (" + strings.Join(bad, " OR ") + ")))",
	}
	occupied := append([]string{}, obligations...)
	for _, entry := range []struct{ table, col string }{{current, "id"}, {version, "destination_id"}, {policy, "destination_id"}, {key, "destination_id"}, {binding, "destination_id"}, {receipt, "destination_id"}} {
		occupied = append(occupied, "EXISTS(SELECT 1 FROM "+entry.table+" WHERE tenant=$1 AND "+entry.col+"=$2)")
	}
	extra := append([]string{}, obligations...)
	for _, name := range []string{version, policy, key, binding, receipt} {
		extra = append(extra, "(SELECT pg_catalog.count(*) FROM "+name+" WHERE tenant=$1 AND destination_id=$2)<>1")
	}
	return registrationSQL{
		tenant:        "SELECT scope_id FROM " + table("scope_limits") + " WHERE scope_kind='tenant' AND tenant=$1 AND scope_id=$1 FOR UPDATE",
		app:           "SELECT id,active FROM " + table("developer_apps") + " WHERE tenant=$1 AND id=$2 FOR SHARE",
		receipt:       "SELECT intent_bytes FROM " + receipt + " WHERE tenant=$1 AND authority_id=$2 AND operation_id=$3 FOR SHARE",
		occupied:      "SELECT " + strings.Join(occupied, " OR "),
		current:       "SELECT app,canonical_url,policy_version,key_ref,key_version,state,enabled FROM " + current + " WHERE tenant=$1 AND id=$2 FOR SHARE",
		version:       "SELECT canonical_url,key_ref,revoked_at IS NULL FROM " + version + " WHERE tenant=$1 AND app=$2 AND destination_id=$3 AND policy_version=$4 AND key_version=$5 FOR SHARE",
		policy:        "SELECT policy_identity,canonical_url,cidrs,signature_profile FROM " + policy + " WHERE tenant=$1 AND app=$2 AND destination_id=$3 AND policy_version=$4 FOR SHARE",
		key:           "SELECT key_identity,key_ref,provider_authority_id,revision_kind,revision_id FROM " + key + " WHERE tenant=$1 AND app=$2 AND destination_id=$3 AND key_version=$4 FOR SHARE",
		binding:       "SELECT policy_identity,key_identity,binding_identity,descriptor_bytes FROM " + binding + " WHERE tenant=$1 AND app=$2 AND destination_id=$3 AND policy_version=$4 AND key_version=$5 FOR SHARE",
		fullReceipt:   "SELECT app,destination_id,policy_version,key_version,binding_identity,intent_bytes FROM " + receipt + " WHERE tenant=$1 AND authority_id=$2 AND operation_id=$3 FOR SHARE",
		obligations:   "SELECT " + strings.Join(extra, " OR "),
		insertCurrent: "INSERT INTO " + current + "(tenant,app,id,policy_version,key_version,canonical_url,key_ref,state) VALUES($1,$2,$3,$4,$5,$6,$7,'pending_review')",
		insertPolicy:  "INSERT INTO " + policy + "(tenant,app,destination_id,policy_version,policy_identity,canonical_url,cidrs,signature_profile) VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
		insertKey:     "INSERT INTO " + key + "(tenant,app,destination_id,key_version,key_identity,key_ref,provider_authority_id,revision_kind,revision_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		insertBinding: "INSERT INTO " + binding + "(tenant,app,destination_id,policy_version,key_version,policy_identity,key_identity,binding_identity,descriptor_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		insertReceipt: "INSERT INTO " + receipt + "(authority_id,tenant,operation_id,app,destination_id,policy_version,key_version,binding_identity,intent_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
	}
}
func frScript(t *testing.T, i registrationIntent, replay bool) []frStep {
	t.Helper()
	q, d := frExpectedSQL(), i.descriptor.Value()
	canonical, err := i.canonical()
	if err != nil {
		t.Fatal(err)
	}
	query := func(name, sql string, args []any, values ...any) frStep {
		return frStep{name: name, sql: sql, args: args, values: values}
	}
	exec := func(name, sql string, args ...any) frStep {
		return frStep{name: name, sql: sql, args: args, exec: true, rows: 1}
	}
	s := []frStep{exec("search_path", `SET LOCAL search_path TO pg_catalog, "wt_synthetic_registry", pg_temp`), query("tenant", q.tenant, []any{d.Tenant}, d.Tenant), query("app", q.app, []any{d.Tenant, d.App}, d.App, true), query("receipt", q.receipt, []any{d.Tenant, i.authorityID, i.operationID}, canonical)}
	if !replay {
		s[3].values, s[3].err = nil, pgx.ErrNoRows
		s = append(s, query("occupied", q.occupied, []any{d.Tenant, d.Destination}, false),
			exec("insert_current", q.insertCurrent, d.Tenant, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, d.URL, d.KeyRef),
			exec("insert_policy", q.insertPolicy, d.Tenant, d.App, d.Destination, d.PolicyVersion, i.descriptor.PolicyIdentity(), d.URL, d.CIDRs, d.SignatureProfile),
			exec("insert_key", q.insertKey, d.Tenant, d.App, d.Destination, d.KeyVersion, i.descriptor.KeyIdentity(), d.KeyRef, d.ProviderAuthorityID, "exact", d.ProviderRevision.ID),
			exec("insert_binding", q.insertBinding, d.Tenant, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, i.descriptor.PolicyIdentity(), i.descriptor.KeyIdentity(), i.descriptor.BindingIdentity(), i.descriptor.CanonicalBytes()),
			exec("insert_receipt", q.insertReceipt, i.authorityID, d.Tenant, i.operationID, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, i.descriptor.BindingIdentity(), canonical))
	}
	tuple := []any{d.Tenant, d.App, d.Destination, d.PolicyVersion, d.KeyVersion}
	s = append(s,
		query("current", q.current, []any{d.Tenant, d.Destination}, d.App, d.URL, d.PolicyVersion, d.KeyRef, d.KeyVersion, "pending_review", false),
		query("version", q.version, tuple, d.URL, d.KeyRef, true),
		query("policy", q.policy, []any{d.Tenant, d.App, d.Destination, d.PolicyVersion}, i.descriptor.PolicyIdentity(), d.URL, d.CIDRs, d.SignatureProfile),
		query("key", q.key, []any{d.Tenant, d.App, d.Destination, d.KeyVersion}, i.descriptor.KeyIdentity(), d.KeyRef, d.ProviderAuthorityID, "exact", d.ProviderRevision.ID),
		query("binding", q.binding, tuple, i.descriptor.PolicyIdentity(), i.descriptor.KeyIdentity(), i.descriptor.BindingIdentity(), i.descriptor.CanonicalBytes()),
		query("full_receipt", q.fullReceipt, []any{d.Tenant, i.authorityID, i.operationID}, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, i.descriptor.BindingIdentity(), canonical),
		query("obligations", q.obligations, []any{d.Tenant, d.Destination}, false),
		exec("constraints", "SET CONSTRAINTS ALL IMMEDIATE"))
	return s
}
func frAssertStopped(t *testing.T, pool *frPool, conn *frConn, tx *frTx, calls, commits, dml int) {
	t.Helper()
	if pool.acquires != 1 || conn.begins != 1 || conn.releases != 1 || tx.index != calls || tx.commits != commits || tx.rollbacks != 1 || tx.dml != dml || !tx.rollbackLive || !tx.rollbackBounded {
		t.Fatalf("wrong lifecycle: acquire=%d begin=%d release=%d calls=%d/%d commit=%d/%d rollback=%d dml=%d/%d cleanup=%v/%v", pool.acquires, conn.begins, conn.releases, tx.index, calls, tx.commits, commits, tx.rollbacks, tx.dml, dml, tx.rollbackLive, tx.rollbackBounded)
	}
}
func frAssertError(t *testing.T, out registrationOutcome, err, want error) {
	t.Helper()
	if out != "" || !errors.Is(err, want) {
		t.Fatalf("outcome %q error %v, want empty and %v", out, err, want)
	}
	if strings.Contains(fmt.Sprint(err), "DO_NOT_ECHO") {
		t.Fatal("driver error leaked")
	}
}

func TestFreshRegistrationCandidateSuccessReplayObserve(t *testing.T) {
	for _, mode := range []string{"fresh", "replay", "observe"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			replay := mode != "fresh"
			steps := frScript(t, i, replay)
			r, pool, conn, tx := frRunner(t, steps)
			v := frPermit(t, i)
			call := registerFreshPending
			want, count, dml := registrationPending, 18, 5
			stages := []registrationStage{registrationBefore, registrationLocked, registrationDecision}
			if replay {
				want, count, dml = registrationObserved, 12, 0
			}
			if mode == "observe" {
				call = observeFreshPending
				stages = []registrationStage{registrationObserveBefore, registrationObserveLocked, registrationObserveDecision}
				v.barrier.closed = true
			}
			out, err := call(context.Background(), r, v, i)
			if err != nil || out != want || !reflect.DeepEqual(v.stages, stages) || v.closeCalls != 0 {
				t.Fatalf("result=%q err=%v stages=%v", out, err, v.stages)
			}
			frAssertStopped(t, pool, conn, tx, count, 1, dml)
			if conn.discards != 0 || (mode == "observe" && !v.barrier.closed) {
				t.Fatal("success changed incident or discarded healthy connection")
			}
		})
	}
}

func TestFreshRegistrationCandidateInvalidBeforeAcquire(t *testing.T) {
	base := frIntent(t)
	changes := map[string]func(*registrationIntent){"descriptor_zero": func(i *registrationIntent) { i.descriptor = registration.Descriptor{} }, "expected_revision": func(i *registrationIntent) { i.expectedRevision = 1 }, "expected_negative": func(i *registrationIntent) { i.expectedRevision = -1 }}
	for _, field := range []string{"schema", "authority", "operation", "actor", "approval", "fence", "history"} {
		for _, bad := range []string{"", "has space", "has/slash", "非ASCII", strings.Repeat("a", 129)} {
			field, bad := field, bad
			changes[field+"_"+bad] = func(i *registrationIntent) {
				switch field {
				case "schema":
					i.schema = bad
				case "authority":
					i.authorityID = bad
				case "operation":
					i.operationID = bad
				case "actor":
					i.actor = bad
				case "approval":
					i.approvalRef = bad
				case "fence":
					i.fenceRef = bad
				case "history":
					i.historyRef = bad
				}
			}
		}
	}
	for _, bad := range []string{"public", "pg_catalog", "pg_temp", "another_schema", "WT_SYNTHETIC_REGISTRY"} {
		bad := bad
		changes["schema_"+bad] = func(i *registrationIntent) { i.schema = bad }
	}
	for _, bad := range []string{"bad", "cbplan-v1:sha256:" + strings.Repeat("A", 64), "cbplan-v1:sha256:" + strings.Repeat("z", 64), "cbplan-v2:sha256:" + strings.Repeat("a", 64), "cbplan-v1:sha256:" + strings.Repeat("a", 63)} {
		bad := bad
		changes["plan_"+bad] = func(i *registrationIntent) { i.planIdentity = bad }
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			i := base
			change(&i)
			r, p, c, tx := frRunner(t, nil)
			v := frPermit(t, base)
			out, err := registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, errRegistrationInvalid)
			if p.acquires != 0 || c.begins != 0 || tx.index != 0 || len(v.stages) != 0 {
				t.Fatal("invalid intent acquired resources or reached verifier")
			}
		})
	}
	for _, name := range []string{"nil_context", "nil_runner", "nil_verifier", "typed_nil_verifier", "zero_verifier", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			r, p, _, _ := frRunner(t, nil)
			var ctx context.Context = context.Background()
			var v registrationVerifier = frPermit(t, base)
			want := errRegistrationInvalid
			switch name {
			case "nil_context":
				ctx = nil
			case "nil_runner":
				r = nil
			case "nil_verifier":
				v = nil
			case "typed_nil_verifier":
				var typed *frVerifier
				v = typed
			case "zero_verifier":
				v = &frVerifier{t: t}
				want = errRegistrationDenied
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = errRegistrationUnavailable
			}
			out, err := registerFreshPending(ctx, r, v, base)
			frAssertError(t, out, err, want)
			if p.acquires != 0 {
				t.Fatal("invalid capability acquired pool")
			}
		})
	}
	for _, name := range []string{"grant", "actor", "provider", "schema", "fence", "history", "expired", "closed", "missing_barrier", "intent_mismatch"} {
		t.Run("assumption_"+name, func(t *testing.T) {
			r, p, _, _ := frRunner(t, nil)
			v := frPermit(t, base)
			switch name {
			case "grant":
				v.facts.grant = false
			case "actor":
				v.facts.actor = false
			case "provider":
				v.facts.provider = false
			case "schema":
				v.facts.schema = false
			case "fence":
				v.facts.fence = false
			case "history":
				v.facts.history = false
			case "expired":
				v.expires = v.now
			case "closed":
				v.barrier.closed = true
			case "missing_barrier":
				v.barrier = nil
			case "intent_mismatch":
				v.expected = []byte("other intent")
			}
			out, err := registerFreshPending(context.Background(), r, v, base)
			frAssertError(t, out, err, errRegistrationDenied)
			if p.acquires != 0 || len(v.stages) != 1 || v.closeCalls != 0 {
				t.Fatal("failed assumption reached resources")
			}
		})
	}
}

func TestFreshRegistrationCandidateIntentBindsAllFields(t *testing.T) {
	base := frIntent(t)
	canonical, _ := base.canonical()
	changes := map[string]func(*registrationIntent){
		"authority": func(i *registrationIntent) { i.authorityID += "2" }, "operation": func(i *registrationIntent) { i.operationID += "2" }, "actor": func(i *registrationIntent) { i.actor += "2" }, "approval": func(i *registrationIntent) { i.approvalRef += "2" }, "fence": func(i *registrationIntent) { i.fenceRef += "2" }, "history": func(i *registrationIntent) { i.historyRef += "2" }, "plan_changed": func(i *registrationIntent) { i.planIdentity = "cbplan-v1:sha256:" + strings.Repeat("b", 64) }, "plan_removed": func(i *registrationIntent) { i.planIdentity = "" },
	}
	for name, change := range map[string]func(*registration.Value){"tenant": func(v *registration.Value) { v.Tenant += "2" }, "app": func(v *registration.Value) { v.App += "2" }, "destination": func(v *registration.Value) { v.Destination += "2" }, "policy_version": func(v *registration.Value) { v.PolicyVersion++ }, "key_version": func(v *registration.Value) { v.KeyVersion++ }, "url": func(v *registration.Value) { v.URL += "2" }, "cidrs": func(v *registration.Value) { v.CIDRs[0] = "8.8.4.0/24" }, "key_ref": func(v *registration.Value) { v.KeyRef += "2" }, "provider_authority": func(v *registration.Value) { v.ProviderAuthorityID += "2" }, "provider_revision": func(v *registration.Value) { v.ProviderRevision.ID += "2" }} {
		change := change
		changes[name] = func(i *registrationIntent) { frChangeDescriptor(t, i, change) }
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			i := base
			change(&i)
			altered, err := i.canonical()
			if err != nil || bytes.Equal(altered, canonical) {
				t.Fatal("changed field absent from exact intent")
			}
			// The old fixture grant is not permission for the changed intent.
			r, p, _, _ := frRunner(t, nil)
			out, err := registerFreshPending(context.Background(), r, frPermit(t, base), i)
			frAssertError(t, out, err, errRegistrationDenied)
			if p.acquires != 0 {
				t.Fatal("changed grant acquired")
			}
			// Even with new assumed authority, a receipt bound to different bytes
			// conflicts instead of repairing, ignoring or overwriting it.
			steps := frScript(t, i, true)
			steps[3].values = []any{canonical}
			r, p, c, tx := frRunner(t, steps)
			v := frPermit(t, i)
			out, err = registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, errRegistrationConflict)
			frAssertStopped(t, p, c, tx, 4, 0, 0)
		})
	}
	var obj map[string]any
	if json.Unmarshal(canonical, &obj) != nil || len(canonical) > 32768 || obj["format"] != "worktool-fresh-registration-intent-v1" || obj["action"] != "REGISTER_FRESH_PENDING" || obj["outcome"] != "REGISTERED_PENDING_NOT_ENABLED" || obj["database_schema"] != "wt_synthetic_registry" || obj["expected_revision"] != float64(0) || obj["new_revision"] != float64(1) {
		t.Fatal("fixed intent contract drift")
	}
}

func TestFreshRegistrationCandidateAuthorityStages(t *testing.T) {
	for _, observe := range []bool{false, true} {
		stages := []registrationStage{registrationBefore, registrationLocked, registrationDecision}
		counts := []int{0, 3, 18}
		call := registerFreshPending
		if observe {
			stages = []registrationStage{registrationObserveBefore, registrationObserveLocked, registrationObserveDecision}
			counts[2] = 12
			call = observeFreshPending
		}
		for n, stage := range stages {
			t.Run(string(stage), func(t *testing.T) {
				i := frIntent(t)
				r, p, c, tx := frRunner(t, frScript(t, i, observe))
				v := frPermit(t, i)
				v.deny = stage
				out, err := call(context.Background(), r, v, i)
				frAssertError(t, out, err, errRegistrationDenied)
				if !reflect.DeepEqual(v.stages, stages[:n+1]) || v.closeCalls != 0 {
					t.Fatal("authority stages skipped or retried")
				}
				if n == 0 {
					if p.acquires != 0 {
						t.Fatal("denied before resources acquired")
					}
					return
				}
				dml := 0
				if n == 2 && !observe {
					dml = 5
				}
				frAssertStopped(t, p, c, tx, counts[n], 0, dml)
			})
		}
	}
}

func TestFreshRegistrationCandidateScopeAndHistoryRefusal(t *testing.T) {
	for _, tc := range []struct {
		name      string
		step      int
		values    []any
		err, want error
	}{
		{"missing_tenant", 1, nil, pgx.ErrNoRows, errRegistrationUnavailable}, {"wrong_tenant", 1, []any{"other_tenant"}, nil, errRegistrationIntegrity},
		{"missing_app", 2, nil, pgx.ErrNoRows, errRegistrationUnavailable}, {"wrong_app", 2, []any{"other_app", true}, nil, errRegistrationDenied}, {"inactive_app", 2, []any{"app_synthetic", false}, nil, errRegistrationDenied},
		{"occupied_any_app_any_state_or_history", 4, []any{true}, nil, errRegistrationExisting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := frIntent(t)
			steps := frScript(t, i, false)
			steps[tc.step].values, steps[tc.step].err = tc.values, tc.err
			r, p, c, tx := frRunner(t, steps)
			out, err := registerFreshPending(context.Background(), r, frPermit(t, i), i)
			frAssertError(t, out, err, tc.want)
			frAssertStopped(t, p, c, tx, tc.step+1, 0, 0)
		})
	}
	i := frIntent(t)
	steps := frScript(t, i, true)
	steps[3].values, steps[3].err = nil, pgx.ErrNoRows
	r, p, c, tx := frRunner(t, steps)
	out, err := observeFreshPending(context.Background(), r, frPermit(t, i), i)
	frAssertError(t, out, err, errRegistrationIntegrity)
	frAssertStopped(t, p, c, tx, 4, 0, 0)
	q := newRegistrationSQL("wt_synthetic_registry")
	if !reflect.DeepEqual(q, frExpectedSQL()) {
		t.Fatal("fixed SQL protocol drift")
	}
	for _, sql := range []string{q.occupied, q.obligations} {
		if strings.Contains(sql, "app=$") || strings.Contains(sql, "state=") || strings.Contains(sql, "retired_at") || strings.Contains(sql, "revoked_at") || strings.Contains(sql, "jsonb_array_elements") {
			t.Fatal("history narrowed to current app/state or expands unbounded array")
		}
		for _, table := range []string{"scope_limits", "scope_slots", "callback_subscription_versions", "admission_reservations", "callback_outbox", "commands"} {
			if !strings.Contains(sql, `"wt_synthetic_registry"."`+table+`"`) {
				t.Fatal("history source missing: " + table)
			}
		}
		for _, text := range []string{"destinationId", "IS DISTINCT FROM 'array'", ">4", ">16384", "IS DISTINCT FROM 'object'", "IS DISTINCT FROM 'string'", "^[A-Za-z0-9_-]{1,128}$"} {
			if !strings.Contains(sql, text) {
				t.Fatal("malformed history guard missing: " + text)
			}
		}
		for n := 0; n < 4; n++ {
			if !strings.Contains(sql, "subscription_snapshot->"+strconv.Itoa(n)) {
				t.Fatal("bounded history probe missing")
			}
		}
	}
}

func TestFreshRegistrationCandidateEveryGraphFieldAndMissingRow(t *testing.T) {
	i := frIntent(t)
	base := frScript(t, i, true)
	for step := 4; step < len(base)-1; step++ {
		for field := range base[step].values {
			t.Run(fmt.Sprintf("%s_field_%d", base[step].name, field), func(t *testing.T) {
				steps := frScript(t, i, true)
				switch val := steps[step].values[field].(type) {
				case string:
					steps[step].values[field] = val + "_mismatch"
				case int64:
					steps[step].values[field] = val + 1
				case bool:
					steps[step].values[field] = !val
				case []byte:
					steps[step].values[field] = append(append([]byte{}, val...), 'x')
				case []string:
					copy := append([]string{}, val...)
					copy[0] += "_mismatch"
					steps[step].values[field] = copy
				default:
					t.Fatal("unhandled graph field type")
				}
				r, p, c, tx := frRunner(t, steps)
				out, err := observeFreshPending(context.Background(), r, frPermit(t, i), i)
				frAssertError(t, out, err, errRegistrationIntegrity)
				frAssertStopped(t, p, c, tx, step+1, 0, 0)
			})
		}
		t.Run(base[step].name+"_missing", func(t *testing.T) {
			steps := frScript(t, i, true)
			steps[step].err = pgx.ErrNoRows
			r, p, c, tx := frRunner(t, steps)
			out, err := observeFreshPending(context.Background(), r, frPermit(t, i), i)
			frAssertError(t, out, err, errRegistrationUnavailable)
			frAssertStopped(t, p, c, tx, step+1, 0, 0)
		})
	}
	for _, name := range []string{"cidrs_empty", "cidrs_reordered", "current_active", "current_revoked"} {
		t.Run(name, func(t *testing.T) {
			steps := frScript(t, i, true)
			step := 6
			switch name {
			case "cidrs_empty":
				steps[step].values[2] = []string{}
			case "cidrs_reordered":
				cidrs := i.descriptor.Value().CIDRs
				cidrs[0], cidrs[1] = cidrs[1], cidrs[0]
				steps[step].values[2] = cidrs
			case "current_active":
				step = 4
				steps[step].values[5] = "active"
			case "current_revoked":
				step = 4
				steps[step].values[5] = "revoked"
			}
			r, p, c, tx := frRunner(t, steps)
			out, err := registerFreshPending(context.Background(), r, frPermit(t, i), i)
			frAssertError(t, out, err, errRegistrationIntegrity)
			frAssertStopped(t, p, c, tx, step+1, 0, 0)
		})
	}
}

func frDMLBefore(steps []frStep, calls int) int {
	count := 0
	for _, step := range steps[:calls] {
		if step.exec && strings.HasPrefix(step.sql, "INSERT INTO ") {
			count++
		}
	}
	return count
}
func TestFreshRegistrationCandidateEverySQLErrorAndAffectedRows(t *testing.T) {
	for _, replay := range []bool{false, true} {
		i := frIntent(t)
		base := frScript(t, i, replay)
		for at := range base {
			t.Run(fmt.Sprintf("replay_%v_%s", replay, base[at].name), func(t *testing.T) {
				steps := frScript(t, i, replay)
				steps[at].err = errors.New("DO_NOT_ECHO SQL parameter secret")
				r, p, c, tx := frRunner(t, steps)
				v := frPermit(t, i)
				out, err := registerFreshPending(context.Background(), r, v, i)
				frAssertError(t, out, err, errRegistrationUnavailable)
				frAssertStopped(t, p, c, tx, at+1, 0, frDMLBefore(steps, at+1))
				if v.closeCalls != 0 || c.discards != 0 {
					t.Fatal("clean rollback incorrectly treated as unknown")
				}
			})
		}
	}
	i := frIntent(t)
	base := frScript(t, i, false)
	for at, step := range base {
		if !step.exec || !strings.HasPrefix(step.sql, "INSERT INTO ") {
			continue
		}
		for _, rows := range []int64{0, 2} {
			t.Run(fmt.Sprintf("%s_rows_%d", step.name, rows), func(t *testing.T) {
				steps := frScript(t, i, false)
				steps[at].rows = rows
				r, p, c, tx := frRunner(t, steps)
				out, err := registerFreshPending(context.Background(), r, frPermit(t, i), i)
				frAssertError(t, out, err, errRegistrationIntegrity)
				frAssertStopped(t, p, c, tx, at+1, 0, frDMLBefore(steps, at+1))
			})
		}
	}
}

func TestFreshRegistrationCandidateAcquireBeginFailures(t *testing.T) {
	for _, mode := range []string{"acquire", "begin", "begin_and_discard"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			r, p, c, tx := frRunner(t, nil)
			v := frPermit(t, i)
			want := errRegistrationUnavailable
			if mode == "acquire" {
				p.err = errors.New("DO_NOT_ECHO acquire")
			} else {
				c.beginErr = errors.New("DO_NOT_ECHO begin")
			}
			if mode == "begin_and_discard" {
				c.discardErr = errors.New("DO_NOT_ECHO discard")
				want = errRegistrationUnknown
			}
			out, err := registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, want)
			if p.acquires != 1 || tx.index != 0 || tx.commits != 0 || tx.rollbacks != 0 {
				t.Fatal("acquire/begin failure entered transaction or retried")
			}
			if mode == "acquire" {
				if c.begins != 0 || c.releases != 0 || c.discards != 0 {
					t.Fatal("failed acquire connection lifecycle")
				}
			} else if c.begins != 1 || c.releases != 1 || c.discards != 1 || !c.discardLive || !c.discardBounded {
				t.Fatal("failed begin cleanup")
			}
			if mode == "begin_and_discard" && (!errors.Is(err, ErrCleanupUnconfirmed) || !v.barrier.closed || v.closeCalls != 1) {
				t.Fatal("begin cleanup uncertainty did not close model")
			}
		})
	}
}

func TestFreshRegistrationCandidateUnknownCleanupAndBarrier(t *testing.T) {
	for _, mode := range []string{"commit", "commit_tx_closed", "commit_discard", "rollback_after_error", "rollback_after_commit", "rollback_and_discard", "barrier_error", "barrier_panic", "barrier_deadline"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			steps := frScript(t, i, false)
			r, p, c, tx := frRunner(t, steps)
			v := frPermit(t, i)
			calls, commits, dml := 18, 1, 5
			cleanup := false
			switch mode {
			case "commit", "barrier_error", "barrier_panic", "barrier_deadline":
				tx.commitErr = errors.New("DO_NOT_ECHO lost commit reply")
			case "commit_tx_closed":
				tx.commitErr = errors.New("DO_NOT_ECHO commit")
				tx.rollbackErr = pgx.ErrTxClosed
			case "commit_discard":
				tx.commitErr = errors.New("DO_NOT_ECHO commit")
				c.discardErr = errors.New("DO_NOT_ECHO discard")
				cleanup = true
			case "rollback_after_error":
				steps[4].err = errors.New("DO_NOT_ECHO query")
				tx.rollbackErr = errors.New("DO_NOT_ECHO rollback")
				calls, commits, dml, cleanup = 5, 0, 0, true
			case "rollback_after_commit":
				tx.rollbackErr = errors.New("DO_NOT_ECHO rollback")
				cleanup = true
			case "rollback_and_discard":
				steps[4].err = errors.New("DO_NOT_ECHO query")
				tx.rollbackErr = errors.New("DO_NOT_ECHO rollback")
				c.discardErr = errors.New("DO_NOT_ECHO discard")
				calls, commits, dml, cleanup = 5, 0, 0, true
			}
			if mode == "barrier_error" {
				v.closeErr = errors.New("DO_NOT_ECHO persistence")
			}
			if mode == "barrier_panic" {
				v.closePanic = true
			}
			if mode == "barrier_deadline" {
				v.closeWait = true
				r.timeouts.Cleanup = time.Millisecond
			}
			out, err := registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, errRegistrationUnknown)
			frAssertStopped(t, p, c, tx, calls, commits, dml)
			if c.discards != 1 || !c.discardLive || !c.discardBounded || v.closeCalls != 1 || !v.closeLive || !v.closeBounded || errors.Is(err, ErrCleanupUnconfirmed) != cleanup {
				t.Fatal("unknown outcome did not get independent bounded cleanup/barrier attempt")
			}
			barrierFailed := strings.HasPrefix(mode, "barrier_")
			if errors.Is(err, errRegistrationBarrier) != barrierFailed || v.barrier.closed == barrierFailed {
				t.Fatal("barrier result was misrepresented")
			}
			if !barrierFailed {
				// Reconstruct both Runner and assumed verifier, sharing only the
				// model's incident record. This models, not proves, restart closure.
				r2, p2, _, _ := frRunner(t, nil)
				v2 := frPermit(t, i)
				v2.barrier = v.barrier
				out, err = registerFreshPending(context.Background(), r2, v2, i)
				frAssertError(t, out, err, errRegistrationDenied)
				if p2.acquires != 0 {
					t.Fatal("closed incident allowed reconstructed writer")
				}
				// Exact observation can report assumed graph evidence but cannot
				// clear the incident or create another write permission.
				r3, p3, c3, tx3 := frRunner(t, frScript(t, i, true))
				out, err = observeFreshPending(context.Background(), r3, v2, i)
				if err != nil || out != registrationObserved || !v2.barrier.closed || v2.closeCalls != 0 {
					t.Fatal("observation cleared or misrepresented incident")
				}
				frAssertStopped(t, p3, c3, tx3, 12, 1, 0)
				r4, p4, _, _ := frRunner(t, nil)
				out, err = registerFreshPending(context.Background(), r4, v2, i)
				frAssertError(t, out, err, errRegistrationDenied)
				if p4.acquires != 0 {
					t.Fatal("observation re-enabled mutation")
				}
			}
		})
	}
}

func TestFreshRegistrationCandidatePanicsNeverRetry(t *testing.T) {
	for _, mode := range []string{"verifier_before", "verifier_locked", "verifier_decision", "sql", "commit", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			steps := frScript(t, i, false)
			r, p, c, tx := frRunner(t, steps)
			v := frPermit(t, i)
			calls, commits, dml := 18, 0, 5
			switch mode {
			case "verifier_before":
				v.panicAt = registrationBefore
				calls, dml = 0, 0
			case "verifier_locked":
				v.panicAt = registrationLocked
				calls, dml = 3, 0
			case "verifier_decision":
				v.panicAt = registrationDecision
			case "sql":
				steps[5].hook = func() { panic("synthetic SQL panic") }
				calls, dml = 6, 1
			case "commit":
				tx.commitPanic = true
				commits = 1
			case "rollback":
				steps[4].err = errors.New("DO_NOT_ECHO SQL")
				tx.rollbackPanic = true
				calls, dml = 5, 0
			}
			out, err := registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, errRegistrationUnknown)
			if !v.barrier.closed || v.closeCalls != 1 || !v.closeLive || !v.closeBounded {
				t.Fatal("panic did not close model with independent context")
			}
			if mode == "verifier_before" {
				if p.acquires != 0 || tx.index != 0 {
					t.Fatal("pre-resource panic acquired")
				}
			} else {
				frAssertStopped(t, p, c, tx, calls, commits, dml)
			}
		})
	}
}

func TestFreshRegistrationCandidateCancellationAndVerifierDeadline(t *testing.T) {
	for _, mode := range []string{"sql", "sql_cleanup_failure", "final_verifier", "verifier_deadline"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			steps := frScript(t, i, false)
			r, p, c, tx := frRunner(t, steps)
			v := frPermit(t, i)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want, calls, dml := errRegistrationUnavailable, 6, 1
			if mode == "final_verifier" {
				v.hook = func(s registrationStage) {
					if s == registrationDecision {
						cancel()
					}
				}
				want, calls, dml = errRegistrationDenied, 18, 5
			} else if mode == "verifier_deadline" {
				r.timeouts.Transaction = time.Millisecond
				v.hook = func(registrationStage) { time.Sleep(2 * time.Millisecond) }
				want, calls, dml = errRegistrationDenied, 0, 0
			} else {
				steps[5].hook = cancel
				steps[5].err = context.Canceled
			}
			if mode == "sql_cleanup_failure" {
				tx.rollbackErr = errors.New("DO_NOT_ECHO cleanup")
				want = errRegistrationUnknown
			}
			out, err := registerFreshPending(ctx, r, v, i)
			frAssertError(t, out, err, want)
			if calls == 0 {
				if p.acquires != 0 {
					t.Fatal("expired verifier acquired")
				}
				return
			}
			frAssertStopped(t, p, c, tx, calls, 0, dml)
			if mode == "sql_cleanup_failure" && (!v.closeLive || !v.closeBounded || !v.barrier.closed || !c.discardLive || !c.discardBounded || !errors.Is(err, ErrCleanupUnconfirmed)) {
				t.Fatal("cancelled request poisoned independent cleanup")
			}
		})
	}
}

func TestFreshRegistrationCandidateProductionContainment(t *testing.T) {
	root := filepath.Clean("../../..")
	candidate := filepath.Join(root, "internal", "store", "pg", "fresh_registration_candidate.go")
	allowed := map[string]bool{"bytes": true, "context": true, "encoding/hex": true, "encoding/json": true, "errors": true, "reflect": true, "strconv": true, "strings": true,
		"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration": true, "github.com/jackc/pgx/v5": true, "github.com/jackc/pgx/v5/pgconn": true}
	privateSurface := map[string]bool{"registrationVerifier": true, "registrationIntent": true, "registrationOutcome": true, "registerFreshPending": true, "observeFreshPending": true, "runFreshRegistration": true, "registrationGuardedPool": true, "registrationGuardedConnection": true, "registrationGuardedTx": true, "registrationGuardedRow": true}
	productionFiles := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		productionFiles++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
		if err != nil {
			return err
		}
		isCandidate := filepath.Clean(path) == candidate
		if bytes.Contains(raw, []byte("fresh_registration_registry_candidate.sql")) || bytes.Contains(raw, []byte("fresh_registration_candidate.sql")) {
			t.Errorf("production embeds/loads candidate DDL: %s", path)
		}
		if !isCandidate {
			ast.Inspect(f, func(n ast.Node) bool {
				if ident, ok := n.(*ast.Ident); ok && privateSurface[ident.Name] {
					t.Errorf("non-test candidate wiring outside containment file: %s (%s)", path, ident.Name)
				}
				if method, ok := n.(*ast.FuncDecl); ok && method.Recv != nil && method.Name.Name == "registrationVerifierSeal" {
					t.Errorf("production verifier implementation: %s", path)
				}
				return true
			})
			return nil
		}
		for _, imp := range f.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if !allowed[name] {
				t.Errorf("unreviewed candidate import %s", name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.GoStmt:
				t.Error("candidate starts background work")
			case *ast.FuncDecl:
				allowedDriverMethod := false
				if node.Recv != nil && len(node.Recv.List) == 1 {
					receiver := node.Recv.List[0].Type
					if pointer, ok := receiver.(*ast.StarExpr); ok {
						receiver = pointer.X
					}
					if ident, ok := receiver.(*ast.Ident); ok {
						methods := map[string]map[string]bool{
							"registrationGuardedPool":       {"Acquire": true},
							"registrationGuardedConnection": {"BeginTx": true},
							"registrationGuardedTx":         {"Commit": true, "Rollback": true, "Exec": true, "QueryRow": true},
							"registrationGuardedRow":        {"Scan": true},
						}
						allowedDriverMethod = methods[ident.Name][node.Name.Name]
					}
				}
				// Standard interface methods on these private containment adapters
				// are allowed. No exported type, factory or writer is permitted.
				if (node.Name.IsExported() && !allowedDriverMethod) || node.Name.Name == "init" {
					t.Errorf("candidate exports/initializes function %s", node.Name.Name)
				}
				if node.Recv != nil && node.Name.Name == "registrationVerifierSeal" {
					t.Error("candidate supplies production verifier")
				}
			case *ast.TypeSpec:
				if node.Name.IsExported() {
					t.Errorf("candidate exports type %s", node.Name.Name)
				}
			case *ast.ValueSpec:
				for _, name := range node.Names {
					if name.IsExported() {
						t.Errorf("candidate exports value %s", name.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if productionFiles < 50 {
		t.Fatalf("containment scan unexpectedly narrow: %d files", productionFiles)
	}
}

func TestFreshRegistrationCandidateGuardedDriverPanicsDiscardOnce(t *testing.T) {
	for _, mode := range []string{"begin", "begin_nil_tx", "query_row", "row_scan", "exec", "commit", "rollback", "query_row_tx_closed", "row_scan_tx_closed", "exec_tx_closed", "commit_tx_closed"} {
		t.Run(mode, func(t *testing.T) {
			i := frIntent(t)
			steps := frScript(t, i, false)
			r, p, c, tx := frRunner(t, steps)
			v := frPermit(t, i)
			calls, commits, dml := 18, 0, 5
			if strings.HasSuffix(mode, "_tx_closed") {
				tx.rollbackErr = pgx.ErrTxClosed
			}
			base := strings.TrimSuffix(mode, "_tx_closed")
			switch base {
			case "begin":
				c.beginPanic = true
				calls, dml = 0, 0
			case "begin_nil_tx":
				c.beginNil = true
				calls, dml = 0, 0
			case "query_row":
				steps[1].hook = func() { panic("DO_NOT_ECHO synthetic QueryRow panic") }
				calls, dml = 2, 0
			case "row_scan":
				steps[1].scanPanic = true
				calls, dml = 2, 0
			case "exec":
				steps[5].hook = func() { panic("DO_NOT_ECHO synthetic Exec panic") }
				calls, dml = 6, 1
			case "commit":
				tx.commitPanic = true
				commits = 1
			case "rollback":
				steps[4].err = errors.New("DO_NOT_ECHO synthetic query failure")
				tx.rollbackPanic = true
				calls, dml = 5, 0
			}
			out, err := registerFreshPending(context.Background(), r, v, i)
			frAssertError(t, out, err, errRegistrationUnknown)
			if p.acquires != 1 || c.begins != 1 || c.releases != 1 || c.discards != 1 || !c.discardLive || !c.discardBounded || v.closeCalls != 1 || !v.barrier.closed || !v.closeLive || !v.closeBounded {
				t.Fatal("driver panic was retried, reused connection, or lost bounded cleanup/incident closure")
			}
			if base == "begin" || base == "begin_nil_tx" {
				if tx.index != 0 || tx.commits != 0 || tx.rollbacks != 0 {
					t.Fatal("failed BEGIN entered transaction")
				}
			} else {
				frAssertStopped(t, p, c, tx, calls, commits, dml)
				if !errors.Is(err, ErrCleanupUnconfirmed) {
					t.Fatal("tainted transaction presented rollback as confirmed, including ErrTxClosed")
				}
			}
			// No reconstructed mutation is authorized by this assumed incident.
			r2, p2, _, _ := frRunner(t, nil)
			v2 := frPermit(t, i)
			v2.barrier = v.barrier
			out, err = registerFreshPending(context.Background(), r2, v2, i)
			frAssertError(t, out, err, errRegistrationDenied)
			if p2.acquires != 0 {
				t.Fatal("driver panic permitted reconstructed mutation")
			}
		})
	}
}

func TestFreshRegistrationCandidateVerifierPanicPreservesCleanupUncertainty(t *testing.T) {
	for _, stage := range []registrationStage{registrationLocked, registrationDecision, registrationObserveLocked, registrationObserveDecision} {
		for _, mode := range []string{"normal_denial", "panic_clean_rollback", "panic_rollback_error", "panic_rollback_and_discard_error", "panic_rollback_panic_and_discard_error"} {
			t.Run(string(stage)+"_"+mode, func(t *testing.T) {
				i := frIntent(t)
				observe := stage == registrationObserveLocked || stage == registrationObserveDecision
				steps := frScript(t, i, observe)
				r, p, c, tx := frRunner(t, steps)
				v := frPermit(t, i)
				call := registerFreshPending
				if observe {
					call = observeFreshPending
				}
				calls, dml := 3, 0
				if stage == registrationDecision {
					calls, dml = 18, 5
				}
				if stage == registrationObserveDecision {
					calls = 12
				}
				if mode == "normal_denial" {
					v.deny = stage
				} else {
					v.panicAt = stage
				}
				cleanup := mode == "panic_rollback_error" || mode == "panic_rollback_and_discard_error" || mode == "panic_rollback_panic_and_discard_error"
				if cleanup {
					tx.rollbackErr = errors.New("DO_NOT_ECHO simultaneous rollback failure")
				}
				if strings.Contains(mode, "discard_error") {
					c.discardErr = errors.New("DO_NOT_ECHO simultaneous discard failure")
				}
				if mode == "panic_rollback_panic_and_discard_error" {
					tx.rollbackPanic = true
				}
				out, err := call(context.Background(), r, v, i)
				want := errRegistrationUnknown
				if mode == "normal_denial" {
					want = errRegistrationDenied
				}
				frAssertError(t, out, err, want)
				frAssertStopped(t, p, c, tx, calls, 0, dml)
				if errors.Is(err, ErrCleanupUnconfirmed) != cleanup {
					t.Fatal("verifier panic erased or invented simultaneous cleanup uncertainty")
				}
				if cleanup {
					if c.discards != 1 || !c.discardLive || !c.discardBounded {
						t.Fatal("combined verifier/cleanup failure was retried or not discarded once")
					}
				} else if c.discards != 0 {
					t.Fatal("clean verifier refusal unexpectedly discarded")
				}
				if mode == "normal_denial" {
					if v.closeCalls != 0 || v.barrier.closed {
						t.Fatal("ordinary verifier error was changed from DENIED to incident")
					}
				} else {
					if v.closeCalls != 1 || !v.barrier.closed || !v.closeLive || !v.closeBounded {
						t.Fatal("verifier panic did not independently close incident model")
					}
					r2, p2, _, _ := frRunner(t, nil)
					v2 := frPermit(t, i)
					v2.barrier = v.barrier
					out, err = registerFreshPending(context.Background(), r2, v2, i)
					frAssertError(t, out, err, errRegistrationDenied)
					if p2.acquires != 0 {
						t.Fatal("combined verifier failure allowed reconstructed mutation")
					}
				}
			})
		}
	}
}
