package pg

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func checkF0(ctx context.Context, tx pgx.Tx, tenant, id string, materializedOK bool) error {
	var empty bool
	var results, callbacks int
	var state string
	e := tx.QueryRow(ctx, `SELECT subscription_snapshot='[]'::jsonb FROM commands WHERE tenant=$1 AND id=$2`, tenant, id).Scan(&empty)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='result'),count(*) FILTER(WHERE kind='callback'),COALESCE(min(state) FILTER(WHERE kind='result'),'') FROM admission_reservations WHERE tenant=$1 AND command=$2`, tenant, id).Scan(&results, &callbacks, &state)
	if e != nil {
		return e
	}
	if !empty || callbacks != 0 {
		return admissionError("CAPABILITY_UNAVAILABLE")
	}
	if results != 1 || (state != "held" && (!materializedOK || state != "materialized")) {
		return admissionError("INTEGRITY_CONFLICT")
	}
	return nil
}

type CommandHint struct {
	ID      string
	Version int64
}

func (s *GatewayStore) NextCommand(ctx context.Context, a SessionAuth) (*CommandHint, error) {
	if !a.a.trusted || !a.consumed {
		return nil, admissionError("UNAUTHENTICATED")
	}
	var out *CommandHint
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var h CommandHint
		e := tx.QueryRow(ctx, `SELECT c.id,c.command_version FROM commands c JOIN developer_apps a ON a.tenant=c.tenant AND a.id=c.app_id JOIN devices d ON d.tenant=c.tenant AND d.id=c.device WHERE c.tenant=$1 AND c.device=$2 AND c.status='queued' AND c.actor_type='app' AND c.capability='message.send_text' AND NOT c.cancel_requested AND c.subscription_snapshot='[]'::jsonb AND c.expires>floor(extract(epoch FROM clock_timestamp())*1000)::bigint AND a.active AND a.auth_epoch=c.app_epoch AND a.token_expires>floor(extract(epoch FROM clock_timestamp())*1000)::bigint AND a.scopes ? 'commands:write' AND d.state='active' AND d.policy=c.policy AND EXISTS(SELECT 1 FROM developer_bindings b WHERE b.tenant=c.tenant AND b.app=c.app_id AND b.device=c.device) ORDER BY c.created,c.id LIMIT 1`, a.a.tenant, a.a.device).Scan(&h.ID, &h.Version)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		out = &h
		return nil
	})
	return out, e
}
func (s *GatewayStore) Claim(ctx context.Context, a SessionAuth, h LeaseHandle, q ClaimCommandRequest) (ClaimOutcome, error) {
	if !a.consumed || a.connection != h.ConnectionID {
		return ClaimOutcome{}, admissionError("UNAUTHENTICATED")
	}
	return s.leases.claim(ctx, a.a, h, q, true)
}

// currentGateway locks physical lease/runtime/device/session, without granting
// app execution permission. It is also the evidence-ingestion authentication gate.
func (s *GatewayStore) currentGateway(ctx context.Context, tx pgx.Tx, a SessionAuth, h LeaseHandle) (*leaseRow, gatewayFacts, error) {
	var facts gatewayFacts
	if !a.consumed || a.connection != h.ConnectionID || !authorityHandle(a.a, h) {
		return nil, facts, admissionError("UNAUTHENTICATED")
	}
	row, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
	if e != nil {
		return nil, facts, e
	}
	if row == nil || row.state != "active" || !sameHandle(row.LeaseHandle, h) {
		return nil, facts, admissionError("STALE_LEASE")
	}
	if e = lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
		return nil, facts, e
	}
	facts, _, e = lockGateway(ctx, tx, a.a, row)
	if e != nil {
		return nil, facts, e
	}
	var consumed bool
	var conn string
	e = tx.QueryRow(ctx, `SELECT ws_consumed,COALESCE(ws_connection_id,'') FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=$3`, h.Tenant, h.DeviceID, h.SessionID).Scan(&consumed, &conn)
	if e != nil {
		return nil, facts, e
	}
	if !consumed || conn != h.ConnectionID {
		return nil, facts, admissionError("SESSION_NOT_CURRENT")
	}
	return row, facts, nil
}
func (s *GatewayStore) gatewayDecision(ctx context.Context, tx pgx.Tx, row *leaseRow, f gatewayFacts) (int64, error) {
	now, ms, e := s.leases.now(ctx, tx)
	if e != nil {
		return 0, e
	}
	if e = liveGateway(f, ms); e != nil {
		return 0, e
	}
	if !row.LeaseUntil.After(now) {
		return 0, admissionError("LEASE_EXPIRED")
	}
	return ms, nil
}

// Execution facts must be locked BEFORE device/session rows (lease/runtime->app
// ->device/session->binding->command). Hint fields are rechecked after locking.
type executionFacts struct {
	app, creator   string
	epoch, expires int64
	active, scope  bool
}

func lockExecutionApp(ctx context.Context, tx pgx.Tx, tenant, device, id string) (executionFacts, error) {
	var f executionFacts
	var app *string
	var actorType, capability string
	e := tx.QueryRow(ctx, `SELECT app_id,actor_type,capability FROM commands WHERE tenant=$1 AND device=$2 AND id=$3`, tenant, device, id).Scan(&app, &actorType, &capability)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, admissionError("NOT_FOUND")
	}
	if e != nil {
		return f, e
	}
	if app == nil || actorType != "app" || capability != "message.send_text" {
		return f, admissionError("UNSUPPORTED_COMMAND")
	}
	f.app = *app
	e = tx.QueryRow(ctx, `SELECT creator,auth_epoch,token_expires,active,scopes ? 'commands:write' FROM developer_apps WHERE tenant=$1 AND id=$2 FOR SHARE`, tenant, *app).Scan(&f.creator, &f.epoch, &f.expires, &f.active, &f.scope)
	return f, e
}
func lockExecutionBinding(ctx context.Context, tx pgx.Tx, tenant, device string, f executionFacts) (bool, error) {
	var id string
	e := tx.QueryRow(ctx, `SELECT device FROM developer_bindings WHERE tenant=$1 AND app=$2 AND device=$3 FOR SHARE`, tenant, f.app, device).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	return e == nil, e
}
func allowedExecution(c leaseCommand, f executionFacts, g gatewayFacts, bound bool, ms int64) bool {
	return c.app != nil && *c.app == f.app && c.actorType == "app" && c.actor == f.creator && c.appEpoch != nil && *c.appEpoch == f.epoch && f.active && f.scope && bound && f.expires > ms && g.capable && c.policy == g.policy && c.expires > ms && !c.cancel
}
func exactDispatch(ctx context.Context, tx pgx.Tx, h LeaseHandle, id, attempt string) (bool, error) {
	var exact bool
	e := tx.QueryRow(ctx, `SELECT attempt=$3 AND dispatch_session=$4 AND dispatch_owner=$5 AND dispatch_fence=$6 AND dispatch_recovery_epoch=$7 AND dispatch_connection=$8 FROM commands WHERE tenant=$1 AND id=$2`, h.Tenant, id, attempt, h.SessionID, h.OwnerInstance, h.Fence, h.RecoveryEpoch, h.ConnectionID).Scan(&exact)
	return exact, e
}
func (s *GatewayStore) AuthorizeSend(ctx context.Context, a SessionAuth, h LeaseHandle, intent ClaimedIntent) error {
	return s.leases.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		row, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
		if e != nil {
			return e
		}
		if row == nil || !sameHandle(row.LeaseHandle, h) || row.state != "active" {
			return admissionError("STALE_LEASE")
		}
		if e = lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
			return e
		}
		f, e := lockExecutionApp(ctx, tx, h.Tenant, h.DeviceID, intent.CommandID)
		if e != nil {
			return e
		}
		row, g, e := s.currentGateway(ctx, tx, a, h)
		if e != nil {
			return e
		}
		bound, e := lockExecutionBinding(ctx, tx, h.Tenant, h.DeviceID, f)
		if e != nil {
			return e
		}
		c, e := readCommand(ctx, tx, h.Tenant, intent.CommandID)
		if e != nil {
			return e
		}
		exact, e := exactDispatch(ctx, tx, h, c.id, intent.AttemptID)
		if e != nil {
			return e
		}
		if !exact || c.status != "delivered" || c.version != intent.CommandVersion || c.digest != intent.PayloadDigest || c.result != nil {
			return admissionError("STALE_COMMAND")
		}
		if e = checkF0(ctx, tx, h.Tenant, c.id, false); e != nil {
			return e
		}
		ms, e := s.gatewayDecision(ctx, tx, row, g)
		if e != nil {
			return e
		}
		if !allowedExecution(c, f, g, bound, ms) {
			return admissionError("POLICY_DENIED")
		}
		return nil
	})
}

type ConnectionState struct {
	PolicyVersion    int64
	ServerTimeMillis int64
}

func (s *GatewayStore) ConnectionState(ctx context.Context, a SessionAuth, h LeaseHandle) (ConnectionState, error) {
	var out ConnectionState
	e := s.leases.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		row, g, e := s.currentGateway(ctx, tx, a, h)
		if e != nil {
			return e
		}
		ms, e := s.gatewayDecision(ctx, tx, row, g)
		if e != nil {
			return e
		}
		out = ConnectionState{g.policy, ms}
		return nil
	})
	if e != nil {
		return ConnectionState{}, e
	}
	return out, nil
}

// PolicyStop records a bounded stop intent for a current attempt without ever
// claiming that revocation proves no effect. Results remain independently ingestible.
func (s *GatewayStore) PolicyStop(ctx context.Context, a SessionAuth, h LeaseHandle) (ReceiptReply, error) {
	var out ReceiptReply
	e := s.leases.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		row, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
		if e != nil {
			return e
		}
		if row == nil || row.state != "active" || !sameHandle(row.LeaseHandle, h) {
			return admissionError("STALE_LEASE")
		}
		if e = lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
			return e
		}
		var id string
		e = tx.QueryRow(ctx, `SELECT id FROM commands WHERE tenant=$1 AND device=$2 AND status IN ('delivered','accepted','running') ORDER BY id LIMIT 1`, h.Tenant, h.DeviceID).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		ef, e := lockExecutionApp(ctx, tx, h.Tenant, h.DeviceID, id)
		if e != nil {
			return e
		}
		row, g, e := s.currentGateway(ctx, tx, a, h)
		if e != nil {
			return e
		}
		bound, e := lockExecutionBinding(ctx, tx, h.Tenant, h.DeviceID, ef)
		if e != nil {
			return e
		}
		c, e := readCommand(ctx, tx, h.Tenant, id)
		if e != nil {
			return e
		}
		if c.attempt == nil {
			return admissionError("INTEGRITY_CONFLICT")
		}
		exact, e := exactDispatch(ctx, tx, h, id, *c.attempt)
		if e != nil {
			return e
		}
		if !exact {
			return nil
		}
		ms, e := s.gatewayDecision(ctx, tx, row, g)
		if e != nil {
			return e
		}
		if allowedExecution(c, ef, g, bound, ms) {
			return nil
		}
		out, e = requestStop(ctx, tx, h, c, ms)
		return e
	})
	if e != nil {
		return ReceiptReply{}, e
	}
	return out, nil
}
