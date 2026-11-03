package pg

import (
	"context"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/jackc/pgx/v5"
	"net/netip"
	"time"
)

func (s *CallbackStore) Prepare(ctx context.Context, p cb.Prepared) (cb.Permit, error) {
	start := s.now()
	h := p.Handle
	policy, ok := s.policies[h.Tenant+"\x00"+p.Binding.Destination]
	if ctx == nil || ctx.Err() != nil || !ok || policy.Binding != p.Binding || h.Owner != s.runtime.instance || h.Recovery != s.runtime.epoch || !asciiIdentifier(h.DeliveryID) || h.Fence < 1 || h.Attempt < 1 || h.Attempt > h.MaxAttempts || h.MaxAttempts > 4 || p.Deadline.Sub(start) <= 0 || p.Deadline.Sub(start) > 2*time.Second {
		return cb.Permit{}, cb.ErrStale
	}
	target, e := s.destinations.Authorize(p.Binding.URL)
	if e != nil {
		return cb.Permit{}, cb.ErrDenied
	}
	if _, e = target.Pin([]netip.Addr{p.IP}); e != nil {
		return cb.Permit{}, cb.ErrDenied
	}
	if e = s.gate(ctx); e != nil {
		return cb.Permit{}, cb.ErrStale
	}
	var decision, limit time.Time
	e = s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		if e := s.lockScopes(ctx, tx, h.Tenant, p.Binding.Destination); e != nil {
			return e
		}
		a, e := s.lockAuthority(ctx, tx, h.Tenant, h.DeliveryID, p.Binding.Destination)
		if e != nil {
			return e
		}
		d, e := callbackReadLocked(ctx, tx, h.Tenant, h.DeliveryID)
		if e != nil {
			return e
		}
		if d.state != "leased" || d.owner == nil || *d.owner != h.Owner || d.recovery == nil || *d.recovery != h.Recovery || d.fence != h.Fence || d.attempts != h.Attempt || d.maxAttempts != h.MaxAttempts || d.lease == nil || d.hard == nil || d.digest != p.Digest || a.binding != p.Binding {
			return cb.ErrStale
		}
		var event string
		var policy, key int64
		e = tx.QueryRow(ctx, `SELECT event_id,policy_version,key_version FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2`, h.Tenant, h.DeliveryID).Scan(&event, &policy, &key)
		if e != nil {
			return e
		}
		if event != p.EventID || policy != p.Binding.PolicyVersion || key != p.Binding.KeyVersion {
			return cb.ErrStale
		}
		var attempt callbackAttemptRow
		e = tx.QueryRow(ctx, callbackReadAttempt, h.Tenant, h.DeliveryID, h.Fence).Scan(&attempt.number, &attempt.owner, &attempt.recovery, &attempt.started, &attempt.prepared, &attempt.finished, &attempt.outcome, &attempt.status, &attempt.code)
		if e != nil {
			return e
		}
		if attempt.number != h.Attempt || attempt.owner != h.Owner || attempt.recovery != h.Recovery || attempt.prepared != nil || attempt.finished != nil || attempt.outcome != "in_progress" {
			return cb.ErrStale
		}
		rows, e := tx.Query(ctx, callbackReadSlots, h.Tenant, h.DeliveryID)
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
			kind, id := "tenant", h.Tenant
			if i == 1 {
				kind, id = "destination", p.Binding.Destination
			}
			if v.kind != kind || v.id != id || v.owner != h.Owner || v.delivery != h.DeliveryID || v.fence != h.Fence || v.recovery != h.Recovery || v.slotFence < 1 || !v.lease.Equal(*d.lease) {
				return cb.ErrStale
			}
		}
		if e = tx.QueryRow(ctx, callbackFinalTime).Scan(&decision); e != nil {
			return e
		}
		if e = s.gate(ctx); e != nil {
			return cb.ErrStale
		}
		if !a.valid || a.expires <= decision.UnixMilli() {
			return cb.ErrDenied
		}
		limit = *d.lease
		for _, v := range []time.Time{*d.hard, d.expires, time.UnixMilli(a.expires)} {
			if v.Before(limit) {
				limit = v
			}
		}
		mapped, e := callbackLocalDeadline(start, s.now(), decision, limit)
		if e != nil || p.Deadline.After(mapped) || !s.now().Before(p.Deadline) {
			return cb.ErrStale
		}
		// Disabled/rate-blocked scopes stop a new send but do not discard evidence.
		for _, v := range slots {
			var enabled bool
			e = tx.QueryRow(ctx, `SELECT enabled AND (blocked_until IS NULL OR blocked_until<=$4) FROM scope_limits WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3`, v.kind, h.Tenant, v.id, decision).Scan(&enabled)
			if e != nil {
				return e
			}
			if !enabled {
				return cb.ErrStale
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE callback_attempts SET send_prepared_at=$6 WHERE tenant=$1 AND delivery_id=$2 AND fence=$3 AND owner_instance=$4 AND recovery_epoch=$5 AND attempt_no=$7 AND outcome='in_progress' AND send_prepared_at IS NULL AND finished_at IS NULL`, h.Tenant, h.DeliveryID, h.Fence, h.Owner, h.Recovery, decision, h.Attempt)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return cb.ErrStale
		}
		return auditGateway(ctx, tx, h.Tenant, h.Owner, "callback.prepared", h.DeliveryID, "prepared", decision.UnixMilli())
	})
	if e != nil {
		return cb.Permit{}, callbackStoreError(e)
	}
	end := s.now()
	mapped, e := callbackLocalDeadline(start, end, decision, limit)
	if e != nil || !end.Before(p.Deadline) || p.Deadline.After(mapped) {
		return cb.Permit{}, cb.ErrStale
	}
	if ctx.Err() != nil {
		return cb.Permit{}, cb.ErrStale
	}
	if e = s.gate(ctx); e != nil {
		return cb.Permit{}, cb.ErrStale
	}
	return cb.Permit{Prepared: p, Fresh: true}, nil
}

var _ cb.Store = (*CallbackStore)(nil)
