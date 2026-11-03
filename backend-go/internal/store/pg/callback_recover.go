package pg

import (
	"context"
	"errors"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"time"
)

// RecoverExpired settles ONE explicitly selected expired delivery in the current
// external generation. It neither sends nor automatically schedules a daemon.
// Cross-generation/PITR recovery is deliberately unavailable.
func (s *CallbackStore) RecoverExpired(ctx context.Context, tenant, id string) error {
	if ctx == nil || ctx.Err() != nil || !asciiIdentifier(tenant) || !asciiIdentifier(id) {
		return cb.ErrStale
	}
	if e := s.gate(ctx); e != nil {
		return cb.ErrStale
	}
	e := s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		var dest string
		if e := tx.QueryRow(ctx, callbackDestinationHint, tenant, id).Scan(&dest); e != nil {
			return e
		}
		if _, ok := s.policies[tenant+"\x00"+dest]; !ok {
			return cb.ErrDenied
		}
		if e := s.lockScopes(ctx, tx, tenant, dest); e != nil {
			return e
		}
		d, e := callbackReadLocked(ctx, tx, tenant, id)
		if e != nil {
			return e
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
		if now.Before(*d.lease) {
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
		return auditGateway(ctx, tx, tenant, s.runtime.instance, "callback.recovered", id, code, now.UnixMilli())
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return cb.ErrStale
	}
	return callbackStoreError(e)
}
