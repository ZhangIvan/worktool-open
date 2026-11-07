package pg

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/jackc/pgx/v5"
)

type SessionGrant struct {
	SessionID, AccessToken, ExpiresAt string
	RevocationEpoch                   int64
}

func (SessionGrant) String() string { return "SessionGrant(redacted)" }
func (s *GatewayStore) RotateDeviceSession(ctx context.Context, device, key, secret string) (SessionGrant, error) {
	var out SessionGrant
	if !wireID(device) || !wireID(key) || !bearerSecret(secret) {
		return out, admissionError("UNAUTHENTICATED")
	}
	raw, e := randomSecret()
	if e != nil {
		return out, e
	}
	id, e := freshID("session_")
	if e != nil {
		return out, e
	}
	e = s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var tenant string
		e := tx.QueryRow(ctx, `SELECT tenant FROM devices WHERE id=$1`, device).Scan(&tenant)
		if errors.Is(e, pgx.ErrNoRows) {
			return admissionError("UNAUTHENTICATED")
		}
		if e != nil {
			return e
		}
		b, e := lockScope(ctx, tx, tenant, "tenant", tenant)
		if e != nil {
			return e
		}
		if !b.enabled {
			return admissionError("UNAUTHENTICATED")
		}
		if _, e = readLease(ctx, tx, tenant, device); e != nil {
			return e
		}
		if e = lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
			return e
		}
		var fp, keyID, state string
		var epoch int64
		e = tx.QueryRow(ctx, `SELECT secret,key_id,state,epoch FROM devices WHERE tenant=$1 AND id=$2 FOR UPDATE`, tenant, device).Scan(&fp, &keyID, &state, &epoch)
		if e != nil {
			return e
		}
		if state != "active" || keyID != key || epoch < 0 || epoch > maxSafeInteger || subtle.ConstantTimeCompare([]byte(fp), []byte(hashText(secret))) != 1 {
			return admissionError("UNAUTHENTICATED")
		}
		rows, e := tx.Query(ctx, `SELECT id FROM device_sessions WHERE tenant=$1 AND device=$2 AND NOT revoked ORDER BY id COLLATE "C" FOR UPDATE`, tenant, device)
		if e != nil {
			return e
		}
		for rows.Next() {
			var sid string
			if e = rows.Scan(&sid); e != nil {
				rows.Close()
				return e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		expires := ms + 300000
		if expires > maxWireMillis {
			return admissionError("INVALID_INPUT")
		}
		_, e = tx.Exec(ctx, `UPDATE device_sessions SET revoked=true WHERE tenant=$1 AND device=$2 AND NOT revoked`, tenant, device)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO device_sessions(id,tenant,device,token,epoch,expires) VALUES($1,$2,$3,$4,$5,$6)`, id, tenant, device, hashText(raw), epoch, expires)
		if e != nil {
			return e
		}
		if e = auditGateway(ctx, tx, tenant, device, "device.session_created", id, "created", ms); e != nil {
			return e
		}
		at, e := wireTime(expires)
		if e != nil {
			return e
		}
		out = SessionGrant{id, raw, at, epoch}
		return nil
	})
	if e != nil {
		return SessionGrant{}, e
	}
	return out, nil
}
func (s *GatewayStore) ConsumeWebSocketSession(ctx context.Context, a SessionAuth, connection string) (SessionAuth, error) {
	if !a.a.trusted || !wireID(connection) {
		return SessionAuth{}, admissionError("UNAUTHENTICATED")
	}
	out := a
	e := s.leases.run(ctx, a.a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		old, e := readLease(ctx, tx, a.a.tenant, a.a.device)
		if e != nil {
			return e
		}
		if e = lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
			return e
		}
		f, _, e := lockGateway(ctx, tx, a.a, old)
		if e != nil {
			return e
		}
		var consumed bool
		e = tx.QueryRow(ctx, `SELECT ws_consumed FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=$3`, a.a.tenant, a.a.device, a.a.session).Scan(&consumed)
		if e != nil {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		if e = liveGateway(f, ms); e != nil {
			return e
		}
		if consumed {
			return admissionError("SESSION_ALREADY_USED")
		}
		tag, e := tx.Exec(ctx, `UPDATE device_sessions SET ws_consumed=true,ws_connection_id=$4 WHERE tenant=$1 AND device=$2 AND id=$3 AND NOT ws_consumed`, a.a.tenant, a.a.device, a.a.session, connection)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("SESSION_ALREADY_USED")
		}
		out.consumed = true
		out.connection = connection
		out.expires = f.expires
		return auditGateway(ctx, tx, a.a.tenant, a.a.device, "device.ws_consumed", a.a.session, "consumed", ms)
	})
	if e != nil {
		return SessionAuth{}, e
	}
	return out, nil
}
func (s *GatewayStore) AcquireConnection(ctx context.Context, a SessionAuth) (LeaseHandle, error) {
	if !a.a.trusted || !a.consumed || !wireID(a.connection) {
		return LeaseHandle{}, admissionError("UNAUTHENTICATED")
	}
	return s.leases.acquireConsumed(ctx, a.a, a.connection)
}
func (s *GatewayStore) RenewConnection(ctx context.Context, a SessionAuth, h LeaseHandle) (LeaseHandle, error) {
	if !a.consumed || a.connection != h.ConnectionID {
		return LeaseHandle{}, admissionError("UNAUTHENTICATED")
	}
	return s.leases.RenewDeviceLease(ctx, a.a, h)
}
func (s *GatewayStore) CloseConnection(ctx context.Context, a SessionAuth, h LeaseHandle) (RetireOutcome, error) {
	if !a.consumed || a.connection != h.ConnectionID || !authorityHandle(a.a, h) {
		return RetireOutcome{}, admissionError("UNAUTHENTICATED")
	}
	return s.leases.CloseExactSession(ctx, s.runtime, h)
}
