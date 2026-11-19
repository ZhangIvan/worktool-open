package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

// RecoverExpired settles ONE explicitly selected expired delivery in the current
// external generation. It neither sends nor automatically schedules a daemon.
// Cross-generation/PITR recovery is deliberately unavailable.
func (s *CallbackStore) RecoverExpired(ctx context.Context, tenant, id string) error {
	_, err := s.recoverExpired(ctx, tenant, id, false)
	return err
}

// recoverExpired preserves explicit recovery semantics. The selector may skip a
// clean busy scope or a row whose successor is no longer expired; neither counts
// as recovery. Generation and cleanup uncertainty always remain errors.
func (s *CallbackStore) recoverExpired(ctx context.Context, tenant, id string, skip bool) (bool, error) {
	if ctx == nil || ctx.Err() != nil || !asciiIdentifier(tenant) || !asciiIdentifier(id) {
		return false, cb.ErrStale
	}
	if e := s.gate(ctx); e != nil {
		return false, cb.ErrStale
	}
	recovered := false
	e := s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		var dest string
		if e := tx.QueryRow(ctx, callbackDestinationHint, tenant, id).Scan(&dest); e != nil {
			return e
		}
		if _, ok := s.policies[tenant+"\x00"+dest]; !ok {
			return cb.ErrDenied
		}
		if e := s.lockScopeMode(ctx, tx, tenant, dest, skip); e != nil {
			return e
		}
		d, e := callbackReadLocked(ctx, tx, tenant, id)
		if e != nil {
			return e
		}
		if d.destination != dest {
			return cb.ErrStale
		}
		if d.state != "leased" {
			return nil
		}
		if d.owner == nil || d.recovery == nil || *d.recovery != s.runtime.epoch || d.lease == nil || d.hard == nil {
			return cb.ErrStale
		}
		var a callbackAttemptRow
		e = tx.QueryRow(ctx, callbackReadAttempt, tenant, id, d.fence).Scan(&a.number, &a.owner, &a.recovery, &a.started, &a.prepared, &a.finished, &a.outcome, &a.status, &a.code)
		if e != nil {
			return e
		}
		if a.number != d.attempts || a.owner != *d.owner || a.recovery != *d.recovery || a.finished != nil || a.outcome != "in_progress" {
			return cb.ErrStale
		}
		rows, e := tx.Query(ctx, callbackReadSlots, tenant, id)
		if e != nil {
			return e
		}
		slots := []callbackSlotRow{}
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
		if len(slots) != 2 {
			return cb.ErrStale
		}
		for i, v := range slots {
			kind, key := "tenant", tenant
			if i == 1 {
				kind, key = "destination", dest
			}
			if v.kind != kind || v.id != key || v.owner != *d.owner || v.delivery != id || v.fence != d.fence || v.recovery != *d.recovery || !v.lease.Equal(*d.lease) || v.slotFence < 1 {
				return cb.ErrStale
			}
		}
		var now time.Time
		if e = tx.QueryRow(ctx, callbackFinalTime).Scan(&now); e != nil {
			return e
		}
		if e = s.gate(ctx); e != nil {
			return cb.ErrStale
		}
		// Expiry is not permission to overwrite contradictory attempt evidence.
		// Check the locked rows against the final database time before any DML
		// or clean unexpired skip. Retain the original evidence and slots on error.
		if now.UnixMilli() < 0 || now.UnixMilli() > maxSafeInteger || d.claimed == nil || a.started.IsZero() || a.started.Before(*d.claimed) || a.started.After(now) || a.status != nil || a.code != nil || d.delivered != nil {
			return cb.ErrStale
		}
		if a.prepared != nil && (a.prepared.Before(a.started) || a.prepared.After(now)) {
			return cb.ErrStale
		}
		if now.Before(*d.lease) {
			if skip {
				return nil
			}
			return cb.ErrStale
		}
		outcome, code := "lease_lost", "LEASE_EXPIRED_BEFORE_PREPARE"
		if a.prepared != nil {
			outcome, code = "delivery_unknown", "LEASE_EXPIRED_AFTER_PREPARE"
		}
		next := now.Add(time.Second << uint(d.attempts-1))
		state := "ready"
		if d.attempts >= d.maxAttempts || !next.Before(d.expires) {
			state = "dead_letter"
		}
		tag, e := tx.Exec(ctx, callbackUpdateAttempt, tenant, id, d.fence, *d.owner, *d.recovery, now, outcome, nil, code, d.attempts)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return cb.ErrStale
		}
		tag, e = tx.Exec(ctx, callbackUpdateDelivery, tenant, id, d.fence, *d.owner, *d.recovery, state, next, now, code, d.attempts, d.maxAttempts)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return cb.ErrStale
		}
		for _, v := range slots {
			tag, e = tx.Exec(ctx, callbackReleaseSlot, v.kind, tenant, v.id, v.number, v.owner, id, v.fence, v.recovery, v.slotFence, v.lease)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return cb.ErrStale
			}
		}
		if e = auditGateway(ctx, tx, tenant, s.runtime.instance, "callback.recovered", id, code, now.UnixMilli()); e != nil {
			return e
		}
		recovered = true
		return nil
	})
	// Only the exact clean rollback sentinel permits continuing past contention.
	if skip && e == errCallbackCandidateBusy {
		return false, nil
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return false, cb.ErrStale
	}
	if e != nil {
		return false, callbackStoreError(e)
	}
	return recovered, nil
}

