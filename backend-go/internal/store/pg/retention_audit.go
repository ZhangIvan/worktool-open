package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CapacityAuditStatus describes only a bounded metadata projection, never release authority.
type CapacityAuditStatus string

const (
	CapacityUnavailable       CapacityAuditStatus = "UNAVAILABLE"
	CapacityIncomplete        CapacityAuditStatus = "INCOMPLETE"
	CapacityUnsupported       CapacityAuditStatus = "UNSUPPORTED"
	CapacityIntegrityConflict CapacityAuditStatus = "INTEGRITY_CONFLICT"
	CapacityDrift             CapacityAuditStatus = "DRIFT"
	CapacityConserved         CapacityAuditStatus = "CONSERVED"
	retentionAuditRowCap                          = 1024
	// Charge a conservative fixed envelope before scanning each bounded SQL row.
	// This bounds retained input metadata, not PostgreSQL scans or total Go heap.
	retentionAuditRowEnvelope    = 4096
	retentionAuditMetadataBudget = 8 << 20
)

// TrustedCapacityTenant is not a request DTO or an authentication mechanism.
// Only an authorized internal primary-read adapter may construct it.
type TrustedCapacityTenant struct{ id string }

func CapacityTenantFromAuthorizedRead(tenant string) (TrustedCapacityTenant, error) {
	if !boundedID(tenant) {
		return TrustedCapacityTenant{}, admissionError("INVALID_INPUT")
	}
	return TrustedCapacityTenant{tenant}, nil
}
func (TrustedCapacityTenant) String() string { return "TrustedCapacityTenant(redacted)" }

type CapacityScopeAudit struct {
	Kind string
	// ScopeToken is a stable hash, not the raw scope/tenant identifier.
	ScopeToken                                           string
	ExpectedJobs, ExpectedBytes, StoredJobs, StoredBytes int64
}
type CapacityAudit struct {
	Status                          CapacityAuditStatus
	ObservedAt                      time.Time
	Scopes                          []CapacityScopeAudit
	Issues, Holds                   []string // Fixed vocabulary only; no identifiers, SQL or body content.
	ReleasableJobs, ReleasableBytes int64    // Always zero. No delete/repair authority exists.
}

func (r CapacityAudit) String() string { return "CapacityAudit(" + string(r.Status) + ",redacted)" }

type RetentionAuditor struct {
	runner    *Runner
	localGate func(context.Context) error
}

// gate must be a local read/recovery interlock, with no I/O or side effects.
// It must pin the expected recovery generation and fail sticky on generation or
// clock-validity loss; a current-open boolean alone cannot detect close/reopen.
func NewRetentionAuditor(r *Runner, gate func(context.Context) error) (*RetentionAuditor, error) {
	if r == nil || gate == nil {
		return nil, admissionError("INVALID_INPUT")
	}
	return &RetentionAuditor{r, gate}, nil
}

type capacityAuditError struct{ cause error }

func (e *capacityAuditError) Error() string { return "CAPACITY_AUDIT_UNAVAILABLE" }
func (e *capacityAuditError) Unwrap() error { return e.cause }

var errCapacityAuditBound = errors.New("capacity audit input bound")

