package pg

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/registration"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Local, unwired transaction candidate. There is deliberately NO exported writer,
// constructor, concrete verifier, startup hook or migration installer. The only
// verifier implementation is in _test.go. These private types contain the
// candidate; they are NOT authentication or a database privilege boundary.
// Before any real use, an independently reviewed control-plane verifier, durable
// incident barrier and least-privilege database boundary are mandatory.
type registrationVerifier interface {
	registrationVerifierSeal()
	// verify must establish exact grant/actor/intent, authenticated immutable
	// provider mapping, schema preflight, exclusive writer fence, complete retained
	// identity history/restore provenance, and a durable open mutation barrier.
	// Every stage is bounded, local-only, and must not perform provider/network I/O.
	// Observation may inspect an incident but can never clear it or permit mutation.
	verify(context.Context, registrationIntent, registrationStage) error
	// close must durably inhibit mutation/restart. No real implementation exists.
	close(context.Context, registrationIntent) error
}

type registrationStage string

const (
	registrationBefore          registrationStage = "before_resources"
	registrationLocked          registrationStage = "after_tenant_and_app_locks"
	registrationDecision        registrationStage = "before_commit"
	registrationObserveBefore   registrationStage = "observe_before_resources"
	registrationObserveLocked   registrationStage = "observe_after_locks"
	registrationObserveDecision registrationStage = "observe_before_commit"
)

type registrationIntent struct {
	descriptor                                                                 registration.Descriptor
	schema, authorityID, operationID, actor, approvalRef, fenceRef, historyRef string
	expectedRevision                                                           int64
	planIdentity                                                               string
}

type registrationOutcome string

const (
	registrationPending  registrationOutcome = "REGISTERED_PENDING_NOT_ENABLED"
	registrationObserved registrationOutcome = "OBSERVED_PENDING_NOT_ENABLED"
)

var (
	errRegistrationInvalid     = errors.New("REGISTRATION_INVALID")
	errRegistrationDenied      = errors.New("REGISTRATION_AUTHORITY_UNCONFIRMED")
	errRegistrationConflict    = errors.New("REGISTRATION_OPERATION_CONFLICT")
	errRegistrationExisting    = errors.New("REGISTRATION_EXISTING_OR_HISTORICAL_IDENTITY")
	errRegistrationIntegrity   = errors.New("REGISTRATION_GRAPH_UNCONFIRMED")
	errRegistrationUnavailable = errors.New("REGISTRATION_UNAVAILABLE")
	errRegistrationUnknown     = errors.New("REGISTRATION_OUTCOME_UNKNOWN")
	errRegistrationBarrier     = errors.New("REGISTRATION_DURABLE_BARRIER_UNCONFIRMED")
)

const registrationCandidateSchema = "wt_synthetic_registry"
const registrationSearchPath = `SET LOCAL search_path TO pg_catalog, "wt_synthetic_registry", pg_temp`

func registrationSchema(s string) bool {
	// PostgreSQL identifiers are at most 63 bytes. Explicit qualification avoids
	// search_path/temp shadowing; provenance still belongs to the missing verifier.
	return s == registrationCandidateSchema && len(s) <= 63 && asciiIdentifier(s) && s != "public" && s != "information_schema" && !strings.HasPrefix(strings.ToLower(s), "pg_")
}
func (i registrationIntent) canonical() ([]byte, error) {
	if !i.descriptor.Valid() || !registrationSchema(i.schema) || i.expectedRevision != 0 {
		return nil, errRegistrationInvalid
	}
	for _, s := range []string{i.authorityID, i.operationID, i.actor, i.approvalRef, i.fenceRef, i.historyRef} {
		if !asciiIdentifier(s) {
			return nil, errRegistrationInvalid
		}
	}
	if i.planIdentity != "" {
		const p = "cbplan-v1:sha256:"
		if !strings.HasPrefix(i.planIdentity, p) || len(i.planIdentity) != len(p)+64 {
			return nil, errRegistrationInvalid
		}
		s := strings.TrimPrefix(i.planIdentity, p)
		if _, e := hex.DecodeString(s); e != nil || strings.ToLower(s) != s {
			return nil, errRegistrationInvalid
		}
	}
	// Exact descriptor bytes, schema, actor and all audit/provenance references
	// participate in operation idempotency. These strings do not authenticate them.
	v := struct {
		Format     string              `json:"format"`
		Action     string              `json:"action"`
		Schema     string              `json:"database_schema"`
		Authority  string              `json:"authority"`
		Operation  string              `json:"operation"`
		Actor      string              `json:"actor"`
		Approval   string              `json:"approval"`
		Fence      string              `json:"fence"`
		History    string              `json:"history"`
		Expected   int64               `json:"expected_revision"`
		Revision   int64               `json:"new_revision"`
		Outcome    registrationOutcome `json:"outcome"`
		Plan       string              `json:"plan_identity"`
		Descriptor json.RawMessage     `json:"descriptor"`
	}{"worktool-fresh-registration-intent-v1", "REGISTER_FRESH_PENDING", i.schema, i.authorityID, i.operationID, i.actor, i.approvalRef, i.fenceRef, i.historyRef, 0, 1, registrationPending, i.planIdentity, i.descriptor.CanonicalBytes()}
	b, e := json.Marshal(v)
	if e != nil || len(b) > 32768 {
		return nil, errRegistrationInvalid
	}
	return b, nil
}

