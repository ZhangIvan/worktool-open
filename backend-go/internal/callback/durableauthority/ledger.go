package durableauthority

import (
	"context"
	"errors"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/store/pg"
	"github.com/jackc/pgx/v5"
)

// Adapter borrows a physical pool. Construction performs no I/O and transfers no
// pool ownership. The caller must join every use before closing that pool.
// There is no production mutation caller, issuer or startup integration.
type Adapter struct {
	pool     pg.Pool
	timeouts pg.Timeouts
}

func New(pool pg.Pool, t pg.Timeouts) (*Adapter, error) {
	if nilDriver(pool) || t.Validate() != nil {
		return nil, ErrInvalid
	}
	return &Adapter{pool, t}, nil
}
func (Adapter) String() string   { return "CallbackAuthorityAdapter(unwired,redacted)" }
func (Adapter) GoString() string { return "CallbackAuthorityAdapter(unwired,redacted)" }

// Evidence is inert historical evidence only, never a grant, retry permission or
// proof of receiver effects. Empty is the only result allowed after any failure.
type Evidence struct {
	Classification      string
	operation, identity string
}

func (Evidence) String() string   { return "CallbackAuthorityEvidence(no-authority,redacted)" }
func (Evidence) GoString() string { return "CallbackAuthorityEvidence(no-authority,redacted)" }

type event struct{ operation, kind, code, reference string }

func (e event) valid() bool {
	if !identifier(e.operation) || !identifier(e.reference) {
		return false
	}
	switch e.kind {
	case "incident":
		return e.code == "UNCERTAINTY" || e.code == "CLEANUP_UNCONFIRMED" || e.code == "START_INTERRUPTED"
	case "joined":
		return e.code == "JOINED"
	case "reconciliation_fact":
		return e.code == "EVIDENCE_RECORDED"
	}
	return false
}

