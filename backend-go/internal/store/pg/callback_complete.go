package pg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// CallbackCompleter implements only completion, not sender.Store. It does not
// claim, prepare, materialize, sign, resolve, send, spawn or register a worker.
// Runtime/gate are trusted startup inputs, never derived from request JSON.
type CallbackCompleter struct {
	runner  *Runner
	runtime RuntimeAuthority
	gate    func(context.Context) error
}

func NewCallbackCompleter(r *Runner, runtime RuntimeAuthority, gate func(context.Context) error) (*CallbackCompleter, error) {
	if r == nil || !runtime.trusted || gate == nil {
		return nil, ErrCallbackCompletionInvalid
	}
	return &CallbackCompleter{r, runtime, gate}, nil
}

const callbackLockTenant = `SELECT scope_id FROM scope_limits WHERE scope_kind='tenant' AND tenant=$1 AND scope_id=$1 FOR UPDATE`
const callbackDestinationHint = `SELECT destination_id FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2`
const callbackLockDestination = `SELECT scope_id FROM scope_limits WHERE scope_kind='destination' AND tenant=$1 AND scope_id=$2 AND destination_id=$2 FOR UPDATE`
const callbackReadDelivery = `SELECT o.destination_id,o.state,o.fence,o.attempt_count,o.max_attempts,o.lease_owner,o.lease_recovery_epoch,o.lease_until,o.claimed_at,o.hard_deadline_at,o.expires_at,o.delivered_at,o.available_at,o.body_bytes,o.payload_digest,o.last_error_code,
 EXISTS(SELECT 1 FROM admission_reservations r WHERE r.tenant=o.tenant AND r.reservation_id=o.reservation_id AND r.kind='callback' AND r.state='materialized' AND r.delivery_id=o.delivery_id AND r.event_id=o.event_id AND r.app=o.app AND r.command=o.command AND r.destination_id=o.destination_id AND r.event_ordinal=o.event_ordinal AND r.subscription_id=o.subscription_id AND r.subscription_version=o.subscription_version AND r.destination_policy_version=o.policy_version AND r.key_version=o.key_version AND r.max_attempts=o.max_attempts),
 (SELECT convert_to(e.body,'UTF8') FROM developer_callbacks e WHERE e.tenant=o.tenant AND e.event=o.event_id AND e.app=o.app AND e.command=o.command)
 FROM callback_outbox o WHERE o.tenant=$1 AND o.delivery_id=$2 FOR UPDATE OF o`
const callbackReadAttempt = `SELECT attempt_no,owner_instance,recovery_epoch,started_at,send_prepared_at,finished_at,outcome,status_code,error_code FROM callback_attempts WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 FOR UPDATE`
const callbackReadSlots = `SELECT scope_kind,scope_id,slot_no,owner_instance,delivery_id,delivery_fence,recovery_epoch,slot_fence,lease_until FROM scope_slots WHERE tenant=$1 AND delivery_id=$2 ORDER BY CASE scope_kind WHEN 'tenant' THEN 0 ELSE 1 END,scope_id COLLATE "C",slot_no LIMIT 3 FOR UPDATE`
const callbackOldSlotExists = `SELECT EXISTS(SELECT 1 FROM scope_slots WHERE tenant=$1 AND delivery_id=$2 AND owner_instance=$3 AND recovery_epoch=$4 AND delivery_fence=$5)`
const callbackFinalTime = `SELECT clock_timestamp()`
const callbackUpdateAttempt = `UPDATE callback_attempts SET finished_at=$6,outcome=$7,status_code=$8,error_code=$9 WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 AND owner_instance=$4 AND recovery_epoch=$5 AND attempt_no=$10 AND outcome='in_progress' AND finished_at IS NULL`
const callbackUpdateDelivery = `UPDATE callback_outbox SET state=$6,available_at=CASE WHEN $6='ready' THEN $7::timestamptz ELSE available_at END,delivered_at=CASE WHEN $6='delivered' THEN $8::timestamptz ELSE NULL END,lease_owner=NULL,lease_recovery_epoch=NULL,lease_until=NULL,last_error_code=$9 WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 AND lease_owner=$4 AND lease_recovery_epoch=$5 AND attempt_count=$10 AND max_attempts=$11 AND state='leased'`
const callbackReleaseSlot = `UPDATE scope_slots SET owner_instance=NULL,delivery_id=NULL,delivery_fence=NULL,recovery_epoch=NULL,lease_until=NULL WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3 AND slot_no=$4 AND owner_instance=$5 AND delivery_id=$6 AND delivery_fence=$7 AND recovery_epoch=$8 AND slot_fence=$9 AND lease_until=$10`
const callbackInsertAudit = `INSERT INTO audit(id,tenant,actor,action,target,outcome,created) VALUES($1,$2,$3,'callback.completed',$4,$5,$6)`

