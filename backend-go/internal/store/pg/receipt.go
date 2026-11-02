package pg

import (
	"bytes"
	"context"
	"errors"
	wire "github.com/ZhangIvan/worktool-open/backend-go/internal/protocol/gatewaywire"
	"github.com/jackc/pgx/v5"
	"math"
	"strings"
)

// ReceiptReply is an unnumbered outbound intent. The sole socket writer assigns
// its eventId/seq/time only at dequeue. Empty Kind means no recursive ACK.
type ReceiptReply struct {
	Kind string
	Body wire.ServerBody
}

func ackFor(f wire.DeviceFrame) ReceiptReply {
	if f.Header.Kind == "execution_result" {
		return ReceiptReply{"result_ack", wire.ServerBody{ResultAck: &wire.ResultAck{AckEventID: f.Header.EventID, CommandID: f.Body.Result.CommandID}}}
	}
	if f.Header.Kind == "transport_ack" {
		return ReceiptReply{}
	}
	return ReceiptReply{"transport_ack", wire.ServerBody{TransportAck: &wire.TransportAck{AckEventID: f.Header.EventID}}}
}
func encodedReply(r ReceiptReply) (string, error) {
	var body map[string]any
	switch r.Kind {
	case "result_ack":
		body = map[string]any{"ackEventId": r.Body.ResultAck.AckEventID, "commandId": r.Body.ResultAck.CommandID}
	case "transport_ack":
		body = map[string]any{"ackEventId": r.Body.TransportAck.AckEventID}
	default:
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	return canonicalRestricted(map[string]any{"kind": r.Kind, "body": body})
}
func frameCommand(f wire.DeviceFrame) (string, string) {
	switch f.Header.Kind {
	case "accepted":
		return f.Body.Accepted.CommandID, f.Body.Accepted.AttemptID
	case "started":
		return f.Body.Started.CommandID, f.Body.Started.AttemptID
	case "execution_result":
		return f.Body.Result.CommandID, f.Body.Result.AttemptID
	}
	return "", ""
}
func (s *GatewayStore) persistReceipt(ctx context.Context, tx pgx.Tx, h LeaseHandle, f wire.DeviceFrame, reply ReceiptReply) error {
	var last int64
	if e := tx.QueryRow(ctx, `SELECT ws_recv_seq FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=$3`, h.Tenant, h.DeviceID, h.SessionID).Scan(&last); e != nil {
		return e
	}
	if f.Header.Seq <= last {
		return admissionError("ILLEGAL_SEQUENCE")
	}
	encoded, e := encodedReply(reply)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO inbox(tenant,device,event,digest,session,seq,reply) VALUES($1,$2,$3,$4,$5,$6,$7)`, h.Tenant, h.DeviceID, f.Header.EventID, f.FrameDigest, h.SessionID, f.Header.Seq, encoded)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE device_sessions SET ws_recv_seq=$4 WHERE tenant=$1 AND device=$2 AND id=$3 AND ws_connection_id=$5`, h.Tenant, h.DeviceID, h.SessionID, f.Header.Seq, h.ConnectionID)
	return e
}
func resultEvent(c leaseCommand, r *wire.ExecutionResult, history bool) (string, error) {
	if c.status == "delivered" || c.status == "accepted" {
		if r.EffectState == "not_started" {
			switch r.Status {
			case "rejected":
				return "device_reject_before_start", nil
			case "cancelled":
				return "device_proves_no_effect_cancel", nil
			case "expired":
				return "device_proves_unstarted_expiry", nil
			}
		}
	}
	if c.status == "running" {
		switch r.Status {
		case "succeeded":
			return "result_success", nil
		case "partially_succeeded":
			return "result_partial", nil
		case "failed":
			return "result_failure_no_effect", nil
		case "cancelled":
			return "result_cancel_no_effect", nil
		case "execution_unknown":
			return "lost_execution_evidence", nil
		}
	}
	if c.status == "execution_unknown" {
		switch r.Status {
		case "succeeded":
			return "reconcile_applied", nil
		case "partially_succeeded":
			return "reconcile_partial", nil
		case "failed":
			return "reconcile_not_applied", nil
		case "execution_unknown":
			return "", nil
		case "rejected", "cancelled", "expired":
			refs := len(r.EvidenceRefs) > 0
			for _, ref := range r.EvidenceRefs {
				refs = refs && strings.HasPrefix(ref, "journal_")
			}
			if r.EffectState == "not_started" && refs && !history && c.effect != "applied" && c.effect != "partially_applied" && c.effect != "not_applied" {
				return "reconcile_unstarted_" + r.Status, nil
			}
		}
	}
	return "", admissionError("ILLEGAL_TRANSITION")
}
func requestStop(ctx context.Context, tx pgx.Tx, h LeaseHandle, c leaseCommand, ms int64) (ReceiptReply, error) {
	if !c.cancel {
		if c.version == math.MaxInt64 {
			return ReceiptReply{}, admissionError("FENCE_EXHAUSTED")
		}
		tag, e := tx.Exec(ctx, `UPDATE commands SET cancel_requested=true,command_version=command_version+1 WHERE tenant=$1 AND id=$2 AND command_version=$3 AND NOT cancel_requested`, h.Tenant, c.id, c.version)
		if e != nil {
			return ReceiptReply{}, e
		}
		if tag.RowsAffected() != 1 {
			return ReceiptReply{}, admissionError("INTEGRITY_CONFLICT")
		}
		if e = auditGateway(ctx, tx, h.Tenant, h.OwnerInstance, "command.policy_stop", c.id, "policy_revoked", ms); e != nil {
			return ReceiptReply{}, e
		}
	}
	_, e := tx.Exec(ctx, `INSERT INTO outbox(tenant,device,command,kind,state) VALUES($1,$2,$3,'stop_request','pending') ON CONFLICT(tenant,command,kind) DO NOTHING`, h.Tenant, h.DeviceID, c.id)
	if e != nil {
		return ReceiptReply{}, e
	}
	return ReceiptReply{"stop_request", wire.ServerBody{Stop: &wire.StopBody{CommandID: c.id, AttemptID: *c.attempt, Reason: "policy_revoked", PolicyVersion: c.policy}}}, nil
}