const searchPathSQL = `SET LOCAL search_path TO pg_catalog, wt_synthetic_callback_authority, pg_temp`
const registrySQL = `SELECT CASE WHEN octet_length(scope_id) BETWEEN 1 AND 128 THEN scope_id ELSE NULL END,CASE WHEN octet_length(allocation_id) BETWEEN 1 AND 128 THEN allocation_id ELSE NULL END,CASE WHEN octet_length(instance_id) BETWEEN 1 AND 128 THEN instance_id ELSE NULL END,CASE WHEN octet_length(boot_id) BETWEEN 1 AND 128 THEN boot_id ELSE NULL END,CASE WHEN octet_length(epoch_id) BETWEEN 1 AND 128 THEN epoch_id ELSE NULL END,CASE WHEN octet_length(provenance_ref) BETWEEN 1 AND 128 THEN provenance_ref ELSE NULL END,registry_revision,CASE WHEN octet_length(plan_identity) BETWEEN 81 AND 81 THEN plan_identity ELSE NULL END,CASE WHEN octet_length(binding_identity) BETWEEN 64 AND 64 THEN binding_identity ELSE NULL END,CASE WHEN octet_length(plan_raw) BETWEEN 1 AND 262144 THEN plan_raw ELSE NULL END,CASE WHEN octet_length(descriptors_raw) BETWEEN 1 AND 524288 THEN descriptors_raw ELSE NULL END FROM wt_synthetic_callback_authority.registry WHERE scope_id=$1 FOR SHARE`
const ledgerSQL = `SELECT CASE WHEN octet_length(l.scope_id) BETWEEN 1 AND 128 THEN l.scope_id ELSE NULL END,CASE WHEN octet_length(l.allocation_id) BETWEEN 1 AND 128 THEN l.allocation_id ELSE NULL END,CASE WHEN octet_length(l.instance_id) BETWEEN 1 AND 128 THEN l.instance_id ELSE NULL END,CASE WHEN octet_length(l.boot_id) BETWEEN 1 AND 128 THEN l.boot_id ELSE NULL END,CASE WHEN octet_length(l.epoch_id) BETWEEN 1 AND 128 THEN l.epoch_id ELSE NULL END,l.registry_revision,CASE WHEN octet_length(l.binding_identity) BETWEEN 64 AND 64 THEN l.binding_identity ELSE NULL END,CASE WHEN octet_length(l.state) BETWEEN 6 AND 7 THEN l.state ELSE NULL END,CASE WHEN octet_length(l.halt_code) BETWEEN 0 AND 19 THEN l.halt_code ELSE NULL END,EXISTS(SELECT 1 FROM wt_synthetic_callback_authority.events e WHERE e.scope_id=l.scope_id AND e.kind IN ('incident','joined')),EXISTS(SELECT 1 FROM wt_synthetic_callback_authority.events e WHERE e.scope_id=l.scope_id AND e.kind IN ('incident','joined') AND e.code=l.halt_code) FROM wt_synthetic_callback_authority.ledger l WHERE l.scope_id=$1 FOR UPDATE OF l`
const insertIntentSQL = `INSERT INTO wt_synthetic_callback_authority.ledger(scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity,state,halt_code) VALUES($1,$2,$3,$4,$5,$6,$7,'running','')`
const haltSQL = `UPDATE wt_synthetic_callback_authority.ledger SET state='halted',halt_code=$8 WHERE scope_id=$1 AND allocation_id=$2 AND instance_id=$3 AND boot_id=$4 AND epoch_id=$5 AND registry_revision=$6 AND binding_identity=$7 AND state='running' AND halt_code=''`
const eventSQL = `SELECT CASE WHEN octet_length(scope_id) BETWEEN 1 AND 128 THEN scope_id ELSE NULL END,CASE WHEN octet_length(allocation_id) BETWEEN 1 AND 128 THEN allocation_id ELSE NULL END,CASE WHEN octet_length(instance_id) BETWEEN 1 AND 128 THEN instance_id ELSE NULL END,CASE WHEN octet_length(boot_id) BETWEEN 1 AND 128 THEN boot_id ELSE NULL END,CASE WHEN octet_length(epoch_id) BETWEEN 1 AND 128 THEN epoch_id ELSE NULL END,registry_revision,CASE WHEN octet_length(binding_identity) BETWEEN 64 AND 64 THEN binding_identity ELSE NULL END,CASE WHEN octet_length(kind) BETWEEN 6 AND 19 THEN kind ELSE NULL END,CASE WHEN octet_length(code) BETWEEN 6 AND 19 THEN code ELSE NULL END,CASE WHEN octet_length(evidence_ref) BETWEEN 1 AND 128 THEN evidence_ref ELSE NULL END FROM wt_synthetic_callback_authority.events WHERE operation_id=$1`
const insertEventSQL = `INSERT INTO wt_synthetic_callback_authority.events(operation_id,scope_id,allocation_id,instance_id,boot_id,epoch_id,registry_revision,binding_identity,kind,code,evidence_ref) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
const anyEventsSQL = `SELECT EXISTS(SELECT 1 FROM wt_synthetic_callback_authority.events WHERE scope_id=$1)`
const incidentsSQL = `SELECT EXISTS(SELECT 1 FROM wt_synthetic_callback_authority.events WHERE scope_id=$1 AND kind='incident')`
const constraintsSQL = `SET CONSTRAINTS ALL IMMEDIATE`

type ledgerRecord struct {
	identity            Identity
	digest, state, code string
}

func bindingArgs(b Binding) []any {
	i := b.identity
	return []any{i.Scope, i.Allocation, i.Instance, i.Boot, i.Epoch, i.Revision, b.digest}
}
func readRegistry(ctx context.Context, tx pgx.Tx, b Binding) error {
	var i Identity
	var planID, digest string
	var raw, ds []byte
	err := tx.QueryRow(ctx, registrySQL, b.identity.Scope).Scan(&i.Scope, &i.Allocation, &i.Instance, &i.Boot, &i.Epoch, &i.ProvenanceRef, &i.Revision, &planID, &digest, &raw, &ds)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMissing
	}
	if err != nil {
		return err
	}
	return b.validateRegistry(i, planID, digest, raw, ds)
}
func readLedger(ctx context.Context, tx pgx.Tx, b Binding) (ledgerRecord, error) {
	var r ledgerRecord
	var terminal, matching bool
	err := tx.QueryRow(ctx, ledgerSQL, b.identity.Scope).Scan(&r.identity.Scope, &r.identity.Allocation, &r.identity.Instance, &r.identity.Boot, &r.identity.Epoch, &r.identity.Revision, &r.digest, &r.state, &r.code, &terminal, &matching)
	if errors.Is(err, pgx.ErrNoRows) {
		var orphan bool
		if e := tx.QueryRow(ctx, anyEventsSQL, b.identity.Scope).Scan(&orphan); e != nil {
			return ledgerRecord{}, e
		}
		if orphan {
			return ledgerRecord{}, ErrInvalid
		}
	}
	if err != nil {
		return ledgerRecord{}, err
	}
	i := b.identity
	r.identity.ProvenanceRef = i.ProvenanceRef
	if r.identity != i || r.digest != b.digest || !((r.state == "running" && r.code == "") || (r.state == "halted" && haltCode(r.code))) {
		return ledgerRecord{}, ErrInvalid
	}
	if (r.state == "running" && terminal) || (r.state == "halted" && (!terminal || !matching)) {
		return ledgerRecord{}, ErrInvalid
	}
	return r, nil
}
func haltCode(c string) bool {
	return c == "JOINED" || c == "UNCERTAINTY" || c == "CLEANUP_UNCONFIRMED" || c == "START_INTERRUPTED"
}
func execOne(ctx context.Context, tx pgx.Tx, sql string, args ...any) error {
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}
func constraints(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, constraintsSQL)
	return err
}
func alive(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	d, ok := ctx.Deadline()
	return !ok || time.Now().Before(d)
}
func redact(err error) error {
	if err == nil {
		return nil
	}
	result := []error{ErrUnavailable}
	for _, code := range []error{ErrInvalid, ErrConflict, ErrMissing, pg.ErrCommitOutcomeUnknown, pg.ErrCleanupUnconfirmed} {
		if errors.Is(err, code) {
			result = append(result, code)
		}
	}
	return errors.Join(result...)
}
func (a *Adapter) run(ctx context.Context, b Binding, fn func(context.Context, pgx.Tx) error) error {
	if a == nil || nilDriver(a.pool) || !b.valid() || !alive(ctx) {
		return ErrInvalid
	}
	err := runGuarded(ctx, a.pool, a.timeouts, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		if !alive(ctx) {
			return ErrUnavailable
		}
		if _, err := tx.Exec(ctx, searchPathSQL); err != nil {
			return err
		}
		if err := readRegistry(ctx, tx, b); err != nil {
			return err
		}
		if err := fn(ctx, tx); err != nil {
			return err
		}
		if !alive(ctx) {
			return ErrUnavailable
		}
		return nil
	})
	if !alive(ctx) {
		err = errors.Join(err, ErrUnavailable)
	}
	return redact(err)
}

// recordIntent is deliberately package-private and unwired. Confirmed running
// intent is a prerequisite of future reviewed admission, not admission itself.
// Missing registry rejects. Occupancy rejects even an exact duplicate.
func (a *Adapter) recordIntent(ctx context.Context, b Binding) (Evidence, error) {
	err := a.run(ctx, b, func(ctx context.Context, tx pgx.Tx) error {
		_, err := readLedger(ctx, tx, b)
		if err == nil {
			return ErrConflict
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err = execOne(ctx, tx, insertIntentSQL, bindingArgs(b)...); err != nil {
			return err
		}
		if err = constraints(ctx, tx); err != nil {
			return err
		}
		r, err := readLedger(ctx, tx, b)
		if err != nil {
			return err
		}
		if r.state != "running" || r.code != "" {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{Classification: "INTENT_RECORDED_NO_AUTHORITY", identity: b.digest}, nil
}

func readEvent(ctx context.Context, tx pgx.Tx, b Binding, e event) error {
	var i Identity
	var digest, kind, code, ref string
	err := tx.QueryRow(ctx, eventSQL, e.operation).Scan(&i.Scope, &i.Allocation, &i.Instance, &i.Boot, &i.Epoch, &i.Revision, &digest, &kind, &code, &ref)
	if err != nil {
		return err
	}
	i.ProvenanceRef = b.identity.ProvenanceRef
	if i != b.identity || digest != b.digest || kind != e.kind || code != e.code || ref != e.reference {
		return ErrConflict
	}
	return nil
}

// recordEvent appends declarations, not authenticated operator decisions. It
// preserves later incidents even after JOINED, and never removes occupancy.
// Global operation uniqueness rejects cross-scope reuse. No automatic retries.
func (a *Adapter) recordEvent(ctx context.Context, b Binding, e event) (Evidence, error) {
	if !e.valid() {
		return Evidence{}, ErrInvalid
	}
	historical := false
	err := a.run(ctx, b, func(ctx context.Context, tx pgx.Tx) error {
		r, err := readLedger(ctx, tx, b)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMissing
		}
		if err != nil {
			return err
		}
		err = readEvent(ctx, tx, b, e)
		if err == nil {
			if e.kind != "reconciliation_fact" && r.state != "halted" {
				return ErrInvalid
			}
			historical = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		args := append([]any{e.operation}, bindingArgs(b)...)
		args = append(args, e.kind, e.code, e.reference)
		if err = execOne(ctx, tx, insertEventSQL, args...); err != nil {
			return err
		}
		if e.kind != "reconciliation_fact" && r.state == "running" {
			args = bindingArgs(b)
			args = append(args, e.code)
			if err = execOne(ctx, tx, haltSQL, args...); err != nil {
				return err
			}
		}
		if err = constraints(ctx, tx); err != nil {
			return err
		}
		if err = readEvent(ctx, tx, b, e); err != nil {
			return err
		}
		after, err := readLedger(ctx, tx, b)
		if err != nil {
			return err
		}
		wantState, wantCode := r.state, r.code
		if e.kind != "reconciliation_fact" && r.state == "running" {
			wantState, wantCode = "halted", e.code
		}
		if after.state != wantState || after.code != wantCode {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return Evidence{}, err
	}
	code := "FACT_RECORDED_NO_AUTHORITY"
	if historical {
		code = "HISTORICAL_FACT_NO_AUTHORITY"
	}
	return Evidence{Classification: code, operation: e.operation, identity: b.digest}, nil
}

// Observe never mutates business rows or returns a send/restart grant. FOR locks
// coordinate with writers; it is a locking observation, not SQL READ ONLY.
// Missing intent is unknown history, not a pristine allocation or safe rollback.
func (a *Adapter) Observe(ctx context.Context, b Binding) (Evidence, error) {
	var classification string
	err := a.run(ctx, b, func(ctx context.Context, tx pgx.Tx) error {
		r, err := readLedger(ctx, tx, b)
		if errors.Is(err, pgx.ErrNoRows) {
			classification = "INTENT_ABSENT_NO_AUTHORITY"
			return nil
		}
		if err != nil {
			return err
		}
		var incident bool
		if err = tx.QueryRow(ctx, incidentsSQL, b.identity.Scope).Scan(&incident); err != nil {
			return err
		}
		if incident && r.state != "halted" {
			return ErrInvalid
		}
		if incident {
			classification = "INCIDENT_BLOCKED"
		} else if r.state == "halted" {
			classification = "HALTED_BLOCKED"
		} else {
			classification = "RUNNING_INTENT_BLOCKED"
		}
		return nil
	})
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{Classification: classification, identity: b.digest}, nil
}