type callbackDeliveryRow struct {
	destination, state    string
	fence                 int64
	attempts, maxAttempts int
	owner, recovery       *string
	lease, claimed, hard  *time.Time
	expires               time.Time
	delivered             *time.Time
	available             time.Time
	body                  []byte
	digest                string
	lastCode              *string
	graph                 bool
	eventBody             []byte
}
type callbackAttemptRow struct {
	number             int
	owner, recovery    string
	started            time.Time
	prepared, finished *time.Time
	outcome            string
	status             *int
	code               *string
}
type callbackSlotRow struct {
	kind, id        string
	number          int
	owner, delivery string
	fence           int64
	recovery        string
	slotFence       int64
	lease           time.Time
}

func callbackSameStatus(p *int, status int) bool {
	return (p == nil && status == 0) || (p != nil && *p == status && status != 0)
}
func callbackRetryState(c CallbackCompletion, now, expires time.Time) (string, time.Time) {
	if c.Outcome != "retryable_failure" && c.Outcome != "delivery_unknown" {
		return c.State, time.Time{}
	}
	next := now.Add(time.Second << (c.Handle.Attempt - 1))
	if c.Handle.Attempt >= c.Handle.MaxAttempts || !next.Before(expires) {
		return "dead_letter", time.Time{}
	}
	return "ready", next
}
func callbackAuditID(h CallbackCompletionHandle) string {
	sum := sha256.Sum256([]byte("callback.complete.v1\x00" + h.Tenant + "\x00" + h.DeliveryID + "\x00" + strconv.FormatInt(h.Fence, 10)))
	return "callback_complete_" + hex.EncodeToString(sum[:])
}
func callbackClassify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCommitOutcomeUnknown) {
		return ErrCallbackCompletionUnknown
	}
	for _, known := range []error{ErrCallbackCompletionInvalid, ErrCallbackCompletionStale, ErrCallbackCompletionConflict, ErrCallbackCompletionIntegrity} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrCallbackCompletionUnavailable
}

