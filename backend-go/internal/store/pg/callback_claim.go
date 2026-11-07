package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"math"
	"sort"
	"time"
)

type callbackCandidate struct{ tenant, dest, id string }
type callbackFreeSlot struct {
	kind, id string
	number   int
	fence    int64
}

func (s *CallbackStore) Claim(ctx context.Context) (cb.Delivery, bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return cb.Delivery{}, false, cb.ErrStale
	}
	if e := s.gate(ctx); e != nil {
		return cb.Delivery{}, false, cb.ErrStale
	}
	keys := make([]string, 0, len(s.policies))
	for k := range s.policies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tenants, dests := []string{}, []string{}
	for _, k := range keys {
		p := s.policies[k]
		tenants = append(tenants, p.Binding.Tenant)
		dests = append(dests, p.Binding.Destination)
	}
	var candidates []callbackCandidate
	e := s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT o.tenant,o.destination_id,o.delivery_id FROM unnest($1::text[],$2::text[]) p(tenant,dest) JOIN LATERAL (SELECT x.* FROM callback_outbox x WHERE x.tenant=p.tenant AND x.destination_id=p.dest AND x.state='ready' AND x.available_at<=clock_timestamp() ORDER BY x.available_at,x.delivery_id COLLATE "C" LIMIT 1) o ON true JOIN scope_limits t ON t.tenant=o.tenant AND t.scope_kind='tenant' AND t.scope_id=o.tenant JOIN scope_limits d ON d.tenant=o.tenant AND d.scope_kind='destination' AND d.scope_id=o.destination_id WHERE o.state='ready' AND o.available_at<=clock_timestamp() AND t.enabled AND d.enabled ORDER BY t.virtual_finish,o.tenant COLLATE "C",d.virtual_finish,o.destination_id COLLATE "C",o.available_at,o.delivery_id COLLATE "C" LIMIT 32`, tenants, dests)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c callbackCandidate
			if e = rows.Scan(&c.tenant, &c.dest, &c.id); e != nil {
				return e
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if e != nil {
		return cb.Delivery{}, false, callbackStoreError(e)
	}
	for _, c := range candidates {
		d, ok, e := s.claimOne(ctx, c)
		if e != nil || ok {
			return d, ok, e
		}
	}
	return cb.Delivery{}, false, nil
}
func (s *CallbackStore) claimOne(ctx context.Context, c callbackCandidate) (cb.Delivery, bool, error) {
	start := s.now()
	var out cb.Delivery
	var claimed bool
	var decision, lease, expires time.Time
	e := s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		if e := s.lockScopeMode(ctx, tx, c.tenant, c.dest, true); e != nil {
			return e
		}
		a, e := s.lockAuthority(ctx, tx, c.tenant, c.id, c.dest)
		if e != nil {
			return e
		}
		d, e := callbackReadLocked(ctx, tx, c.tenant, c.id)
		if e != nil {
			return e
		}
		if d.state != "ready" {
			return nil
		}
		var event string
		var policy, key int64
		e = tx.QueryRow(ctx, `SELECT event_id,policy_version,key_version FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2`, c.tenant, c.id).Scan(&event, &policy, &key)
		if e != nil {
			return e
		}
		if policy != a.binding.PolicyVersion || key != a.binding.KeyVersion {
			a.valid = false
		}
		// Scope and slot locks happen before the final authoritative clock read.
		slots := []callbackFreeSlot{}
		for _, v := range []callbackFreeSlot{{kind: "tenant", id: c.tenant}, {kind: "destination", id: c.dest}} {
			e = tx.QueryRow(ctx, `SELECT s.slot_no,s.slot_fence FROM scope_slots s JOIN scope_limits l USING(scope_kind,tenant,scope_id) WHERE s.scope_kind=$1 AND s.tenant=$2 AND s.scope_id=$3 AND s.slot_no<l.max_in_flight AND s.owner_instance IS NULL ORDER BY s.slot_no LIMIT 1 FOR UPDATE OF s`, v.kind, c.tenant, v.id).Scan(&v.number, &v.fence)
			if errors.Is(e, pgx.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			if v.fence == math.MaxInt64 {
				return cb.ErrStale
			}
			slots = append(slots, v)
		}
		if e = tx.QueryRow(ctx, callbackFinalTime).Scan(&decision); e != nil {
			return e
		}
		if e = s.gate(ctx); e != nil {
			return cb.ErrStale
		}
		if d.available.After(decision) {
			return nil
		}
		terminal, reason := "", ""
		if !a.valid || a.expires <= decision.UnixMilli() {
			terminal = "cancelled"
			reason = "DELIVERY_AUTHORITY_REVOKED"
		} else if !decision.Before(d.expires) || d.attempts >= d.maxAttempts {
			terminal = "dead_letter"
			reason = "DELIVERY_EXPIRED"
		}
		if terminal != "" {
			tag, e := tx.Exec(ctx, `UPDATE callback_outbox SET state=$3,last_error_code=$4 WHERE tenant=$1 AND delivery_id=$2 AND state='ready'`, c.tenant, c.id, terminal, reason)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return cb.ErrStale
			}
			return auditGateway(ctx, tx, c.tenant, s.runtime.instance, "callback.not_claimed", c.id, reason, decision.UnixMilli())
		}
		if d.fence == math.MaxInt64 {
			return cb.ErrStale
		}
		// Refill and debit both scopes in the same transaction; a second failure rolls
		// back the first debit. No local rate counter can authorize another worker.
		for _, slot := range slots {
			var eligible bool
			e = tx.QueryRow(ctx, `SELECT enabled AND (blocked_until IS NULL OR blocked_until<=$4) AND refill_at<=$4 AND LEAST(burst,tokens+EXTRACT(EPOCH FROM ($4::timestamptz-refill_at))*rate_per_sec)>=1 FROM scope_limits WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3`, slot.kind, c.tenant, slot.id, decision).Scan(&eligible)
			if e != nil {
				return e
			}
			if !eligible {
				return nil
			}
		}
		lease = decision.Add(10 * time.Second)
		if d.expires.Before(lease) {
			lease = d.expires
		}
		expires = d.expires
		for _, slot := range slots {
			tag, e := tx.Exec(ctx, `UPDATE scope_limits SET tokens=LEAST(burst,tokens+EXTRACT(EPOCH FROM ($4::timestamptz-refill_at))*rate_per_sec)-1,refill_at=$4,virtual_finish=virtual_finish+1.0/weight WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3`, slot.kind, c.tenant, slot.id, decision)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return cb.ErrStale
			}
		}
		fence, attempt := d.fence+1, d.attempts+1
		tag, e := tx.Exec(ctx, `UPDATE callback_outbox SET state='leased',fence=$3,attempt_count=$4,claimed_at=$5,hard_deadline_at=$6,lease_until=$6,lease_owner=$7,lease_recovery_epoch=$8 WHERE tenant=$1 AND delivery_id=$2 AND state='ready' AND fence=$9 AND attempt_count=$10`, c.tenant, c.id, fence, attempt, decision, lease, s.runtime.instance, s.runtime.epoch, d.fence, d.attempts)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return cb.ErrStale
		}
		_, e = tx.Exec(ctx, `INSERT INTO callback_attempts(tenant,delivery_id,fence,attempt_no,recovery_epoch,owner_instance,started_at,outcome) VALUES($1,$2,$3,$4,$5,$6,$7,'in_progress')`, c.tenant, c.id, fence, attempt, s.runtime.epoch, s.runtime.instance, decision)
		if e != nil {
			return e
		}
		for _, slot := range slots {
			tag, e = tx.Exec(ctx, `UPDATE scope_slots SET owner_instance=$5,delivery_id=$6,delivery_fence=$7,recovery_epoch=$8,slot_fence=slot_fence+1,lease_until=$9 WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3 AND slot_no=$4 AND owner_instance IS NULL AND slot_fence=$10`, slot.kind, c.tenant, slot.id, slot.number, s.runtime.instance, c.id, fence, s.runtime.epoch, lease, slot.fence)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return cb.ErrStale
			}
		}
		if e = auditGateway(ctx, tx, c.tenant, s.runtime.instance, "callback.claimed", c.id, "leased", decision.UnixMilli()); e != nil {
			return e
		}
		out = cb.Delivery{Handle: cb.Handle{Tenant: c.tenant, DeliveryID: c.id, Owner: s.runtime.instance, Recovery: s.runtime.epoch, Fence: fence, Attempt: attempt, MaxAttempts: d.maxAttempts}, Binding: a.binding, EventID: event, Digest: d.digest, Body: append([]byte(nil), d.body...)}
		claimed = true
		return nil
	})
	// Only a clean busy rollback permits trying the next candidate. Runner may
	// join this sentinel with cleanup failures; those must remain fail-closed.
	if e == errCallbackCandidateBusy {
		return cb.Delivery{}, false, nil
	}
	if e != nil {
		return cb.Delivery{}, false, callbackStoreError(e)
	}
	if !claimed {
		return cb.Delivery{}, false, nil
	}
	end := s.now()
	out.LeaseUntil, e = callbackLocalDeadline(start, end, decision, lease)
	if e != nil {
		return cb.Delivery{}, false, e
	}
	out.HardDeadline = out.LeaseUntil
	out.ExpiresAt, e = callbackLocalDeadline(start, end, decision, expires)
	if e != nil {
		return cb.Delivery{}, false, e
	}
	return out, true, nil
}