// registerFreshPending is intentionally unreachable from the application. Its
// test-only verifier represents missing trusted infrastructure, not authorization.
func registerFreshPending(ctx context.Context, r *Runner, v registrationVerifier, i registrationIntent) (registrationOutcome, error) {
	return runFreshRegistration(ctx, r, v, i, false)
}
func observeFreshPending(ctx context.Context, r *Runner, v registrationVerifier, i registrationIntent) (registrationOutcome, error) {
	return runFreshRegistration(ctx, r, v, i, true)
}
func registrationVerifierAbsent(v registrationVerifier) bool {
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
func registrationVerify(ctx context.Context, r *Runner, v registrationVerifier, i registrationIntent, stage registrationStage) (err error) {
	// Convert verifier panic inside the transaction body so Runner can retain
	// any simultaneous rollback/discard failure instead of unwinding past it.
	defer func() {
		if recover() != nil {
			err = errRegistrationUnknown
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, r.timeouts.Transaction)
	defer cancel()
	if e := v.verify(bounded, i, stage); e != nil || bounded.Err() != nil {
		return errRegistrationDenied
	}
	return nil
}
func closeRegistrationBarrier(r *Runner, v registrationVerifier, i registrationIntent) (err error) {
	defer func() {
		if recover() != nil {
			err = errRegistrationBarrier
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), r.timeouts.Cleanup)
	defer cancel()
	if e := v.close(ctx, i); e != nil || ctx.Err() != nil {
		return errRegistrationBarrier
	}
	return nil
}
func runFreshRegistration(ctx context.Context, r *Runner, v registrationVerifier, i registrationIntent, observe bool) (out registrationOutcome, err error) {
	canonical, e := i.canonical()
	if e != nil || ctx == nil || r == nil || registrationVerifierAbsent(v) {
		return "", errRegistrationInvalid
	}
	if ctx.Err() != nil {
		return "", errRegistrationUnavailable
	}
	// A panic cannot be interpreted as rollback or enablement. Runner still performs
	// its independent cleanup; close the missing durable authority conservatively.
	defer func() {
		if recover() != nil {
			out = ""
			err = errors.Join(errRegistrationUnknown, closeRegistrationBarrier(r, v, i))
		}
	}()
	before, locked, decision := registrationBefore, registrationLocked, registrationDecision
	if observe {
		before, locked, decision = registrationObserveBefore, registrationObserveLocked, registrationObserveDecision
	}
	if e := registrationVerify(ctx, r, v, i, before); e != nil {
		if errors.Is(e, errRegistrationUnknown) {
			return "", errors.Join(e, closeRegistrationBarrier(r, v, i))
		}
		return "", e
	}
	q := newRegistrationSQL(i.schema)
	d := i.descriptor.Value()
	candidate := registrationPending
	guarded := &Runner{pool: registrationGuardedPool{r.pool}, timeouts: r.timeouts}
	e = guarded.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		// Pin builtin/operator/type resolution before any business lock/query;
		// all relations remain explicitly schema-qualified as well.
		if _, e := tx.Exec(ctx, registrationSearchPath); e != nil {
			return e
		}
		var tenant string
		if e := tx.QueryRow(ctx, q.tenant, d.Tenant).Scan(&tenant); e != nil {
			return e
		}
		if tenant != d.Tenant {
			return errRegistrationIntegrity
		}
		var app string
		var active bool
		if e := tx.QueryRow(ctx, q.app, d.Tenant, d.App).Scan(&app, &active); e != nil {
			return e
		}
		if app != d.App || !active {
			return errRegistrationDenied
		}
		if e := registrationVerify(ctx, r, v, i, locked); e != nil {
			return e
		}
		var prior []byte
		e := tx.QueryRow(ctx, q.receipt, d.Tenant, i.authorityID, i.operationID).Scan(&prior)
		if e == nil {
			if !bytes.Equal(prior, canonical) {
				return errRegistrationConflict
			}
			if e = registrationReadGraph(ctx, tx, q, i, canonical); e != nil {
				return e
			}
			candidate = registrationObserved
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		} else {
			if observe {
				return errRegistrationIntegrity
			}
			var occupied bool
			if e = tx.QueryRow(ctx, q.occupied, d.Tenant, d.Destination).Scan(&occupied); e != nil {
				return e
			}
			if occupied {
				return errRegistrationExisting
			}
			// Five fixed inserts, no upsert or conflict-ignore path. 0009's synchronous
			// AFTER trigger owns the exact version insert. No scope/subscription/enablement.
			writes := []struct {
				sql  string
				args []any
			}{
				{q.insertCurrent, []any{d.Tenant, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, d.URL, d.KeyRef}},
				{q.insertPolicy, []any{d.Tenant, d.App, d.Destination, d.PolicyVersion, i.descriptor.PolicyIdentity(), d.URL, d.CIDRs, d.SignatureProfile}},
				{q.insertKey, []any{d.Tenant, d.App, d.Destination, d.KeyVersion, i.descriptor.KeyIdentity(), d.KeyRef, d.ProviderAuthorityID, d.ProviderRevision.Kind, d.ProviderRevision.ID}},
				{q.insertBinding, []any{d.Tenant, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, i.descriptor.PolicyIdentity(), i.descriptor.KeyIdentity(), i.descriptor.BindingIdentity(), i.descriptor.CanonicalBytes()}},
				{q.insertReceipt, []any{i.authorityID, d.Tenant, i.operationID, d.App, d.Destination, d.PolicyVersion, d.KeyVersion, i.descriptor.BindingIdentity(), canonical}},
			}
			for _, w := range writes {
				tag, e := tx.Exec(ctx, w.sql, w.args...)
				if e != nil {
					return e
				}
				if tag.RowsAffected() != 1 {
					return errRegistrationIntegrity
				}
			}
			if e = registrationReadGraph(ctx, tx, q, i, canonical); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
			return e
		}
		if e := registrationVerify(ctx, r, v, i, decision); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		if errors.Is(e, ErrCommitOutcomeUnknown) || errors.Is(e, ErrCleanupUnconfirmed) || errors.Is(e, errRegistrationDriverPanic) || errors.Is(e, errRegistrationUnknown) {
			closed := closeRegistrationBarrier(r, v, i)
			if errors.Is(e, ErrCleanupUnconfirmed) {
				return "", errors.Join(errRegistrationUnknown, ErrCleanupUnconfirmed, closed)
			}
			return "", errors.Join(errRegistrationUnknown, closed)
		}
		for _, known := range []error{errRegistrationDenied, errRegistrationConflict, errRegistrationExisting, errRegistrationIntegrity} {
			if errors.Is(e, known) {
				return "", known
			}
		}
		return "", errRegistrationUnavailable
	}
	// Observation and clean duplicate recognition never clear a durable barrier.
	return candidate, nil
}

type registrationSQL struct{ tenant, app, receipt, occupied, current, version, policy, key, binding, fullReceipt, obligations, insertCurrent, insertPolicy, insertKey, insertBinding, insertReceipt string }

// Four fixed probes, never an unbounded JSON array expansion. An unclassifiable
// tenant snapshot is history uncertainty, not evidence that this pair is fresh.
func registrationMalformedSnapshotSQL() string {
	count := "(CASE WHEN pg_catalog.jsonb_typeof(subscription_snapshot)='array' THEN pg_catalog.jsonb_array_length(subscription_snapshot) ELSE 0 END)"
	bad := []string{"pg_catalog.jsonb_typeof(subscription_snapshot) IS DISTINCT FROM 'array'", count + ">4", "pg_catalog.octet_length(subscription_snapshot::text)>16384"}
	for n := 0; n < 4; n++ {
		index := strconv.Itoa(n)
		x := "(subscription_snapshot->" + index + ")"
		bad = append(bad, "("+count+">"+index+" AND (pg_catalog.jsonb_typeof("+x+") IS DISTINCT FROM 'object' OR pg_catalog.jsonb_typeof("+x+"->'destinationId') IS DISTINCT FROM 'string' OR ("+x+"->>'destinationId') COLLATE pg_catalog.\"C\" !~ '^[A-Za-z0-9_-]{1,128}$'))")
	}
	return "(" + strings.Join(bad, " OR ") + ")"
}
func newRegistrationSQL(schema string) registrationSQL {
	table := func(n string) string { return pgx.Identifier{schema, n}.Sanitize() }
	current, version := table("callback_destinations"), table("callback_destination_versions")
	policy, key, binding, receipt := table("callback_registration_policies"), table("callback_registration_keys"), table("callback_registration_bindings"), table("callback_registration_receipts")
	obligations := []string{
		"EXISTS(SELECT 1 FROM " + table("scope_limits") + " WHERE tenant=$1 AND (destination_id=$2 OR (scope_kind='destination' AND scope_id=$2)))",
		"EXISTS(SELECT 1 FROM " + table("scope_slots") + " WHERE tenant=$1 AND scope_kind='destination' AND scope_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("callback_subscription_versions") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("admission_reservations") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("callback_outbox") + " WHERE tenant=$1 AND destination_id=$2)",
		"EXISTS(SELECT 1 FROM " + table("commands") + " WHERE tenant=$1 AND (subscription_snapshot @> pg_catalog.jsonb_build_array(pg_catalog.jsonb_build_object('destinationId',$2::text)) OR " + registrationMalformedSnapshotSQL() + "))",
	}
	occupied := append([]string{}, obligations...)
	for _, t := range []string{current, version, policy, key, binding, receipt} {
		col := "destination_id"
		if t == current {
			col = "id"
		}
		occupied = append(occupied, "EXISTS(SELECT 1 FROM "+t+" WHERE tenant=$1 AND "+col+"=$2)")
	}
	// Exact graph observation rejects additional versions/projections/receipts, not
	// merely a matching subset. This is deliberately fresh-only, never adoption.
	extra := append([]string{}, obligations...)
	for _, t := range []string{version, policy, key, binding, receipt} {
		extra = append(extra, "(SELECT pg_catalog.count(*) FROM "+t+" WHERE tenant=$1 AND destination_id=$2)<>1")
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
func registrationReadGraph(ctx context.Context, tx pgx.Tx, q registrationSQL, i registrationIntent, canonical []byte) error {
	d := i.descriptor.Value()
	scope := []any{d.Tenant, d.App, d.Destination}
	tuple := append(append([]any{}, scope...), d.PolicyVersion, d.KeyVersion)
	var app, url, keyRef, state string
	var pv, kv int64
	var enabled bool
	if e := tx.QueryRow(ctx, q.current, d.Tenant, d.Destination).Scan(&app, &url, &pv, &keyRef, &kv, &state, &enabled); e != nil {
		return e
	}
	if app != d.App || url != d.URL || pv != d.PolicyVersion || keyRef != d.KeyRef || kv != d.KeyVersion || state != "pending_review" || enabled {
		return errRegistrationIntegrity
	}
	var live bool
	if e := tx.QueryRow(ctx, q.version, tuple...).Scan(&url, &keyRef, &live); e != nil {
		return e
	}
	if url != d.URL || keyRef != d.KeyRef || !live {
		return errRegistrationIntegrity
	}
	var policyID, keyID, bindingID, profile, provider, kind, revision string
	var cidrs []string
	if e := tx.QueryRow(ctx, q.policy, append(append([]any{}, scope...), d.PolicyVersion)...).Scan(&policyID, &url, &cidrs, &profile); e != nil {
		return e
	}
	if policyID != i.descriptor.PolicyIdentity() || url != d.URL || profile != d.SignatureProfile || len(cidrs) != len(d.CIDRs) {
		return errRegistrationIntegrity
	}
	for n := range cidrs {
		if cidrs[n] != d.CIDRs[n] {
			return errRegistrationIntegrity
		}
	}
	if e := tx.QueryRow(ctx, q.key, append(append([]any{}, scope...), d.KeyVersion)...).Scan(&keyID, &keyRef, &provider, &kind, &revision); e != nil {
		return e
	}
	if keyID != i.descriptor.KeyIdentity() || keyRef != d.KeyRef || provider != d.ProviderAuthorityID || kind != d.ProviderRevision.Kind || revision != d.ProviderRevision.ID {
		return errRegistrationIntegrity
	}
	var descriptor, receipt []byte
	if e := tx.QueryRow(ctx, q.binding, tuple...).Scan(&policyID, &keyID, &bindingID, &descriptor); e != nil {
		return e
	}
	if policyID != i.descriptor.PolicyIdentity() || keyID != i.descriptor.KeyIdentity() || bindingID != i.descriptor.BindingIdentity() || !bytes.Equal(descriptor, i.descriptor.CanonicalBytes()) {
		return errRegistrationIntegrity
	}
	var dest string
	if e := tx.QueryRow(ctx, q.fullReceipt, d.Tenant, i.authorityID, i.operationID).Scan(&app, &dest, &pv, &kv, &bindingID, &receipt); e != nil {
		return e
	}
	if app != d.App || dest != d.Destination || pv != d.PolicyVersion || kv != d.KeyVersion || bindingID != i.descriptor.BindingIdentity() || !bytes.Equal(receipt, canonical) {
		return errRegistrationIntegrity
	}
	var extra bool
	if e := tx.QueryRow(ctx, q.obligations, d.Tenant, d.Destination).Scan(&extra); e != nil {
		return e
	}
	if extra {
		return errRegistrationIntegrity
	}
	return nil
}

// Guard only this candidate's driver calls; the shared Runner is unchanged.
// Panics in the wrapped transaction methods taint the transaction. Converting
// them to errors lets Runner preserve Commit uncertainty and perform its ordinary
// bounded discard. Any tainted transaction makes rollback unconfirmed even if
// the underlying driver returns nil. These are fault-containment assumptions,
// not evidence of actual PostgreSQL rollback or connection health. Acquire,
// Release and Discard still depend on their existing interface contracts; an
// outer panic there closes the barrier but cannot establish healthy disposal.
var errRegistrationDriverPanic = errors.New("REGISTRATION_DRIVER_UNCONFIRMED")

type registrationGuardedPool struct{ Pool }

func (p registrationGuardedPool) Acquire(ctx context.Context) (Connection, error) {
	c, e := p.Pool.Acquire(ctx)
	if e != nil {
		return c, e
	}
	return registrationGuardedConnection{c}, nil
}

type registrationGuardedConnection struct{ Connection }

func (c registrationGuardedConnection) BeginTx(ctx context.Context, o pgx.TxOptions) (tx pgx.Tx, err error) {
	defer func() {
		if recover() != nil {
			tx = nil
			err = errRegistrationDriverPanic
		}
	}()
	t, e := c.Connection.BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	if t == nil {
		return nil, errRegistrationDriverPanic
	}
	return &registrationGuardedTx{Tx: t}, nil
}

type registrationGuardedTx struct {
	pgx.Tx
	tainted bool
}

func (t *registrationGuardedTx) Commit(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			t.tainted = true
			err = errRegistrationDriverPanic
		}
	}()
	return t.Tx.Commit(ctx)
}
func (t *registrationGuardedTx) Rollback(ctx context.Context) (err error) {
	defer func() {
		if recover() != nil {
			t.tainted = true
			err = errRegistrationDriverPanic
		}
		if t.tainted {
			err = errors.Join(ErrCleanupUnconfirmed, errRegistrationDriverPanic)
		}
	}()
	return t.Tx.Rollback(ctx)
}
func (t *registrationGuardedTx) Exec(ctx context.Context, q string, args ...any) (tag pgconn.CommandTag, err error) {
	defer func() {
		if recover() != nil {
			t.tainted = true
			tag = pgconn.CommandTag{}
			err = errRegistrationDriverPanic
		}
	}()
	return t.Tx.Exec(ctx, q, args...)
}
func (t *registrationGuardedTx) QueryRow(ctx context.Context, q string, args ...any) (row pgx.Row) {
	defer func() {
		if recover() != nil {
			t.tainted = true
			row = registrationGuardedRow{tx: t, failed: true}
		}
	}()
	return registrationGuardedRow{tx: t, row: t.Tx.QueryRow(ctx, q, args...)}
}

type registrationGuardedRow struct {
	tx     *registrationGuardedTx
	row    pgx.Row
	failed bool
}

func (r registrationGuardedRow) Scan(dest ...any) (err error) {
	defer func() {
		if recover() != nil {
			r.tx.tainted = true
			err = errRegistrationDriverPanic
		}
	}()
	if r.failed {
		return errRegistrationDriverPanic
	}
	return r.row.Scan(dest...)
}
