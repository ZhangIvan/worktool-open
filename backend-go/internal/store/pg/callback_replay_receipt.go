package pg

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Private first-result evidence, not an authorization token, signed attestation,
// compacted replay path, or permission to reclaim any retained graph or charge.
const callbackReplayReceiptLimit = 16384

type callbackReplayBinding struct {
	reservation, delivery, event, app, subscription, destination, bodyDigest string
	version, policy, key, ordinal                                            int64
	attempts                                                                 int
}

type callbackReplayFacts struct {
	tenant, device, command, app, attempt, commandDigest, snapshotDigest string
	resultDigest, status, effect, dispatchSession, resultReservation     string
	batch, message, child, requestHash                                   string
	appEpoch, dispatchEpoch                                              int64
	bindings                                                             []callbackReplayBinding
}

func replayHash(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Input is private DB-derived metadata only. Stable ordering is required, not
// repaired here: duplicate or reordered destination identities fail closed.
func canonicalCallbackReplayReceipt(f callbackReplayFacts) (string, error) {
	bad := admissionError("INTEGRITY_CONFLICT")
	for _, id := range []string{f.tenant, f.device, f.command, f.attempt, f.dispatchSession, f.resultReservation, f.batch, f.message, f.child} {
		if !asciiIdentifier(id) || len(id) > 128 {
			return "", bad
		}
	}
	if !boundedID(f.app) {
		return "", bad
	}
	for _, digest := range []string{f.commandDigest, f.snapshotDigest, f.resultDigest, f.requestHash} {
		if !replayHash(digest) {
			return "", bad
		}
	}
	if f.appEpoch < 0 || f.appEpoch > maxSafeInteger || f.dispatchEpoch < 0 || f.dispatchEpoch > maxSafeInteger || len(f.bindings) > 4 {
		return "", bad
	}
	// Reuse the established status/effect pairing contract without retaining body.
	if _, err := callbackEventBody("receipt_validation", f.batch, f.message, f.child, f.command, f.status, f.effect); err != nil {
		return "", bad
	}
	bindings := make([]any, 0, len(f.bindings))
	seenReservations, seenDeliveries := map[string]bool{}, map[string]bool{}
	previous, event := "", ""
	for _, b := range f.bindings {
		for _, id := range []string{b.reservation, b.delivery, b.event} {
			if !asciiIdentifier(id) || len(id) > 128 {
				return "", bad
			}
		}
		if !boundedID(b.app) || !boundedID(b.subscription) || !boundedID(b.destination) {
			return "", bad
		}
		if b.app != f.app || b.destination <= previous || seenReservations[b.reservation] || seenDeliveries[b.delivery] || b.reservation == f.resultReservation ||
			b.version < 1 || b.version > maxSafeInteger || b.policy < 1 || b.policy > maxSafeInteger || b.key < 1 || b.key > maxSafeInteger || b.attempts < 1 || b.attempts > 4 || b.ordinal != 0 || !replayHash(b.bodyDigest) {
			return "", bad
		}
		if event != "" && b.event != event {
			return "", bad
		}
		event, previous = b.event, b.destination
		seenReservations[b.reservation], seenDeliveries[b.delivery] = true, true
		bindings = append(bindings, map[string]any{"reservation": b.reservation, "delivery": b.delivery, "event": b.event, "app": b.app, "subscription": b.subscription, "destination": b.destination, "version": b.version, "policy": b.policy, "key": b.key, "attempts": int64(b.attempts), "ordinal": b.ordinal, "bodyDigest": b.bodyDigest})
	}
	encoded, err := canonicalRestricted(map[string]any{
		"schema": "callback-result-replay-v1", "tenant": f.tenant, "device": f.device, "command": f.command, "app": f.app, "appEpoch": f.appEpoch,
		"attempt": f.attempt, "commandDigest": f.commandDigest, "snapshotDigest": f.snapshotDigest,
		"resultDigest": f.resultDigest, "status": f.status, "effect": f.effect,
		"dispatchSession": f.dispatchSession, "dispatchEpoch": f.dispatchEpoch,
		"resultReservation": f.resultReservation, "batch": f.batch, "message": f.message, "child": f.child, "requestHash": f.requestHash, "callbacks": bindings,
	})
	if err != nil {
		return "", bad
	}
	if len(encoded) > callbackReplayReceiptLimit {
		return "", admissionError("RESOURCE_LIMIT")
	}
	return encoded, nil
}

// SQL bounds all transferred identifier metadata, including corrupt rows. This
// does not bound PostgreSQL scan/planning work or claim a physical memory limit.
func replayIDSQL(column string) string {
	return "CASE WHEN octet_length(" + column + ") BETWEEN 1 AND 128 THEN " + column + " ELSE '' END"
}

var callbackReplayOriginSQL = `SELECT ` + replayIDSQL("c.dispatch_session") + `,s.epoch FROM commands c JOIN device_sessions s ON s.tenant=c.tenant AND s.device=c.device AND s.id=c.dispatch_session WHERE c.tenant=$1 AND c.id=$2 AND c.device=$3 FOR SHARE OF s`
var callbackReplayMessageSQL = `SELECT ` + replayIDSQL("batch") + `,` + replayIDSQL("message") + `,` + replayIDSQL("child") + `,CASE WHEN octet_length(request_hash)=64 THEN request_hash ELSE '' END FROM developer_messages WHERE tenant=$1 AND app=$2 AND command=$3 FOR SHARE`
var callbackReplayResultSQL = `SELECT ` + replayIDSQL("reservation_id") + ` FROM admission_reservations WHERE tenant=$1 AND command=$2 AND kind='result' AND state='materialized' AND reserved_jobs=1 AND reserved_bytes=196608 FOR SHARE`
var callbackReplayBindingsSQL = `SELECT ` + strings.Join([]string{replayIDSQL("r.reservation_id"), replayIDSQL("r.delivery_id"), replayIDSQL("r.event_id"), replayIDSQL("r.app"), replayIDSQL("r.subscription_id"), replayIDSQL("r.destination_id")}, ",") + `,r.subscription_version,r.destination_policy_version,r.key_version,r.max_attempts,r.event_ordinal,CASE WHEN octet_length(o.payload_digest)=64 THEN o.payload_digest ELSE '' END FROM admission_reservations r JOIN callback_outbox o ON o.tenant=r.tenant AND o.reservation_id=r.reservation_id AND o.delivery_id=r.delivery_id AND o.event_id=r.event_id AND o.destination_id=r.destination_id AND o.command=r.command WHERE r.tenant=$1 AND r.command=$2 AND r.kind='callback' AND r.state='materialized' ORDER BY r.destination_id COLLATE "C" LIMIT 5 FOR SHARE OF r,o`

// Invoked only after the existing full graph validators, inside the same locked
// result transaction. Current authentication is supplied by ApplyDeviceFrame;
// dispatchSession is the ORIGINAL command provenance, never h.SessionID.
func callbackReplayReceiptFromGraph(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) (string, error) {
	if c.app == nil || c.appEpoch == nil || c.attempt == nil || c.result == nil || c.device != h.DeviceID || c.actorType != "app" || c.capability != "message.send_text" || c.admission != "bounded-result-v1" {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	f := callbackReplayFacts{tenant: h.Tenant, device: c.device, command: c.id, app: *c.app, appEpoch: *c.appEpoch, attempt: *c.attempt, commandDigest: c.digest, snapshotDigest: c.snapshotDigest, resultDigest: hashText(*c.result), status: c.status, effect: c.effect}
	if err := tx.QueryRow(ctx, callbackReplayOriginSQL, h.Tenant, c.id, c.device).Scan(&f.dispatchSession, &f.dispatchEpoch); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, callbackReplayMessageSQL, h.Tenant, *c.app, c.id).Scan(&f.batch, &f.message, &f.child, &f.requestHash); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, callbackReplayResultSQL, h.Tenant, c.id).Scan(&f.resultReservation); err != nil {
		return "", err
	}
	rows, err := tx.Query(ctx, callbackReplayBindingsSQL, h.Tenant, c.id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		if len(f.bindings) == 4 {
			return "", admissionError("INTEGRITY_CONFLICT")
		}
		var b callbackReplayBinding
		if err = rows.Scan(&b.reservation, &b.delivery, &b.event, &b.app, &b.subscription, &b.destination, &b.version, &b.policy, &b.key, &b.attempts, &b.ordinal, &b.bodyDigest); err != nil {
			return "", err
		}
		f.bindings = append(f.bindings, b)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	rows.Close()
	// Reject a dropped JOIN parent instead of producing a shorter plausible receipt.
	var complete bool
	if err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM admission_reservations WHERE tenant=$1 AND command=$2 AND kind='callback')=$3 AND (SELECT count(*) FROM callback_outbox WHERE tenant=$1 AND command=$2)=$3 AND (SELECT count(*) FROM developer_callbacks WHERE tenant=$1 AND command=$2)=$4`, h.Tenant, c.id, len(f.bindings), min(len(f.bindings), 1)).Scan(&complete); err != nil {
		return "", err
	}
	if !complete {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	return canonicalCallbackReplayReceipt(f)
}

func verifyCallbackReplayReceipt(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand) error {
	expected, err := callbackReplayReceiptFromGraph(ctx, tx, h, c)
	if err != nil {
		return err
	}
	var stored string
	err = tx.QueryRow(ctx, `SELECT CASE WHEN octet_length(receipt)<=16384 THEN receipt ELSE '' END FROM callback_replay_receipts WHERE tenant=$1 AND command=$2 FOR SHARE`, h.Tenant, c.id).Scan(&stored)
	if err != nil {
		return err
	}
	if stored != expected {
		return admissionError("INTEGRITY_CONFLICT")
	}
	return nil
}

func persistCallbackReplayReceipt(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand, projectionBytes int) error {
	receipt, err := callbackReplayReceiptFromGraph(ctx, tx, h, c)
	if err != nil {
		return err
	}
	// Same logical reservation, now explicitly charges the additional receipt.
	// Subtraction avoids integer overflow; this does not assert physical PG size.
	if projectionBytes < 0 || projectionBytes > 196608-32768-len(receipt) {
		return admissionError("RESOURCE_LIMIT")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO callback_replay_receipts(tenant,command,receipt) VALUES($1,$2,$3)`, h.Tenant, c.id, receipt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return admissionError("INTEGRITY_CONFLICT")
	}
	return nil
}