// ApplyDeviceFrame validates raw input itself; callers cannot forge normalized
// body/digests. Gateway's single reader additionally enforces ALL-frame sequence
// continuity and bounded RAM dedupe, including non-durable heartbeat traffic.
func (s *GatewayStore) ApplyDeviceFrame(ctx context.Context, a SessionAuth, h LeaseHandle, raw []byte) (ReceiptReply, error) {
	f, e := wire.DecodeDeviceFrame(raw)
	if e != nil {
		return ReceiptReply{}, admissionError("SCHEMA_INVALID")
	}
	if !a.consumed || a.connection != h.ConnectionID || !authorityHandle(a.a, h) || f.Header.TenantID != h.Tenant || f.Header.DeviceID != h.DeviceID || f.Header.SessionID != h.SessionID {
		return ReceiptReply{}, admissionError("UNAUTHENTICATED")
	}
	id, attempt := frameCommand(f)
	reply := ackFor(f)
	var trustedConflict bool
	e = s.leases.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		// Preserve lease/runtime->app->device/session ordering for execution receipts.
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
		execution := f.Header.Kind == "accepted" || f.Header.Kind == "started"
		var ef executionFacts
		if execution {
			ef, e = lockExecutionApp(ctx, tx, h.Tenant, h.DeviceID, id)
			if e != nil {
				return e
			}
		}
		row, g, e := s.currentGateway(ctx, tx, a, h)
		if e != nil {
			return e
		}
		bound := false
		if execution {
			bound, e = lockExecutionBinding(ctx, tx, h.Tenant, h.DeviceID, ef)
			if e != nil {
				return e
			}
		}
		var c leaseCommand
		if id != "" {
			c, e = readCommand(ctx, tx, h.Tenant, id)
			if errors.Is(e, pgx.ErrNoRows) {
				return admissionError("NOT_FOUND")
			}
			if e != nil {
				return e
			}
			if c.device != h.DeviceID || c.attempt == nil || *c.attempt != attempt || c.actorType != "app" || c.capability != "message.send_text" {
				return admissionError("NOT_FOUND")
			}
			if e = checkF0(ctx, tx, h.Tenant, id, true); e != nil {
				return e
			}
		}
		// Lock any durable prior event before the final clock decision. No query/body
		// can choose another tenant/device or bypass the authenticated attempt check.
		var priorDigest, priorSession string
		var priorSeq int64
		e = tx.QueryRow(ctx, `SELECT digest,session,seq FROM inbox WHERE tenant=$1 AND device=$2 AND event=$3 FOR SHARE`, h.Tenant, h.DeviceID, f.Header.EventID).Scan(&priorDigest, &priorSession, &priorSeq)
		prior := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if f.Header.Kind == "execution_result" && c.result == nil {
			if _, e = checkAdmissionGraph(ctx, tx, h, c); e != nil {
				return e
			}
			// Result reservation is a ledger identity, not a pool of callback capacity.
			var reservation string
			e = tx.QueryRow(ctx, `SELECT reservation_id FROM admission_reservations WHERE tenant=$1 AND command=$2 AND kind='result' AND state='held' AND reserved_jobs=1 AND reserved_bytes=196608 FOR UPDATE`, h.Tenant, id).Scan(&reservation)
			if e != nil {
				return e
			}
			// Lock possible ACK intent before checking time, even if a legacy consumer
			// currently holds it. New insert is protected by the locked command identity.
			rows, e := tx.Query(ctx, `SELECT id FROM outbox WHERE tenant=$1 AND command=$2 AND kind='result_ack' FOR UPDATE`, h.Tenant, id)
			if e != nil {
				return e
			}
			for rows.Next() {
				var n int64
				if e = rows.Scan(&n); e != nil {
					rows.Close()
					return e
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		ms, e := s.gatewayDecision(ctx, tx, row, g)
		if e != nil {
			return e
		}
		sent := f.Header.SentAt.UnixMilli()
		if sent < ms-30000 || sent > ms+30000 {
			return admissionError("EXPIRED")
		}
		if prior {
			if priorDigest != f.FrameDigest || priorSession != h.SessionID || priorSeq != f.Header.Seq {
				trustedConflict = id != "" && f.Header.Kind == "execution_result"
				return admissionError("IDEMPOTENCY_CONFLICT")
			}
			return nil
		}
		if id == "" {
			return nil
		} // advisory frames: bounded by controller, no durable append
		if execution {
			exact, e := exactDispatch(ctx, tx, h, id, attempt)
			if e != nil {
				return e
			}
			if !exact {
				return admissionError("STALE_COMMAND")
			}
			if !allowedExecution(c, ef, g, bound, ms) {
				reply, e = requestStop(ctx, tx, h, c, ms)
				return e
			}
			source, event, target := "delivered", "device_accept", "accepted"
			if f.Header.Kind == "started" {
				source, event, target = "accepted", "begin", "running"
			}
			if c.status == target || (f.Header.Kind == "accepted" && c.status == "running") {
				return nil
			} // same receipt, fresh event: no row growth
			if c.status != source || c.result != nil {
				return admissionError("ILLEGAL_TRANSITION")
			}
			if c.version == math.MaxInt64 {
				return admissionError("FENCE_EXHAUSTED")
			}
			tag, e := tx.Exec(ctx, `UPDATE commands SET status=$4,command_version=command_version+1 WHERE tenant=$1 AND id=$2 AND command_version=$3`, h.Tenant, id, c.version, target)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return admissionError("INTEGRITY_CONFLICT")
			}
			if e = commandTransition(ctx, tx, h, id, source, event, target, ms); e != nil {
				return e
			}
			return s.persistReceipt(ctx, tx, h, f, reply)
		}
		r := f.Body.Result
		if c.result != nil {
			if !bytes.Equal([]byte(*c.result), f.ResultCanonical) {
				trustedConflict = true
				return admissionError("RESULT_CONFLICT")
			}
			return nil
		} // immutable read+current-event ACK, no new inbox/outbox/charge
		var history bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM command_events WHERE tenant=$1 AND command=$2 AND (event='begin' OR source='running' OR target IN ('running','succeeded','partially_succeeded','failed','cancelled','expired','rejected')))`, h.Tenant, id).Scan(&history)
		if e != nil {
			return e
		}
		event, e := resultEvent(c, r, history)
		if e != nil {
			return e
		}
		if c.version == math.MaxInt64 {
			return admissionError("FENCE_EXHAUSTED")
		}
		// Conservative serialized projection, not PG physical disk allocation. At
		// most three durable receipt rows and bounded IDs/metadata reserve 32KiB.
		projection, e := canonicalRestricted(map[string]any{"commandId": id, "attemptId": attempt, "payload": *c.payload, "result": string(f.ResultCanonical)})
		if e != nil {
			return e
		}
		if len(projection)+32768 > 196608 {
			return admissionError("RESOURCE_LIMIT")
		}
		tag, e := tx.Exec(ctx, `UPDATE commands SET status=$4,effect=$5,result=$6,command_version=command_version+1 WHERE tenant=$1 AND id=$2 AND command_version=$3 AND result IS NULL`, h.Tenant, id, c.version, r.Status, r.EffectState, string(f.ResultCanonical))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		if c.status != r.Status {
			if e = commandTransition(ctx, tx, h, id, c.status, event, r.Status, ms); e != nil {
				return e
			}
		}
		tag, e = tx.Exec(ctx, `UPDATE admission_reservations SET state='materialized',materialized_at=clock_timestamp() WHERE tenant=$1 AND command=$2 AND kind='result' AND state='held'`, h.Tenant, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return admissionError("INTEGRITY_CONFLICT")
		}
		_, e = tx.Exec(ctx, `INSERT INTO tombstones(tenant,device,command,digest,status,retain_until) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant,device,command) DO UPDATE SET retain_until=GREATEST(tombstones.retain_until,EXCLUDED.retain_until)`, h.Tenant, h.DeviceID, id, c.digest, r.Status, ms+7*86400000)
		if e != nil {
			return e
		}
		if e = auditGateway(ctx, tx, h.Tenant, h.DeviceID, "command.result", id, r.Status, ms); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO outbox(tenant,device,command,kind,state) VALUES($1,$2,$3,'result_ack','pending') ON CONFLICT(tenant,command,kind) DO NOTHING`, h.Tenant, h.DeviceID, id)
		if e != nil {
			return e
		}
		return s.persistReceipt(ctx, tx, h, f, reply)
	})
	if e != nil {
		if trustedConflict {
			s.recordResultConflict(ctx, h, id, attempt)
		}
		return ReceiptReply{}, e
	}
	return reply, nil
}
func (s *GatewayStore) recordResultConflict(ctx context.Context, h LeaseHandle, id, attempt string) {
	// Best effort metadata after rollback. No attacker-selected target reaches
	// this helper until current-device + exact original attempt were authenticated.
	_ = s.leases.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		var actual *string
		e := tx.QueryRow(ctx, `SELECT attempt FROM commands WHERE tenant=$1 AND device=$2 AND id=$3 FOR UPDATE`, h.Tenant, h.DeviceID, id).Scan(&actual)
		if e != nil {
			return e
		}
		if actual == nil || *actual != attempt {
			return admissionError("NOT_FOUND")
		}
		var exists bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit WHERE tenant=$1 AND target=$2 AND action='command.result_conflict')`, h.Tenant, id).Scan(&exists)
		if e != nil || exists {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		return auditGateway(ctx, tx, h.Tenant, h.DeviceID, "command.result_conflict", id, "RESULT_CONFLICT", ms)
	})
}

// MarkAckEmitted is deliberately absent: a successful socket write is not proof
// of device journal ACK. Pending/coalesced intent is retained; retry compares the
// durable result and constructs the ACK for the CURRENT inbound event.