func capacityFailure(err error) (CapacityAudit, error) {
	return CapacityAudit{Status: CapacityUnavailable}, &capacityAuditError{err}
}
func (a *RetentionAuditor) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.localGate(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

// AuditRetainedCapacity does not retry, mutate, lock rows or authorize retention.
// A result is private until Runner cleanup and final local gate/context checks.
func (a *RetentionAuditor) AuditRetainedCapacity(ctx context.Context, tenant TrustedCapacityTenant) (CapacityAudit, error) {
	if a == nil || a.runner == nil || a.localGate == nil || ctx == nil || !boundedID(tenant.id) {
		return capacityFailure(admissionError("INVALID_INPUT"))
	}
	if err := a.check(ctx); err != nil {
		return capacityFailure(err)
	}
	var candidate CapacityAudit
	err := a.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		if err := a.check(ctx); err != nil {
			return err
		}
		for _, q := range []string{"SET LOCAL statement_timeout='1500ms'", "SET LOCAL lock_timeout='500ms'"} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		s, err := readCapacitySnapshot(ctx, tx, tenant.id, a.check)
		if err != nil {
			return err
		}
		candidate = reduceCapacitySnapshot(s)
		return a.check(ctx)
	})
	// Cleanup errors take precedence over an otherwise intentional bounded abort.
	if err != nil && (!errors.Is(err, errCapacityAuditBound) || errors.Is(err, ErrCleanupUnconfirmed) || errors.Is(err, ErrCommitOutcomeUnknown)) {
		return capacityFailure(err)
	}
	if finalErr := a.check(ctx); finalErr != nil {
		return capacityFailure(finalErr)
	}
	if err != nil {
		return CapacityAudit{Status: CapacityIncomplete, Issues: []string{"INPUT_BOUND"}}, nil
	}
	return candidate, nil
}

type auditMeta struct {
	tenant string
	valid  bool
}
type auditScope struct {
	auditMeta
	kind, id, destination                    string
	jobs, bytes, capacityJobs, capacityBytes int64
}
type auditBinding struct {
	app, subscription, destination          string
	version, policy, key, attempts, ordinal int64
}

func (b auditBinding) valid() bool {
	return boundedID(b.app) && boundedID(b.subscription) && boundedID(b.destination) && b.version > 0 && b.policy > 0 && b.key > 0 && b.attempts >= 1 && b.attempts <= 4 && b.ordinal == 0
}

type auditReservation struct {
	auditMeta
	id, command, kind, state, delivery, event string
	binding                                   auditBinding
	jobs, bytes                               int64
	retain                                    *time.Time
	released, reason, resultIdentity          bool
}
type auditCommand struct {
	auditMeta
	id, app, status, effect          string
	supported, result, snapshotValid bool
	destinationCount                 int64
	destinations                     [4]string
}
type auditDelivery struct {
	auditMeta
	id, reservation, command, event, state string
	binding                                auditBinding
	leaseValid, leased                     bool
	fence                                  int64
}
type auditEvent struct {
	auditMeta
	id, command, app string
	body, managed    bool
}
type auditAttempt struct {
	auditMeta
	delivery, outcome string
	fence, number     int64
	finished          bool
}
type auditSlot struct {
	auditMeta
	kind, id, delivery               string
	number, deliveryFence, slotFence int64
	ownershipValid, occupied         bool
}
type auditIntent struct {
	auditMeta
	id                   int64
	command, kind, state string
}
type capacitySnapshot struct {
	tenant       string
	observed     time.Time
	scopes       []auditScope
	reservations []auditReservation
	commands     []auditCommand
	deliveries   []auditDelivery
	events       []auditEvent
	attempts     []auditAttempt
	slots        []auditSlot
	intents      []auditIntent
}

// All expressions below are fixed trusted source, never caller-provided SQL.
// NULL optional identifiers become empty strings; the separate validity bit
// distinguishes NULL from a stored invalid empty/oversized identifier.
func auditID(column string) string {
	return "CASE WHEN octet_length(" + column + ") BETWEEN 1 AND 128 THEN " + column + " ELSE '' END"
}
func auditIDsValid(required, optional []string) string {
	p := []string{}
	for _, c := range required {
		p = append(p, "(octet_length("+c+") BETWEEN 1 AND 128)")
	}
	for _, c := range optional {
		p = append(p, "("+c+" IS NULL OR octet_length("+c+") BETWEEN 1 AND 128)")
	}
	return "COALESCE(" + strings.Join(p, " AND ") + ",false)"
}
func auditEnum(column string, values ...string) string {
	return "CASE WHEN " + column + " IN ('" + strings.Join(values, "','") + "') THEN " + column + " ELSE '' END"
}
func auditSelect(table string, fields ...string) string {
	return "SELECT " + strings.Join(fields, ",") + " FROM " + table + " WHERE tenant=$1 LIMIT 1025"
}
func auditBindingColumns(policy string) []string {
	return []string{auditID("app"), auditID("subscription_id"), auditID("destination_id"), "COALESCE(subscription_version,0)::bigint", "COALESCE(" + policy + ",0)::bigint", "COALESCE(key_version,0)::bigint", "COALESCE(max_attempts,0)::bigint", "event_ordinal::bigint"}
}

