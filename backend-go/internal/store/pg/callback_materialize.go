package pg

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

// NewSyntheticCallbackGatewayStore is trusted, explicit synthetic composition.
// The default constructor remains F0. This does not install a worker, key
// provider, endpoint or production callback transport.
func NewSyntheticCallbackGatewayStore(r *Runner, gate func(context.Context) error, runtime RuntimeAuthority, ttl time.Duration) (*GatewayStore, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, admissionError("INVALID_INPUT")
	}
	s, e := NewGatewayStore(r, gate, runtime)
	if e != nil {
		return nil, e
	}
	s.callbackTTL = ttl
	return s, nil
}
func (s *GatewayStore) checkCallbackCommand(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand, materializedOK bool) error {
	if s.callbackTTL == 0 {
		return checkF0(ctx, tx, h.Tenant, c.id, materializedOK)
	}
	state := "held"
	if c.result != nil {
		if !materializedOK {
			return admissionError("INTEGRITY_CONFLICT")
		}
		state = "materialized"
	}
	_, e := checkAdmissionGraphState(ctx, tx, h, c, state)
	return e
}

type callbackReservation struct {
	id, app, sub, destination, state string
	version, policy, key             int64
	attempts                         int
	active                           bool
}

// Called under the tenant gate, BEFORE the physical lease/app/device locks.
// Configuration writers must take that same gate. Hints are subsequently
// compared to the locked immutable reservation graph.
func (s *GatewayStore) callbackScopes(ctx context.Context, tx pgx.Tx, h LeaseHandle, id string) error {
	if s.callbackTTL == 0 || id == "" {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT DISTINCT destination_id COLLATE "C" FROM admission_reservations WHERE tenant=$1 AND command=$2 AND kind='callback' ORDER BY destination_id COLLATE "C" LIMIT 5`, h.Tenant, id)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) > 4 {
		return admissionError("INTEGRITY_CONFLICT")
	}
	for _, d := range ids {
		var exact string
		if e = tx.QueryRow(ctx, callbackLockDestination, h.Tenant, d).Scan(&exact); e != nil {
			return e
		}
		if exact != d {
			return admissionError("INTEGRITY_CONFLICT")
		}
	}
	return nil
}
func callbackReservations(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) ([]callbackReservation, error) {
	rows, e := tx.Query(ctx, `SELECT reservation_id,app,subscription_id,destination_id,state,subscription_version,destination_policy_version,key_version,max_attempts FROM admission_reservations WHERE tenant=$1 AND command=$2 AND kind='callback' ORDER BY destination_id COLLATE "C" LIMIT 5 FOR UPDATE`, h.Tenant, c.id)
	if e != nil {
		return nil, e
	}
	var out []callbackReservation
	for rows.Next() {
		var v callbackReservation
		if e = rows.Scan(&v.id, &v.app, &v.sub, &v.destination, &v.state, &v.version, &v.policy, &v.key, &v.attempts); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(out) > 4 {
		return nil, admissionError("INTEGRITY_CONFLICT")
	}
	return out, nil
}

// Data projection is independent of the HTTP payload/text. Fixed keys and
// constrained identifiers are canonicalized once, and those exact bytes persist.
func callbackEventBody(event, batch, message, child, id, status, effect string) (string, error) {
	for _, v := range []string{event, batch, message, child, id} {
		if !asciiIdentifier(v) || len(v) > 128 {
			return "", admissionError("INTEGRITY_CONFLICT")
		}
	}
	switch status {
	case "succeeded", "failed", "cancelled", "expired", "rejected", "execution_unknown", "partially_succeeded":
	default:
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	paired := (status == "succeeded" && effect == "applied") || (status == "partially_succeeded" && effect == "partially_applied") || (status == "execution_unknown" && effect == "unknown") || ((status == "failed" || status == "cancelled") && (effect == "not_applied" || effect == "not_started")) || ((status == "expired" || status == "rejected") && effect == "not_started")
	if !paired {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	b, e := canonicalRestricted(map[string]any{"eventId": event, "eventType": "command.result", "eventSchemaVersion": "bounded-result-v1", "mode": "synthetic", "batchId": batch, "messageId": message, "childId": child, "commandId": id, "status": status, "effectState": effect})
	if e != nil {
		return "", e
	}
	if len(b) > 4096 {
		return "", admissionError("RESOURCE_LIMIT")
	}
	return b, nil
}
func callbackDispatchEpoch(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) (int64, error) {
	var epoch int64
	e := tx.QueryRow(ctx, `SELECT s.epoch FROM commands c JOIN device_sessions s ON s.tenant=c.tenant AND s.device=c.device AND s.id=c.dispatch_session WHERE c.tenant=$1 AND c.id=$2 AND c.device=$3 FOR SHARE OF s`, h.Tenant, c.id, h.DeviceID).Scan(&epoch)
	if e != nil {
		return 0, e
	}
	if epoch < 0 {
		return 0, admissionError("INTEGRITY_CONFLICT")
	}
	return epoch, nil
}

// Duplicate validation is read-only and does not test current delivery grants.
// It runs before the prior-inbox early return, not just on new inbound event IDs.
func (s *GatewayStore) validateCallbackMaterialized(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) error {
	if s.callbackTTL == 0 {
		return nil
	}
	rs, e := callbackReservations(ctx, tx, h, c)
	if e != nil {
		return e
	}
	if len(rs) == 0 {
		var n int
		e = tx.QueryRow(ctx, `SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND command=$2`, h.Tenant, c.id).Scan(&n)
		if e != nil {
			return e
		}
		if n != 0 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		return nil
	}
	epoch, e := callbackDispatchEpoch(ctx, tx, h, c)
	if e != nil {
		return e
	}
	var event, body, batch, message, child string
	var events int
	e = tx.QueryRow(ctx, `SELECT count(*),COALESCE(min(event),''),COALESCE(min(body),'') FROM developer_callbacks WHERE tenant=$1 AND command=$2 AND app=$3`, h.Tenant, c.id, *c.app).Scan(&events, &event, &body)
	if e != nil {
		return e
	}
	if events != 1 {
		return admissionError("INTEGRITY_CONFLICT")
	}
	e = tx.QueryRow(ctx, `SELECT batch,message,child FROM developer_messages WHERE tenant=$1 AND app=$2 AND command=$3`, h.Tenant, *c.app, c.id).Scan(&batch, &message, &child)
	if e != nil {
		return e
	}
	expected, e := callbackEventBody(event, batch, message, child, c.id, c.status, c.effect)
	if e != nil || body != expected {
		return admissionError("INTEGRITY_CONFLICT")
	}
	var count int
	e = tx.QueryRow(ctx, `SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND command=$2`, h.Tenant, c.id).Scan(&count)
	if e != nil {
		return e
	}
	if count != len(rs) {
		return admissionError("INTEGRITY_CONFLICT")
	}
	for _, r := range rs {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT o.event_id=$3 AND o.app=$4 AND o.device=$5 AND o.app_epoch=$6 AND o.device_epoch=$7 AND o.policy_version=$8 AND o.key_version=$9 AND o.max_attempts=$10 AND o.subscription_id=$11 AND o.subscription_version=$12 AND o.event_ordinal=0 AND o.body_bytes=$13 AND o.payload_digest=$14 AND r.state='materialized' AND r.delivery_id=o.delivery_id AND r.event_id=o.event_id AND o.state IN ('ready','leased','delivered','dead_letter','cancelled') FROM callback_outbox o JOIN admission_reservations r ON r.tenant=o.tenant AND r.reservation_id=o.reservation_id WHERE o.tenant=$1 AND o.reservation_id=$2 AND o.command=$15`, h.Tenant, r.id, event, *c.app, h.DeviceID, *c.appEpoch, epoch, r.policy, r.key, r.attempts, r.sub, r.version, []byte(body), hashText(body), c.id).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return admissionError("INTEGRITY_CONFLICT")
		}
	}
	return nil
}

