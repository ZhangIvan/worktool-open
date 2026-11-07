package pg

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"strings"
)

type leaseCommand struct {
	id, device, actor, actorType, capability, digest, status, admission, snapshotDigest, effect string
	app, payload, attempt, result                                                               *string
	appEpoch                                                                                    *int64
	policy, created, expires, version                                                           int64
	confirm, cancel, dispatchEmpty                                                              bool
}

func readCommand(ctx context.Context, tx pgx.Tx, tenant, id string) (leaseCommand, error) {
	var c leaseCommand
	e := tx.QueryRow(ctx, `SELECT id,device,actor,actor_type,capability,digest,status,admission_version,subscription_snapshot_digest,effect,app_id,payload,attempt,result,app_epoch,policy,created,expires,command_version,confirm,cancel_requested,(dispatch_session IS NULL AND dispatch_owner IS NULL AND dispatch_fence IS NULL AND dispatch_recovery_epoch IS NULL AND dispatch_connection IS NULL) FROM commands WHERE tenant=$1 AND id=$2 FOR UPDATE`, tenant, id).Scan(&c.id, &c.device, &c.actor, &c.actorType, &c.capability, &c.digest, &c.status, &c.admission, &c.snapshotDigest, &c.effect, &c.app, &c.payload, &c.attempt, &c.result, &c.appEpoch, &c.policy, &c.created, &c.expires, &c.version, &c.confirm, &c.cancel, &c.dispatchEmpty)
	return c, e
}
func checkAdmissionGraph(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) (string, error) {
	return checkAdmissionGraphState(ctx, tx, h, c, "held")
}
func checkAdmissionGraphState(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand, ledgerState string) (string, error) {
	if ledgerState != "held" && ledgerState != "materialized" {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	bad := admissionError("INTEGRITY_CONFLICT")
	if c.payload == nil || c.app == nil || c.appEpoch == nil || c.confirm || c.admission != "bounded-result-v1" || c.created < 0 || c.expires < c.created || c.expires > maxWireMillis || c.policy < 1 || c.policy > maxSafeInteger {
		return "", bad
	}
	delta := c.expires - c.created
	if delta%1000 != 0 || delta < 30000 || delta > 300000 {
		return "", bad
	}
	var p struct {
		Target string `json:"targetId"`
		Text   string `json:"text"`
	}
	dec := json.NewDecoder(strings.NewReader(*c.payload))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&p); e != nil {
		return "", bad
	}
	payload, e := canonicalRestricted(map[string]any{"targetId": p.Target, "text": p.Text})
	if e != nil || payload != *c.payload {
		return "", bad
	}
	r := AppTextRequest{"internal-placeholder", h.DeviceID, p.Target, p.Text, "synthetic", 203, delta / 1000}
	request, e := requestCanonical(r)
	if e != nil {
		return "", bad
	}
	expires, e := wireTime(c.expires)
	if e != nil {
		return "", bad
	}
	command, e := commandCanonical(r, c.policy, expires)
	if e != nil || hashText(command) != c.digest {
		return "", bad
	}
	var requestHash string
	e = tx.QueryRow(ctx, `SELECT request_hash FROM developer_messages WHERE tenant=$1 AND app=$2 AND command=$3 FOR SHARE`, h.Tenant, *c.app, c.id).Scan(&requestHash)
	if e != nil {
		return "", e
	}
	if requestHash != hashText(request) {
		return "", bad
	}
	var results, total int
	var valid bool
	e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='result'),count(*),COALESCE(bool_and(state=$3 AND event_ordinal=0 AND reserved_jobs=1 AND ((kind='result' AND reserved_bytes=196608) OR (kind='callback' AND reserved_bytes=90112))),false) FROM admission_reservations WHERE tenant=$1 AND command=$2`, h.Tenant, c.id, ledgerState).Scan(&results, &total, &valid)
	if e != nil {
		return "", e
	}
	if results != 1 || total < 1 || total > 5 || !valid {
		return "", bad
	}
	rows, e := tx.Query(ctx, `SELECT s.app,s.subscription_id,s.destination_id,s.event_type,s.event_schema_version,s.version,s.destination_policy_version,s.key_version,s.max_events_per_child,s.max_body_bytes,s.max_attempts FROM admission_reservations r JOIN callback_subscription_versions s ON s.tenant=r.tenant AND s.app=r.app AND s.subscription_id=r.subscription_id AND s.version=r.subscription_version WHERE r.tenant=$1 AND r.command=$2 AND r.kind='callback' ORDER BY s.destination_id COLLATE "C" FOR SHARE OF r,s`, h.Tenant, c.id)
	if e != nil {
		return "", e
	}
	subs := []subscription{}
	for rows.Next() {
		var v subscription
		if e = rows.Scan(&v.App, &v.ID, &v.Destination, &v.Event, &v.Schema, &v.Version, &v.Policy, &v.Key, &v.Events, &v.Body, &v.Attempts); e != nil {
			rows.Close()
			return "", e
		}
		subs = append(subs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return "", e
	}
	if len(subs) != total-1 {
		return "", bad
	}
	for _, v := range subs {
		if v.App != *c.app {
			return "", bad
		}
	}
	snap, e := snapshotCanonical(h.Tenant, *c.app, subs)
	if e != nil || hashText(snap) != c.snapshotDigest {
		return "", bad
	}
	arr, e := canonicalRestricted(snapshotValues(subs))
	if e != nil {
		return "", e
	}
	var equal bool
	e = tx.QueryRow(ctx, `SELECT subscription_snapshot=$3::jsonb FROM commands WHERE tenant=$1 AND id=$2`, h.Tenant, c.id, arr).Scan(&equal)
	if e != nil {
		return "", e
	}
	if !equal {
		return "", bad
	}
	return expires, nil
}
func (s *LeaseStore) ClaimReadyCommand(ctx context.Context, a GatewayAuthority, h LeaseHandle, q ClaimCommandRequest) (ClaimOutcome, error) {
	return s.claim(ctx, a, h, q, false)
}
func (s *LeaseStore) claim(ctx context.Context, a GatewayAuthority, h LeaseHandle, q ClaimCommandRequest, f0 bool) (ClaimOutcome, error) {
	var out ClaimOutcome
	if !validHandle(h) || !authorityHandle(a, h) || !asciiIdentifier(q.CommandID) || q.ExpectedCommandVersion < 0 {
		return out, admissionError("INVALID_INPUT")
	}
	e := s.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		row, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
		if e != nil {
			return e
		}
		if e = lockRuntime(ctx, tx, a.runtime, "gateway"); e != nil {
			return e
		}
		if row == nil || row.state != "active" || !sameHandle(row.LeaseHandle, h) {
			return admissionError("STALE_LEASE")
		}
		// Unlocked hint only selects the app lock; all fields are re-read under the command lock.
		var app *string
		var actorType, capability string
		e = tx.QueryRow(ctx, `SELECT app_id,actor_type,capability FROM commands WHERE tenant=$1 AND device=$2 AND id=$3`, h.Tenant, h.DeviceID, q.CommandID).Scan(&app, &actorType, &capability)
		if errors.Is(e, pgx.ErrNoRows) {
			return admissionError("COMMAND_NOT_FOUND")
		}
		if e != nil {
			return e
		}
		if app == nil || actorType != "app" || capability != "message.send_text" {
			out.Reason = "UNSUPPORTED_COMMAND"
			return nil
		}
		var creator string
		var epoch, appExpires int64
		var active, scope bool
		e = tx.QueryRow(ctx, `SELECT creator,auth_epoch,token_expires,active,scopes ? 'commands:write' FROM developer_apps WHERE tenant=$1 AND id=$2 FOR SHARE`, h.Tenant, *app).Scan(&creator, &epoch, &appExpires, &active, &scope)
		if e != nil {
			return e
		}
		f, _, e := lockGateway(ctx, tx, a, row)
		if e != nil {
			return e
		}
		var binding string
		e = tx.QueryRow(ctx, `SELECT device FROM developer_bindings WHERE tenant=$1 AND app=$2 AND device=$3 FOR SHARE`, h.Tenant, *app, h.DeviceID).Scan(&binding)
		bound := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		c, e := readCommand(ctx, tx, h.Tenant, q.CommandID)
		if e != nil {
			return e
		}
		if c.device != h.DeviceID || c.app == nil || *c.app != *app || c.actorType != actorType || c.capability != capability {
			return admissionError("INTEGRITY_CONFLICT")
		}
		if c.version != q.ExpectedCommandVersion {
			return admissionError("COMMAND_VERSION_CONFLICT")
		}
		if c.status != "queued" {
			out.Reason = "NOT_QUEUED"
			return nil
		}
		if c.attempt != nil || c.result != nil || !c.dispatchEmpty || c.effect != "not_started" {
			return admissionError("INTEGRITY_CONFLICT")
		}
		var occupied bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM commands WHERE tenant=$1 AND device=$2 AND status IN ('delivered','accepted','running','execution_unknown'))`, h.Tenant, h.DeviceID).Scan(&occupied)
		if e != nil {
			return e
		}
		if occupied {
			out.Reason = "DEVICE_RECONCILIATION_REQUIRED"
			return nil
		}
		if f0 {
			if e = checkF0(ctx, tx, h.Tenant, c.id, false); e != nil {
				return e
			}
		}
		expires, e := checkAdmissionGraph(ctx, tx, h, c)
		if e != nil {
			return e
		}
		// The existing intent row can be locked by a concurrent DB consumer.
		// Obtain it before the final authorization/expiry decision, not while
		// performing the later state update. The schema unique key fixes one row.
		var outboxID int64
		e = tx.QueryRow(ctx, `SELECT id FROM outbox WHERE tenant=$1 AND device=$2 AND command=$3 AND kind='command_ready' AND state='pending' FOR UPDATE`, h.Tenant, h.DeviceID, c.id).Scan(&outboxID)
		if e != nil {
			return e
		}
		now, ms, e := s.now(ctx, tx)
		if e != nil {
			return e
		}
		if e = liveGateway(f, ms); e != nil {
			return e
		}
		if !row.LeaseUntil.After(now) {
			return admissionError("LEASE_EXPIRED")
		}
		if c.cancel {
			out.Reason = "CANCEL_REQUESTED"
			return nil
		}
		if c.expires <= ms {
			out.Reason = "EXPIRED"
			return nil
		}
		if appExpires < 0 || appExpires > maxSafeInteger || epoch < 0 || epoch > maxSafeInteger {
			return admissionError("INTEGRITY_CONFLICT")
		}
		if !active || !scope || !bound || !f.capable || c.appEpoch == nil || *c.appEpoch != epoch || c.actor != creator || c.policy != f.policy || appExpires <= ms {
			out.Reason = "POLICY_DENIED"
			return nil
		}
		if c.version == math.MaxInt64 {
			return admissionError("FENCE_EXHAUSTED")
		}
		attempt, e := freshID("attempt_")
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE commands SET status='delivered',attempt=$4,command_version=command_version+1,dispatch_session=$5,dispatch_owner=$6,dispatch_fence=$7,dispatch_recovery_epoch=$8,dispatch_connection=$9 WHERE tenant=$1 AND device=$2 AND id=$3 AND command_version=$10 AND status='queued' AND attempt IS NULL AND result IS NULL AND dispatch_session IS NULL`, h.Tenant, h.DeviceID, c.id, attempt, h.SessionID, h.OwnerInstance, h.Fence, h.RecoveryEpoch, h.ConnectionID, c.version)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		if e = commandTransition(ctx, tx, h, c.id, "queued", "dispatch", "delivered", ms); e != nil {
			return e
		}
		tag, e = tx.Exec(ctx, `UPDATE outbox SET state='claimed' WHERE tenant=$1 AND device=$2 AND command=$3 AND kind='command_ready' AND state='pending'`, h.Tenant, h.DeviceID, c.id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		current := row.LeaseHandle
		out.Intent = &ClaimedIntent{c.id, attempt, c.version + 1, current, c.digest, *c.payload, c.policy, false, expires}
		return nil
	})
	if e != nil {
		return ClaimOutcome{}, e
	}
	return out, nil
}