// Discovery holds no row locks and grants no authority. At most one candidate
// per copied policy is returned (32 total); oldest expiry first avoids a hot
// destination hiding another destination behind its backlog. Disabled/revoked
// scopes may still release old issued attempts, just as explicit recovery does.
// The existing partial leased-destination index supports each bounded lookup.
const callbackExpiredCandidates = `SELECT o.tenant,o.destination_id,o.delivery_id
 FROM unnest($1::text[],$2::text[]) p(tenant,dest)
 JOIN LATERAL (SELECT x.tenant,x.destination_id,x.delivery_id,x.lease_until FROM callback_outbox x
 WHERE x.tenant=p.tenant AND x.destination_id=p.dest AND x.state='leased'
 AND x.lease_recovery_epoch=$3 AND x.lease_until<=clock_timestamp()
 ORDER BY x.lease_until,x.delivery_id COLLATE "C" LIMIT 1) o ON true
 ORDER BY o.lease_until,o.tenant COLLATE "C",o.destination_id COLLATE "C",o.delivery_id COLLATE "C" LIMIT 32`

var _ cb.RecoveryStore = (*CallbackStore)(nil)

// RecoverOneExpired is the typed lifecycle adapter. A committed settlement is
// the only true result. Selection and mutation share the caller's budget, but
// each Runner may additionally perform its independent bounded cleanup.
func (s *CallbackStore) RecoverOneExpired(ctx context.Context) (bool, error) {
	if ctx == nil || ctx.Err() != nil {
		return false, cb.ErrStale
	}
	if e := s.gate(ctx); e != nil {
		return false, cb.ErrStale
	}
	keys := make([]string, 0, len(s.policies))
	for k := range s.policies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tenants, dests := make([]string, 0, len(keys)), make([]string, 0, len(keys))
	for _, k := range keys {
		p := s.policies[k]
		tenants = append(tenants, p.Binding.Tenant)
		dests = append(dests, p.Binding.Destination)
	}
	candidates := make([]callbackCandidate, 0, len(keys))
	e := s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, callbackExpiredCandidates, tenants, dests, s.runtime.epoch)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var c callbackCandidate
			if e = rows.Scan(&c.tenant, &c.dest, &c.id); e != nil {
				return e
			}
			if _, ok := s.policies[c.tenant+"\x00"+c.dest]; !ok || !asciiIdentifier(c.id) || len(candidates) >= len(keys) {
				return cb.ErrDenied
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if e != nil {
		return false, callbackStoreError(e)
	}
	for _, c := range candidates {
		// The method rechecks gate, scope/runtime, immutable graph, same epoch,
		// exact fences/slots and database expiry under the established lock order.
		recovered, e := s.recoverExpired(ctx, c.tenant, c.id, true)
		if e != nil || recovered {
			return recovered, e
		}
	}
	return false, nil
}
