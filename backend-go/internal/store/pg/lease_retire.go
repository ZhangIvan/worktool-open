package pg

import (
	"context"
	"github.com/jackc/pgx/v5"
	"math"
)

// retireCommands preserves unknown/results. It never manufactures a result or
// releases a reservation. Only the exact old physical connection is considered.
func retireCommands(ctx context.Context, tx pgx.Tx, h LeaseHandle, ms int64) (string, error) {
	rows, e := tx.Query(ctx, `SELECT id,status,attempt,command_version,result IS NOT NULL FROM commands WHERE tenant=$1 AND device=$2 AND dispatch_session=$3 AND dispatch_owner=$4 AND dispatch_fence=$5 AND dispatch_recovery_epoch=$6 AND dispatch_connection=$7 AND status IN ('delivered','accepted','running','execution_unknown') ORDER BY id COLLATE "C" LIMIT 2 FOR UPDATE`, h.Tenant, h.DeviceID, h.SessionID, h.OwnerInstance, h.Fence, h.RecoveryEpoch, h.ConnectionID)
	if e != nil {
		return "", e
	}
	type item struct {
		id, status string
		attempt    *string
		version    int64
		result     bool
	}
	items := []item{}
	for rows.Next() {
		var v item
		if e = rows.Scan(&v.id, &v.status, &v.attempt, &v.version, &v.result); e != nil {
			rows.Close()
			return "", e
		}
		items = append(items, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if len(items) > 1 {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	if len(items) == 0 {
		return "", nil
	}
	v := items[0]
	if v.status == "execution_unknown" {
		return "", nil
	}
	if v.result || v.attempt == nil || v.version == math.MaxInt64 {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	var contradictory bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM command_events WHERE tenant=$1 AND command=$2 AND target IN ('succeeded','partially_succeeded','failed','rejected','cancelled','expired'))`, h.Tenant, v.id).Scan(&contradictory)
	if e != nil {
		return "", e
	}
	if contradictory {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	tag, e := tx.Exec(ctx, `UPDATE commands SET status='execution_unknown',command_version=command_version+1 WHERE tenant=$1 AND id=$2 AND device=$3 AND attempt=$4 AND command_version=$5 AND status=$6 AND dispatch_session=$7 AND dispatch_owner=$8 AND dispatch_fence=$9 AND dispatch_recovery_epoch=$10 AND dispatch_connection=$11 AND result IS NULL`, h.Tenant, v.id, h.DeviceID, *v.attempt, v.version, v.status, h.SessionID, h.OwnerInstance, h.Fence, h.RecoveryEpoch, h.ConnectionID)
	if e != nil {
		return "", e
	}
	if tag.RowsAffected() != 1 {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	if e = commandTransition(ctx, tx, h, v.id, v.status, "lost_execution_evidence", "execution_unknown", ms); e != nil {
		return "", e
	}
	_, e = tx.Exec(ctx, `INSERT INTO outbox(tenant,device,command,kind,state) VALUES($1,$2,$3,'command_state_changed','pending') ON CONFLICT(tenant,command,kind) DO UPDATE SET state='pending'`, h.Tenant, h.DeviceID, v.id)
	return v.id, e
}
func commandTransition(ctx context.Context, tx pgx.Tx, h LeaseHandle, id, source, event, target string, ms int64) error {
	eid, e := freshID("evt_")
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO command_events(tenant,command,event_id,source,event,target,created) VALUES($1,$2,$3,$4,$5,$6,$7)`, h.Tenant, id, eid, source, event, target, ms)
	if e != nil {
		return e
	}
	aid, e := freshID("audit_")
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit(id,tenant,actor,action,target,outcome,created) VALUES($1,$2,$3,$4,$5,$6,$7)`, aid, h.Tenant, h.OwnerInstance, "command."+event, id, target, ms)
	return e
}
func (s *LeaseStore) CloseExactSession(ctx context.Context, r RuntimeAuthority, h LeaseHandle) (RetireOutcome, error) {
	return s.retire(ctx, r, h, false)
}
func (s *LeaseStore) RecoverExpiredLease(ctx context.Context, r RuntimeAuthority, h LeaseHandle) (RetireOutcome, error) {
	return s.retire(ctx, r, h, true)
}
func (s *LeaseStore) retire(ctx context.Context, r RuntimeAuthority, h LeaseHandle, reap bool) (RetireOutcome, error) {
	var out RetireOutcome
	if !validHandle(h) || !r.trusted || r.epoch != h.RecoveryEpoch || (!reap && r.instance != h.OwnerInstance) {
		return out, admissionError("INVALID_INPUT")
	}
	e := s.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		row, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
		if e != nil {
			return e
		}
		role := "gateway"
		if reap {
			role = "worker"
		}
		if e = lockRuntime(ctx, tx, r, role); e != nil {
			return e
		}
		if row == nil || row.state != "active" || !sameHandle(row.LeaseHandle, h) {
			return nil
		}
		// Lock device and exact session before command, matching the claim lock order.
		var device string
		e = tx.QueryRow(ctx, `SELECT id FROM devices WHERE tenant=$1 AND id=$2 FOR SHARE`, h.Tenant, h.DeviceID).Scan(&device)
		if e != nil {
			return e
		}
		var session string
		e = tx.QueryRow(ctx, `SELECT id FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=$3 FOR UPDATE`, h.Tenant, h.DeviceID, h.SessionID).Scan(&session)
		if e != nil {
			return e
		}
		now, ms, e := s.now(ctx, tx)
		if e != nil {
			return e
		}
		if reap && row.LeaseUntil.After(now) {
			return admissionError("NOT_EXPIRED")
		}
		if row.Fence == math.MaxInt64 {
			return admissionError("FENCE_EXHAUSTED")
		}
		id, e := retireCommands(ctx, tx, h, ms)
		if e != nil {
			return e
		}
		if e = clearSnapshot(ctx, tx, h); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE device_gateway_leases SET fence=fence+1,state='retired',lease_until=LEAST(lease_until,$3),updated_at=$3 WHERE tenant=$1 AND device=$2`, h.Tenant, h.DeviceID, now)
		if e != nil {
			return e
		}
		if e = leaseAudit(ctx, tx, h, "gateway.lease_retired", ms); e != nil {
			return e
		}
		out = RetireOutcome{true, id}
		return nil
	})
	if e != nil {
		return RetireOutcome{}, e
	}
	return out, nil
}