var capacityAuditQueries = func() [8]string {
	base := func(required, optional []string) []string {
		return []string{auditID("tenant"), auditIDsValid(append([]string{"tenant"}, required...), optional)}
	}
	q := [8]string{}
	q[0] = auditSelect("scope_limits", append(base([]string{"scope_id"}, []string{"destination_id"}), auditEnum("scope_kind", "tenant", "destination"), auditID("scope_id"), auditID("destination_id"), "charged_jobs", "charged_bytes", "capacity_jobs", "capacity_bytes")...)
	r := append(base([]string{"reservation_id", "command"}, []string{"app", "subscription_id", "destination_id", "delivery_id", "event_id"}), auditID("reservation_id"), auditID("command"), auditEnum("kind", "result", "callback"), auditEnum("state", "held", "materialized", "cancelled_retained", "purged"), auditID("delivery_id"), auditID("event_id"))
	r = append(r, auditBindingColumns("destination_policy_version")...)
	r = append(r, "reserved_jobs::bigint", "reserved_bytes", "retain_until", "released_at IS NOT NULL", "release_reason IS NOT NULL", "app IS NULL AND subscription_id IS NULL AND subscription_version IS NULL AND destination_id IS NULL AND destination_policy_version IS NULL AND key_version IS NULL AND max_attempts IS NULL AND delivery_id IS NULL AND event_id IS NULL")
	q[1] = auditSelect("admission_reservations", r...)
	c := append(base([]string{"id"}, []string{"app_id"}), auditID("id"), auditID("app_id"), auditEnum("status", "queued", "delivered", "accepted", "running", "succeeded", "partially_succeeded", "failed", "cancelled", "expired", "rejected", "execution_unknown"), auditEnum("effect", "not_started", "not_applied", "applied", "partially_applied", "unknown"), "actor_type='app' AND capability='message.send_text' AND admission_version='bounded-result-v1'", "result IS NOT NULL")
	count := "CASE WHEN jsonb_typeof(subscription_snapshot)='array' THEN CASE WHEN octet_length(subscription_snapshot::text)<=16384 THEN jsonb_array_length(subscription_snapshot)::bigint ELSE -1 END ELSE -1 END"
	valid := "(" + count + ") BETWEEN 0 AND 4"
	for _, i := range []string{"0", "1", "2", "3"} {
		x := "subscription_snapshot->" + i
		valid += " AND ((" + count + ")<=" + i + " OR (jsonb_typeof(" + x + ")='object' AND jsonb_typeof(" + x + "->'destinationId')='string' AND octet_length(" + x + "->>'destinationId') BETWEEN 1 AND 128))"
	}
	c = append(c, "COALESCE(("+valid+"),false)", count)
	for _, i := range []string{"0", "1", "2", "3"} {
		c = append(c, auditID("subscription_snapshot->"+i+"->>'destinationId'"))
	}
	q[2] = auditSelect("commands", c...)
	d := append(base([]string{"delivery_id", "reservation_id", "command", "event_id", "app", "subscription_id", "destination_id"}, nil), auditID("delivery_id"), auditID("reservation_id"), auditID("command"), auditID("event_id"), auditEnum("state", "ready", "leased", "delivered", "dead_letter", "cancelled"))
	d = append(d, auditBindingColumns("policy_version")...)
	d = append(d, "(lease_owner IS NULL)=(lease_until IS NULL) AND (lease_owner IS NULL)=(lease_recovery_epoch IS NULL) AND ((state='leased')=(lease_owner IS NOT NULL)) AND (state<>'leased' OR (fence>0 AND claimed_at IS NOT NULL AND hard_deadline_at IS NOT NULL)) AND reservation_kind='callback' AND reservation_state='materialized'", "lease_owner IS NOT NULL", "fence")
	q[3] = auditSelect("callback_outbox", d...)
	q[4] = auditSelect("developer_callbacks", append(base([]string{"event", "command", "app"}, nil), auditID("event"), auditID("command"), auditID("app"), "body IS NOT NULL", "state='managed_outbox'")...)
	q[5] = auditSelect("callback_attempts", append(base([]string{"delivery_id"}, nil), auditID("delivery_id"), auditEnum("outcome", "in_progress", "delivered", "retryable_failure", "permanent_failure", "lease_lost", "delivery_unknown"), "fence", "attempt_no::bigint", "finished_at IS NOT NULL")...)
	q[6] = auditSelect("scope_slots", append(base([]string{"scope_id"}, []string{"delivery_id"}), auditEnum("scope_kind", "tenant", "destination"), auditID("scope_id"), auditID("delivery_id"), "slot_no::bigint", "COALESCE(delivery_fence,0)", "slot_fence", "(owner_instance IS NULL AND delivery_id IS NULL AND delivery_fence IS NULL AND lease_until IS NULL AND recovery_epoch IS NULL) OR (owner_instance IS NOT NULL AND delivery_id IS NOT NULL AND delivery_fence IS NOT NULL AND lease_until IS NOT NULL AND recovery_epoch IS NOT NULL)", "delivery_id IS NOT NULL")...)
	q[7] = auditSelect("outbox", append(base([]string{"command"}, nil), "id", auditID("command"), auditEnum("kind", "command_ready", "command_state_changed", "stop_request", "result_ack"), auditEnum("state", "pending", "claimed", "settled"))...)
	return q
}()