// Policy prelocks run after the app/device/session authority locks but before
// command/reservation locks. They do not determine whether evidence is trusted.
func callbackPolicyLocks(ctx context.Context, tx pgx.Tx, h LeaseHandle, id string) error {
	rows, e := tx.Query(ctx, `SELECT d.id FROM callback_destinations d JOIN admission_reservations r ON r.tenant=d.tenant AND r.destination_id=d.id AND r.app=d.app WHERE r.tenant=$1 AND r.command=$2 AND r.kind='callback' ORDER BY d.id COLLATE "C" FOR SHARE OF d`, h.Tenant, id)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = tx.Query(ctx, `SELECT v.destination_id FROM callback_destination_versions v JOIN admission_reservations r ON r.tenant=v.tenant AND r.app=v.app AND r.destination_id=v.destination_id AND r.destination_policy_version=v.policy_version AND r.key_version=v.key_version WHERE r.tenant=$1 AND r.command=$2 AND r.kind='callback' ORDER BY v.destination_id COLLATE "C" FOR SHARE OF v`, h.Tenant, id)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	rows, e = tx.Query(ctx, `SELECT s.subscription_id FROM callback_subscription_versions s JOIN admission_reservations r ON r.tenant=s.tenant AND r.app=s.app AND r.subscription_id=s.subscription_id AND r.subscription_version=s.version WHERE r.tenant=$1 AND r.command=$2 AND r.kind='callback' ORDER BY s.destination_id COLLATE "C" FOR SHARE OF s`, h.Tenant, id)
	if e != nil {
		return e
	}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
	}
	e = rows.Err()
	rows.Close()
	return e
}
func (s *GatewayStore) materializeCallbacks(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand, status, effect string, ms int64) error {
	if s.callbackTTL == 0 {
		return nil
	}
	rs, e := callbackReservations(ctx, tx, h, c)
	if e != nil {
		return e
	}
	if len(rs) == 0 {
		return nil
	}
	epoch, e := callbackDispatchEpoch(ctx, tx, h, c)
	if e != nil {
		return e
	}
	var batch, message, child string
	e = tx.QueryRow(ctx, `SELECT batch,message,child FROM developer_messages WHERE tenant=$1 AND app=$2 AND command=$3 FOR SHARE`, h.Tenant, *c.app, c.id).Scan(&batch, &message, &child)
	if e != nil {
		return e
	}
	event, e := freshID("evt_cb_")
	if e != nil {
		return e
	}
	body, e := callbackEventBody(event, batch, message, child, c.id, status, effect)
	if e != nil {
		return e
	}
	if 2*len(body)+32768 > 90112 {
		return admissionError("RESOURCE_LIMIT")
	}
	_, e = tx.Exec(ctx, `INSERT INTO developer_callbacks(event,tenant,app,command,status,body,state,attempts,next_at) VALUES($1,$2,$3,$4,$5,$6,'managed_outbox',0,$7)`, event, h.Tenant, *c.app, c.id, status, body, ms)
	if e != nil {
		return e
	}
	for _, r := range rs {
		if r.state != "held" || r.app != *c.app {
			return admissionError("INTEGRITY_CONFLICT")
		}
		var active bool
		e = tx.QueryRow(ctx, `SELECT a.active AND a.auth_epoch=$4 AND a.token_expires>$5 AND a.scopes ? 'callbacks:read' AND a.scopes ? 'results:read' AND d.state='active' AND d.policy_version=$6 AND d.key_version=$7 AND v.revoked_at IS NULL AND s.destination_policy_version=$6 AND s.key_version=$7 AND s.retired_at IS NULL AND EXISTS(SELECT 1 FROM devices dv WHERE dv.tenant=a.tenant AND dv.id=$8 AND dv.state='active' AND dv.epoch=$11) AND EXISTS(SELECT 1 FROM developer_bindings b WHERE b.tenant=a.tenant AND b.app=a.id AND b.device=$8) FROM developer_apps a JOIN callback_destinations d ON d.tenant=a.tenant AND d.app=a.id JOIN callback_destination_versions v ON v.tenant=d.tenant AND v.app=d.app AND v.destination_id=d.id AND v.policy_version=$6 AND v.key_version=$7 JOIN callback_subscription_versions s ON s.tenant=d.tenant AND s.app=d.app AND s.destination_id=d.id WHERE a.tenant=$1 AND a.id=$2 AND d.id=$3 AND s.subscription_id=$9 AND s.version=$10`, h.Tenant, *c.app, r.destination, *c.appEpoch, ms, r.policy, r.key, h.DeviceID, r.sub, r.version, epoch).Scan(&active)
		if errors.Is(e, pgx.ErrNoRows) {
			active = false
		} else if e != nil {
			return e
		}
		delivery, e := freshID("del_")
		if e != nil {
			return e
		}
		state, reason := "ready", any(nil)
		if !active {
			state = "cancelled"
			reason = "DELIVERY_AUTHORITY_REVOKED"
		}
		tag, e := tx.Exec(ctx, `UPDATE admission_reservations SET state='materialized',delivery_id=$3,event_id=$4,materialized_at=$5 WHERE tenant=$1 AND reservation_id=$2 AND state='held' AND kind='callback'`, h.Tenant, r.id, delivery, event, time.UnixMilli(ms))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		_, e = tx.Exec(ctx, `INSERT INTO callback_outbox(tenant,delivery_id,event_id,destination_id,app,command,device,app_epoch,device_epoch,body_bytes,payload_digest,policy_version,key_version,state,available_at,max_attempts,expires_at,last_error_code,reservation_id,reservation_kind,reservation_state,subscription_id,subscription_version,event_ordinal) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'callback','materialized',$20,$21,0)`, h.Tenant, delivery, event, r.destination, *c.app, c.id, h.DeviceID, *c.appEpoch, epoch, []byte(body), hashText(body), r.policy, r.key, state, time.UnixMilli(ms), r.attempts, time.UnixMilli(ms).Add(s.callbackTTL), reason, r.id, r.sub, r.version)
		if e != nil {
			return e
		}
	}
	return nil
}