// Complete validates a finite intent against locked authoritative rows. Exact
// finished duplicates are read-only; stale/conflicting calls cannot change rows.
// DB writes and an audit commit together. No ledger charge is released here.
func (s *CallbackCompleter) Complete(ctx context.Context, c CallbackCompletion) error {
	requirePrepared, err := validateCallbackCompletion(c)
	if err != nil || ctx == nil {
		return ErrCallbackCompletionInvalid
	}
	h := c.Handle
	if h.Owner != s.runtime.instance || h.Recovery != s.runtime.epoch {
		return ErrCallbackCompletionStale
	}
	if ctx.Err() != nil {
		return ErrCallbackCompletionUnavailable
	}
	if err = s.gate(ctx); err != nil {
		return ErrCallbackCompletionStale
	}
	err = s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		var tenant, destination, scope string
		if e := tx.QueryRow(ctx, callbackLockTenant, h.Tenant).Scan(&tenant); e != nil {
			return e
		}
		if tenant != h.Tenant {
			return ErrCallbackCompletionIntegrity
		}
		if e := tx.QueryRow(ctx, callbackDestinationHint, h.Tenant, h.DeliveryID).Scan(&destination); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return ErrCallbackCompletionStale
			}
			return e
		}
		if !asciiIdentifier(destination) {
			return ErrCallbackCompletionIntegrity
		}
		if e := tx.QueryRow(ctx, callbackLockDestination, h.Tenant, destination).Scan(&scope); e != nil {
			return e
		}
		if scope != destination {
			return ErrCallbackCompletionIntegrity
		}
		// Do not use LeaseStore.run: disabled scheduling scopes must still settle
		// already-issued attempts. Current runtime/recovery authority is still needed.
		if e := lockRuntime(ctx, tx, s.runtime, "worker"); e != nil {
			return ErrCallbackCompletionStale
		}
		var d callbackDeliveryRow
		if e := tx.QueryRow(ctx, callbackReadDelivery, h.Tenant, h.DeliveryID).Scan(&d.destination, &d.state, &d.fence, &d.attempts, &d.maxAttempts, &d.owner, &d.recovery, &d.lease, &d.claimed, &d.hard, &d.expires, &d.delivered, &d.available, &d.body, &d.digest, &d.lastCode, &d.graph, &d.eventBody); e != nil {
			return e
		}
		if d.destination != destination || !d.graph || len(d.body) == 0 || len(d.body) > 4096 || !bytes.Equal(d.body, d.eventBody) {
			return ErrCallbackCompletionIntegrity
		}
		digest := sha256.Sum256(d.body)
		if hex.EncodeToString(digest[:]) != d.digest {
			return ErrCallbackCompletionIntegrity
		}
		if d.fence != h.Fence || d.attempts != h.Attempt || d.maxAttempts != h.MaxAttempts {
			return ErrCallbackCompletionStale
		}
		var a callbackAttemptRow
		if e := tx.QueryRow(ctx, callbackReadAttempt, h.Tenant, h.DeliveryID, h.Fence).Scan(&a.number, &a.owner, &a.recovery, &a.started, &a.prepared, &a.finished, &a.outcome, &a.status, &a.code); e != nil {
			return e
		}
		if a.number != h.Attempt || a.owner != h.Owner || a.recovery != h.Recovery {
			return ErrCallbackCompletionStale
		}
		if requirePrepared && a.prepared == nil {
			return ErrCallbackCompletionIntegrity
		}
		var slots []callbackSlotRow
		var oldSlot bool
		if a.finished != nil {
			// Do not lock or require emptiness of slots reassigned to another delivery.
			if e := tx.QueryRow(ctx, callbackOldSlotExists, h.Tenant, h.DeliveryID, h.Owner, h.Recovery, h.Fence).Scan(&oldSlot); e != nil {
				return e
			}
		} else {
			rows, e := tx.Query(ctx, callbackReadSlots, h.Tenant, h.DeliveryID)
			if e != nil {
				return e
			}
			for rows.Next() {
				var v callbackSlotRow
				if e = rows.Scan(&v.kind, &v.id, &v.number, &v.owner, &v.delivery, &v.fence, &v.recovery, &v.slotFence, &v.lease); e != nil {
					rows.Close()
					return e
				}
				slots = append(slots, v)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		// Both the generation gate and PostgreSQL clock are refreshed after every
		// potentially blocking row lock. Unknown/expired authority never releases slots.
		var now time.Time
		if e := tx.QueryRow(ctx, callbackFinalTime).Scan(&now); e != nil {
			return e
		}
		if e := s.gate(ctx); e != nil {
			return ErrCallbackCompletionStale
		}
		if now.UnixMilli() < 0 || now.UnixMilli() > maxSafeInteger || d.claimed == nil || d.hard == nil || a.started.IsZero() || a.started.Before(*d.claimed) || a.started.After(now) || d.hard.After(d.expires) {
			return ErrCallbackCompletionIntegrity
		}
		if a.prepared != nil && (a.prepared.Before(a.started) || a.prepared.After(now)) {
			return ErrCallbackCompletionIntegrity
		}
		if a.finished != nil {
			if a.finished.Before(a.started) || a.finished.After(now) || !a.finished.Before(*d.hard) || (a.prepared != nil && a.prepared.After(*a.finished)) {
				return ErrCallbackCompletionIntegrity
			}
			if oldSlot || d.owner != nil || d.recovery != nil || d.lease != nil {
				return ErrCallbackCompletionIntegrity
			}
			if a.outcome != c.Outcome || a.code == nil || *a.code != c.Code || !callbackSameStatus(a.status, c.Status) || d.state != c.State {
				return ErrCallbackCompletionConflict
			}
			if d.lastCode == nil || *d.lastCode != *a.code {
				return ErrCallbackCompletionIntegrity
			}
			state, next := callbackRetryState(c, *a.finished, d.expires)
			if state != d.state || (state == "ready" && !d.available.Equal(next)) {
				return ErrCallbackCompletionIntegrity
			}
			if c.State == "delivered" {
				if d.delivered == nil || !d.delivered.Equal(*a.finished) {
					return ErrCallbackCompletionIntegrity
				}
			} else if d.delivered != nil {
				return ErrCallbackCompletionIntegrity
			}
			return nil // original durable completion; no repeated writes or audit
		}
		if d.state != "leased" || d.owner == nil || *d.owner != h.Owner || d.recovery == nil || *d.recovery != h.Recovery || d.lease == nil || a.outcome != "in_progress" {
			return ErrCallbackCompletionStale
		}
		if a.status != nil || a.code != nil || d.delivered != nil {
			return ErrCallbackCompletionIntegrity
		}
		if !now.Before(*d.lease) || !now.Before(*d.hard) || !now.Before(d.expires) {
			return ErrCallbackCompletionStale
		}
		if d.lease.After(*d.hard) || d.hard.Before(*d.claimed) {
			return ErrCallbackCompletionIntegrity
		}
		if len(slots) != 2 {
			return ErrCallbackCompletionIntegrity
		}
		for i, v := range slots {
			kind, id := "tenant", h.Tenant
			if i == 1 {
				kind, id = "destination", destination
			}
			if v.kind != kind || v.id != id || v.number < 0 || v.slotFence <= 0 || v.owner != h.Owner || v.delivery != h.DeliveryID || v.fence != h.Fence || v.recovery != h.Recovery || !v.lease.Equal(*d.lease) {
				return ErrCallbackCompletionIntegrity
			}
		}
		state, next := callbackRetryState(c, now, d.expires)
		if state != c.State {
			return ErrCallbackCompletionStale
		}
		var status any
		if c.Status != 0 {
			status = c.Status
		}
		tag, e := tx.Exec(ctx, callbackUpdateAttempt, h.Tenant, h.DeliveryID, h.Fence, h.Owner, h.Recovery, now, c.Outcome, status, c.Code, h.Attempt)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrCallbackCompletionStale
		}
		var nextArg any
		if !next.IsZero() {
			nextArg = next
		}
		tag, e = tx.Exec(ctx, callbackUpdateDelivery, h.Tenant, h.DeliveryID, h.Fence, h.Owner, h.Recovery, state, nextArg, now, c.Code, h.Attempt, h.MaxAttempts)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrCallbackCompletionStale
		}
		for _, v := range slots {
			tag, e = tx.Exec(ctx, callbackReleaseSlot, v.kind, h.Tenant, v.id, v.number, h.Owner, h.DeliveryID, h.Fence, h.Recovery, v.slotFence, v.lease)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return ErrCallbackCompletionStale
			}
		}
		tag, e = tx.Exec(ctx, callbackInsertAudit, callbackAuditID(h), h.Tenant, h.Owner, h.DeliveryID, state+":"+c.Code, now.UnixMilli())
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrCallbackCompletionIntegrity
		}
		return nil
	})
	return callbackClassify(err)
}