func readCapacityRows[T any](ctx context.Context, tx pgx.Tx, query, tenant string, budget *int, scan func(pgx.Rows) (T, error), gate func(context.Context) error) ([]T, error) {
	rows, err := tx.Query(ctx, query, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		if len(out) == retentionAuditRowCap || *budget < retentionAuditRowEnvelope {
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			if err := gate(ctx); err != nil {
				return nil, err
			}
			return nil, errCapacityAuditBound
		}
		*budget -= retentionAuditRowEnvelope
		row, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = gate(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
func bindingTargets(b *auditBinding) []any {
	return []any{&b.app, &b.subscription, &b.destination, &b.version, &b.policy, &b.key, &b.attempts, &b.ordinal}
}
func readCapacitySnapshot(ctx context.Context, tx pgx.Tx, tenant string, gate func(context.Context) error) (capacitySnapshot, error) {
	s := capacitySnapshot{tenant: tenant}
	budget := retentionAuditMetadataBudget
	// This first SELECT establishes the RR snapshot. It is not a release clock.
	if err := tx.QueryRow(ctx, "SELECT transaction_timestamp()").Scan(&s.observed); err != nil {
		return s, err
	}
	if s.observed.IsZero() || s.observed.Before(time.Unix(0, 0)) || s.observed.After(time.UnixMilli(maxWireMillis)) {
		return s, errors.New("invalid audit observation clock")
	}
	var err error
	s.scopes, err = readCapacityRows(ctx, tx, capacityAuditQueries[0], tenant, &budget, func(rows pgx.Rows) (auditScope, error) {
		r := auditScope{}
		e := rows.Scan(&r.tenant, &r.valid, &r.kind, &r.id, &r.destination, &r.jobs, &r.bytes, &r.capacityJobs, &r.capacityBytes)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.reservations, err = readCapacityRows(ctx, tx, capacityAuditQueries[1], tenant, &budget, func(rows pgx.Rows) (auditReservation, error) {
		r := auditReservation{}
		args := []any{&r.tenant, &r.valid, &r.id, &r.command, &r.kind, &r.state, &r.delivery, &r.event}
		args = append(args, bindingTargets(&r.binding)...)
		args = append(args, &r.jobs, &r.bytes, &r.retain, &r.released, &r.reason, &r.resultIdentity)
		e := rows.Scan(args...)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.commands, err = readCapacityRows(ctx, tx, capacityAuditQueries[2], tenant, &budget, func(rows pgx.Rows) (auditCommand, error) {
		r := auditCommand{}
		e := rows.Scan(&r.tenant, &r.valid, &r.id, &r.app, &r.status, &r.effect, &r.supported, &r.result, &r.snapshotValid, &r.destinationCount, &r.destinations[0], &r.destinations[1], &r.destinations[2], &r.destinations[3])
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.deliveries, err = readCapacityRows(ctx, tx, capacityAuditQueries[3], tenant, &budget, func(rows pgx.Rows) (auditDelivery, error) {
		r := auditDelivery{}
		args := []any{&r.tenant, &r.valid, &r.id, &r.reservation, &r.command, &r.event, &r.state}
		args = append(args, bindingTargets(&r.binding)...)
		args = append(args, &r.leaseValid, &r.leased, &r.fence)
		e := rows.Scan(args...)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.events, err = readCapacityRows(ctx, tx, capacityAuditQueries[4], tenant, &budget, func(rows pgx.Rows) (auditEvent, error) {
		r := auditEvent{}
		e := rows.Scan(&r.tenant, &r.valid, &r.id, &r.command, &r.app, &r.body, &r.managed)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.attempts, err = readCapacityRows(ctx, tx, capacityAuditQueries[5], tenant, &budget, func(rows pgx.Rows) (auditAttempt, error) {
		r := auditAttempt{}
		e := rows.Scan(&r.tenant, &r.valid, &r.delivery, &r.outcome, &r.fence, &r.number, &r.finished)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.slots, err = readCapacityRows(ctx, tx, capacityAuditQueries[6], tenant, &budget, func(rows pgx.Rows) (auditSlot, error) {
		r := auditSlot{}
		e := rows.Scan(&r.tenant, &r.valid, &r.kind, &r.id, &r.delivery, &r.number, &r.deliveryFence, &r.slotFence, &r.ownershipValid, &r.occupied)
		return r, e
	}, gate)
	if err != nil {
		return s, err
	}
	s.intents, err = readCapacityRows(ctx, tx, capacityAuditQueries[7], tenant, &budget, func(rows pgx.Rows) (auditIntent, error) {
		r := auditIntent{}
		e := rows.Scan(&r.tenant, &r.valid, &r.id, &r.command, &r.kind, &r.state)
		return r, e
	}, gate)
	return s, err
}

type auditScopeKey struct{ kind, id string }
type auditAttemptKey struct {
	delivery string
	fence    int64
}
type auditSlotKey struct {
	scope  auditScopeKey
	number int64
}

func auditAdd(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}
func auditToken(kind, id string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + id))
	return hex.EncodeToString(h[:])
}
func sortedAuditCodes(c map[string]bool) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func auditTerminal(status string) bool {
	switch status {
	case "succeeded", "partially_succeeded", "failed", "cancelled", "expired", "rejected":
		return true
	}
	return false
}

// reduceCapacitySnapshot is pure. It is called only after all eight inputs pass
// their caps, and performs no I/O. It deliberately does not prove full graph integrity.
func reduceCapacitySnapshot(s capacitySnapshot) CapacityAudit {
	totalRows := 0
	for _, n := range []int{len(s.scopes), len(s.reservations), len(s.commands), len(s.deliveries), len(s.events), len(s.attempts), len(s.slots), len(s.intents)} {
		if n > retentionAuditRowCap {
			return CapacityAudit{Status: CapacityIncomplete, Issues: []string{"INPUT_BOUND"}}
		}
		totalRows += n
	}
	if totalRows > retentionAuditMetadataBudget/retentionAuditRowEnvelope {
		return CapacityAudit{Status: CapacityIncomplete, Issues: []string{"INPUT_BOUND"}}
	}
	for _, c := range s.commands {
		if !c.supported {
			return CapacityAudit{Status: CapacityUnsupported, Issues: []string{"UNSUPPORTED_POPULATION"}}
		}
	}
	for _, e := range s.events {
		if !e.managed {
			return CapacityAudit{Status: CapacityUnsupported, Issues: []string{"UNSUPPORTED_POPULATION"}}
		}
	}
	issues := map[string]bool{}
	holds := map[string]bool{"RETENTION_POLICY_NOT_IMPLEMENTED": true, "REPLAY_RETENTION_NOT_PROVEN": true}
	check := func(ok bool, code string) {
		if !ok {
			issues[code] = true
		}
	}
	meta := func(m auditMeta) { check(m.valid && m.tenant == s.tenant, "INVALID_OR_CROSS_TENANT_METADATA") }
	scopes := map[auditScopeKey]auditScope{}
	expected := map[auditScopeKey][2]int64{}
	reservations := map[string]auditReservation{}
	commands := map[string]auditCommand{}
	deliveries := map[string]auditDelivery{}
	events := map[string]auditEvent{}
	groups := map[string][]auditReservation{}
	eventRefs := map[string]int{}
	for _, v := range s.scopes {
		meta(v.auditMeta)
		k := auditScopeKey{v.kind, v.id}
		_, dup := scopes[k]
		check(!dup, "DUPLICATE_SCOPE")
		scopes[k] = v
		check(boundedID(v.id) && ((v.kind == "tenant" && v.id == s.tenant && v.destination == "") || (v.kind == "destination" && v.destination == v.id)), "INVALID_SCOPE_IDENTITY")
		check(v.jobs >= 0 && v.bytes >= 0 && v.capacityJobs >= v.jobs && v.capacityBytes >= v.bytes, "INVALID_COUNTER_RANGE")
	}
	tenantKey := auditScopeKey{"tenant", s.tenant}
	_, exists := scopes[tenantKey]
	check(exists, "MISSING_TENANT_SCOPE")
	for _, v := range s.commands {
		meta(v.auditMeta)
		_, dup := commands[v.id]
		check(!dup, "DUPLICATE_COMMAND")
		commands[v.id] = v
		groups[v.id] = nil
		check(boundedID(v.id) && boundedID(v.app) && v.status != "" && v.effect != "", "INVALID_COMMAND_METADATA")
		check(v.snapshotValid && v.destinationCount >= 0 && v.destinationCount <= 4, "INVALID_SUBSCRIPTION_SNAPSHOT")
		if !auditTerminal(v.status) || v.effect == "unknown" {
			holds["COMMAND_UNSETTLED_OR_UNKNOWN"] = true
		}
	}
	for _, v := range s.deliveries {
		meta(v.auditMeta)
		_, dup := deliveries[v.id]
		check(!dup, "DUPLICATE_DELIVERY")
		deliveries[v.id] = v
		eventRefs[v.event]++
		check(boundedID(v.id) && boundedID(v.reservation) && boundedID(v.command) && boundedID(v.event) && v.binding.valid() && v.state != "" && v.leaseValid && v.fence >= 0, "INVALID_DELIVERY_METADATA")
		if v.state == "ready" || v.state == "leased" || v.leased {
			holds["CALLBACK_PENDING_OR_LEASED"] = true
		}
	}
	for _, v := range s.events {
		meta(v.auditMeta)
		_, dup := events[v.id]
		check(!dup, "DUPLICATE_EVENT")
		events[v.id] = v
		c, ok := commands[v.command]
		check(ok && c.app == v.app && boundedID(v.id) && v.body, "INVALID_SHARED_EVENT")
		if eventRefs[v.id] > 1 {
			holds["SHARED_EVENT_COHORT"] = true
		}
		if eventRefs[v.id] == 0 && v.body {
			holds["UNREFERENCED_EVENT_BODY_RETAINED"] = true
		}
	}
	for _, v := range s.reservations {
		meta(v.auditMeta)
		_, dup := reservations[v.id]
		check(!dup, "DUPLICATE_RESERVATION")
		reservations[v.id] = v
		groups[v.command] = append(groups[v.command], v)
		c, ok := commands[v.command]
		check(ok && boundedID(v.id), "MISSING_OR_INVALID_COMMAND_IDENTITY")
		check(v.state == "held" || v.state == "materialized" || v.state == "cancelled_retained" || v.state == "purged", "INVALID_RESERVATION_STATE")
		check((v.state == "purged") == v.released && (v.state == "purged") == v.reason, "INVALID_RELEASE_MARKER")
		amount := int64(196608)
		keys := []auditScopeKey{tenantKey}
		switch v.kind {
		case "result":
			if v.state == "purged" && c.result {
				holds["RESULT_BODY_RETAINED_AFTER_PURGE"] = true
			}
			check(v.resultIdentity && v.binding == (auditBinding{}) && v.delivery == "" && v.event == "", "INVALID_RESULT_IDENTITY")
			if v.state == "materialized" {
				check(c.result, "MISSING_MATERIALIZED_RESULT")
			}
		case "callback":
			amount = 90112
			check(v.binding.valid() && v.binding.app == c.app, "INVALID_CALLBACK_IDENTITY")
			k := auditScopeKey{"destination", v.binding.destination}
			_, ok := scopes[k]
			check(ok, "MISSING_DESTINATION_SCOPE")
			keys = append(keys, k)
			if v.state == "materialized" {
				d, ok := deliveries[v.delivery]
				check(ok && d.reservation == v.id && d.command == v.command && d.event == v.event && d.binding == v.binding, "MISSING_OR_MISMATCHED_DELIVERY")
			} else {
				check(v.delivery == "" && v.event == "", "INVALID_RESERVATION_POINTER")
			}
		default:
			check(false, "INVALID_RESERVATION_KIND")
		}
		validAmount := v.jobs == 1 && v.bytes == amount
		check(validAmount, "INVALID_RESERVATION_AMOUNT")
		if v.state != "purged" {
			if v.retain == nil {
				holds["RETENTION_DEADLINE_UNSET"] = true
			} else if v.retain.After(s.observed) {
				holds["RETENTION_DEADLINE_NOT_REACHED"] = true
			}
			if v.state == "held" {
				holds["HELD_RESERVATION"] = true
			}
			if v.state == "cancelled_retained" {
				holds["CANCELLED_RETAINED"] = true
			}
			if validAmount {
				for _, k := range keys {
					x := expected[k]
					a, oka := auditAdd(x[0], v.jobs)
					b, okb := auditAdd(x[1], v.bytes)
					check(oka && okb, "AGGREGATE_OVERFLOW")
					expected[k] = [2]int64{a, b}
				}
			}
		}
	}
	for _, d := range s.deliveries {
		r, ok := reservations[d.reservation]
		check(ok && r.kind == "callback" && r.state == "materialized" && r.delivery == d.id && r.event == d.event && r.command == d.command && r.binding == d.binding, "ORPHAN_OR_REUSED_DELIVERY")
		e, ok := events[d.event]
		check(ok && e.command == d.command && e.app == d.binding.app, "MISSING_OR_MISMATCHED_SHARED_EVENT")
	}
	for cid, g := range groups {
		c, ok := commands[cid]
		check(ok, "MISSING_COMMAND")
		results := 0
		dest := map[string]bool{}
		purged, live := false, false
		for _, r := range g {
			if r.kind == "result" {
				results++
			} else if r.kind == "callback" {
				check(!dest[r.binding.destination], "DUPLICATE_COMMAND_DESTINATION")
				dest[r.binding.destination] = true
			}
			if r.state == "purged" {
				purged = true
			} else {
				live = true
			}
		}
		check(results == 1 && len(g) <= 5, "INVALID_COMMAND_RESERVATION_CARDINALITY")
		check(c.destinationCount == int64(len(dest)), "SNAPSHOT_DESTINATION_MISMATCH")
		seen := map[string]bool{}
		for i, d := range c.destinations {
			if int64(i) < c.destinationCount {
				check(boundedID(d) && dest[d] && !seen[d], "SNAPSHOT_DESTINATION_MISMATCH")
				seen[d] = true
			} else {
				check(d == "", "INVALID_SUBSCRIPTION_SNAPSHOT")
			}
		}
		if purged && live {
			holds["MIXED_PURGED_REPLAY_GRAPH"] = true
		}
	}
	attempts := map[auditAttemptKey]bool{}
	attemptNumbers := map[auditAttemptKey]bool{}
	for _, v := range s.attempts {
		meta(v.auditMeta)
		k := auditAttemptKey{v.delivery, v.fence}
		n := auditAttemptKey{v.delivery, v.number}
		check(!attempts[k] && !attemptNumbers[n], "DUPLICATE_ATTEMPT")
		attempts[k] = true
		attemptNumbers[n] = true
		d, ok := deliveries[v.delivery]
		check(ok && v.fence > 0 && v.fence <= d.fence && v.number >= 1 && v.number <= d.binding.attempts, "ORPHAN_OR_INVALID_ATTEMPT")
		check(v.outcome != "" && ((v.outcome == "in_progress") != v.finished), "INVALID_ATTEMPT_OUTCOME")
		if !v.finished || v.outcome == "in_progress" || v.outcome == "delivery_unknown" || v.outcome == "lease_lost" {
			holds["CALLBACK_ATTEMPT_UNRESOLVED"] = true
		}
	}
	slots := map[auditSlotKey]bool{}
	for _, v := range s.slots {
		meta(v.auditMeta)
		scope := auditScopeKey{v.kind, v.id}
		k := auditSlotKey{scope, v.number}
		check(!slots[k], "DUPLICATE_SLOT")
		slots[k] = true
		_, ok := scopes[scope]
		check(ok && v.number >= 0 && v.slotFence >= 0 && v.ownershipValid, "INVALID_SLOT_IDENTITY")
		if v.occupied {
			d, ok := deliveries[v.delivery]
			check(ok && v.deliveryFence > 0 && v.deliveryFence <= d.fence && (v.kind == "tenant" || v.id == d.binding.destination), "ORPHAN_OR_INVALID_SLOT")
			holds["CALLBACK_SLOT_REFERENCE"] = true
		} else {
			check(v.delivery == "" && v.deliveryFence == 0, "INVALID_SLOT_IDENTITY")
		}
	}
	intents := map[int64]bool{}
	intentKinds := map[[2]string]bool{}
	for _, v := range s.intents {
		meta(v.auditMeta)
		k := [2]string{v.command, v.kind}
		check(!intents[v.id] && !intentKinds[k], "DUPLICATE_INTENT")
		intents[v.id] = true
		intentKinds[k] = true
		_, ok := commands[v.command]
		check(ok && v.id > 0 && v.kind != "" && v.state != "", "ORPHAN_OR_INVALID_INTENT")
		holds["OUTBOX_SETTLEMENT_NOT_PROVEN"] = true
	}
	result := CapacityAudit{Status: CapacityConserved, ObservedAt: s.observed, Issues: sortedAuditCodes(issues), Holds: sortedAuditCodes(holds)}
	if len(issues) > 0 {
		result.Status = CapacityIntegrityConflict
		return result
	} // No usable totals on invalid graphs.
	for k, v := range scopes {
		x := expected[k]
		result.Scopes = append(result.Scopes, CapacityScopeAudit{k.kind, auditToken(k.kind, k.id), x[0], x[1], v.jobs, v.bytes})
		if x[0] != v.jobs || x[1] != v.bytes {
			result.Status = CapacityDrift
		}
	}
	sort.Slice(result.Scopes, func(i, j int) bool {
		a, b := result.Scopes[i], result.Scopes[j]
		return a.Kind < b.Kind || (a.Kind == b.Kind && a.ScopeToken < b.ScopeToken)
	})
	if result.Status == CapacityDrift {
		result.Issues = []string{"COUNTER_DRIFT"}
	}
	return result
}
