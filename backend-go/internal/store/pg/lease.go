package pg

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RuntimeAuthority is a trusted adapter value, never an HTTP DTO or token verifier.
// The caller must independently authenticate the boot and external recovery epoch.
type RuntimeAuthority struct {
	instance, boot, epoch string
	trusted               bool
}

func RuntimeAuthorityFromTrusted(instance, boot, recoveryEpoch string) (RuntimeAuthority, error) {
	if !asciiIdentifier(instance) || !asciiIdentifier(boot) || !asciiIdentifier(recoveryEpoch) {
		return RuntimeAuthority{}, admissionError("INVALID_INPUT")
	}
	return RuntimeAuthority{instance, boot, recoveryEpoch, true}, nil
}
func (RuntimeAuthority) String() string { return "RuntimeAuthority(redacted)" }

type GatewayAuthority struct {
	runtime                              RuntimeAuthority
	tenant, device, session, fingerprint string
	deviceEpoch                          int64
	trusted                              bool
}

func GatewayAuthorityFromAuthenticated(runtime RuntimeAuthority, tenant, device, session, fingerprint string, deviceEpoch int64) (GatewayAuthority, error) {
	if !runtime.trusted || !asciiIdentifier(tenant) || !asciiIdentifier(device) || !asciiIdentifier(session) || len(fingerprint) != 64 || deviceEpoch < 0 || deviceEpoch > maxSafeInteger {
		return GatewayAuthority{}, admissionError("INVALID_INPUT")
	}
	for _, c := range fingerprint {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return GatewayAuthority{}, admissionError("INVALID_INPUT")
		}
	}
	return GatewayAuthority{runtime, tenant, device, session, fingerprint, deviceEpoch, true}, nil
}
func (GatewayAuthority) String() string { return "GatewayAuthority(redacted)" }

// LeaseUntil is an observation, NOT part of the immutable CAS identity.
type LeaseHandle struct {
	Tenant, DeviceID, SessionID, OwnerInstance, ConnectionID, RecoveryEpoch string
	Fence                                                                   int64
	LeaseUntil                                                              time.Time
}
type AcquireLeaseRequest struct {
	ConnectionID  string
	ExpectedFence int64
}
type ClaimCommandRequest struct {
	CommandID              string
	ExpectedCommandVersion int64
}
type ClaimedIntent struct {
	CommandID, AttemptID            string
	CommandVersion                  int64
	Lease                           LeaseHandle
	PayloadDigest, CanonicalPayload string
	PolicyVersion                   int64
	RequiresLocalConfirmation       bool
	ExpiresAt                       string
}
type ClaimOutcome struct {
	Intent *ClaimedIntent
	Reason string
}
type RetireOutcome struct {
	Changed          bool
	RetiredCommandID string
}
type LeaseStore struct {
	runner    *Runner
	localGate func(context.Context) error
}

func NewLeaseStore(r *Runner, gate func(context.Context) error) (*LeaseStore, error) {
	if r == nil || gate == nil {
		return nil, admissionError("INVALID_INPUT")
	}
	return &LeaseStore{r, gate}, nil
}
func leaseError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, ErrCommitOutcomeUnknown) {
		return admissionError("COMMIT_OUTCOME_UNKNOWN")
	}
	var a admissionError
	if errors.As(e, &a) {
		return a
	}
	var p *pgconn.PgError
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) || (errors.As(e, &p) && (p.Code == "57014" || p.Code == "55P03")) {
		return admissionError("STORE_TIMEOUT")
	}
	return admissionError("INTEGRITY_CONFLICT")
}
func (s *LeaseStore) run(ctx context.Context, tenant string, fn func(context.Context, pgx.Tx) error) error {
	if ctx == nil || !asciiIdentifier(tenant) {
		return admissionError("INVALID_INPUT")
	}
	if e := s.localGate(ctx); e != nil {
		return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	return leaseError(s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		b, e := lockScope(ctx, tx, tenant, "tenant", tenant)
		if e != nil {
			return e
		}
		if !b.enabled {
			return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
		}
		return fn(ctx, tx)
	}))
}

type leaseRow struct {
	LeaseHandle
	state string
}

func readLease(ctx context.Context, tx pgx.Tx, tenant, device string) (*leaseRow, error) {
	var r leaseRow
	r.Tenant = tenant
	r.DeviceID = device
	e := tx.QueryRow(ctx, `SELECT session_id,owner_instance,connection_id,recovery_epoch,fence,lease_until,state FROM device_gateway_leases WHERE tenant=$1 AND device=$2 FOR UPDATE`, tenant, device).Scan(&r.SessionID, &r.OwnerInstance, &r.ConnectionID, &r.RecoveryEpoch, &r.Fence, &r.LeaseUntil, &r.state)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, e
}
func validHandle(h LeaseHandle) bool {
	return asciiIdentifier(h.Tenant) && asciiIdentifier(h.DeviceID) && asciiIdentifier(h.SessionID) && asciiIdentifier(h.OwnerInstance) && asciiIdentifier(h.ConnectionID) && asciiIdentifier(h.RecoveryEpoch) && h.Fence > 0
}
func sameHandle(a, b LeaseHandle) bool {
	return a.Tenant == b.Tenant && a.DeviceID == b.DeviceID && a.SessionID == b.SessionID && a.OwnerInstance == b.OwnerInstance && a.ConnectionID == b.ConnectionID && a.RecoveryEpoch == b.RecoveryEpoch && a.Fence == b.Fence
}
func lockRuntime(ctx context.Context, tx pgx.Tx, r RuntimeAuthority, role string) error {
	if !r.trusted {
		return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	var boot, epoch, state string
	var capable bool
	e := tx.QueryRow(ctx, `SELECT boot_id,recovery_epoch,state,$2=ANY(roles) FROM runtime_instances WHERE instance_id=$1 FOR SHARE`, r.instance, role).Scan(&boot, &epoch, &state, &capable)
	if errors.Is(e, pgx.ErrNoRows) {
		return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	if e != nil {
		return e
	}
	if boot != r.boot || epoch != r.epoch || state != "active" || !capable {
		return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	return nil
}

type gatewayFacts struct {
	expires, policy int64
	capable         bool
}

func lockGateway(ctx context.Context, tx pgx.Tx, a GatewayAuthority, old *leaseRow) (gatewayFacts, bool, error) {
	var f gatewayFacts
	var state string
	var epoch int64
	e := tx.QueryRow(ctx, `SELECT state,epoch,policy,capabilities ? 'message.send_text' FROM devices WHERE tenant=$1 AND id=$2 FOR SHARE`, a.tenant, a.device).Scan(&state, &epoch, &f.policy, &f.capable)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, false, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	if e != nil {
		return f, false, e
	}
	if state != "active" || epoch != a.deviceEpoch {
		return f, false, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	ids := []string{a.session}
	if old != nil && old.SessionID != a.session {
		ids = append(ids, old.SessionID)
	}
	rows, e := tx.Query(ctx, `SELECT id,token,epoch,expires,revoked FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=ANY($3::text[]) ORDER BY id COLLATE "C" FOR UPDATE`, a.tenant, a.device, ids)
	if e != nil {
		return f, false, e
	}
	defer rows.Close()
	found, oldRevoked := false, false
	for rows.Next() {
		var id, fp string
		var de, expires int64
		var revoked bool
		if e = rows.Scan(&id, &fp, &de, &expires, &revoked); e != nil {
			return f, false, e
		}
		if old != nil && id == old.SessionID {
			oldRevoked = revoked
		}
		if id == a.session {
			found = true
			f.expires = expires
			if revoked || de != a.deviceEpoch || subtle.ConstantTimeCompare([]byte(fp), []byte(a.fingerprint)) != 1 {
				return f, false, admissionError("SESSION_NOT_CURRENT")
			}
		}
	}
	if e = rows.Err(); e != nil {
		return f, false, e
	}
	if !found {
		return f, false, admissionError("SESSION_NOT_CURRENT")
	}
	return f, oldRevoked, nil
}
func (s *LeaseStore) now(ctx context.Context, tx pgx.Tx) (time.Time, int64, error) {
	if e := s.localGate(ctx); e != nil {
		return time.Time{}, 0, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	var t time.Time
	var ms int64
	e := tx.QueryRow(ctx, `SELECT n,floor(extract(epoch FROM n)*1000)::bigint FROM (SELECT clock_timestamp() n) x`).Scan(&t, &ms)
	if e != nil {
		return t, ms, e
	}
	if ms < 0 || ms > maxWireMillis {
		return t, ms, admissionError("INTEGRITY_CONFLICT")
	}
	return t, ms, nil
}
func liveGateway(f gatewayFacts, ms int64) error {
	if f.expires < 0 || f.expires > maxSafeInteger || f.policy < 1 || f.policy > maxSafeInteger {
		return admissionError("INTEGRITY_CONFLICT")
	}
	if f.expires <= ms {
		return admissionError("SESSION_NOT_CURRENT")
	}
	return nil
}
func authorityHandle(a GatewayAuthority, h LeaseHandle) bool {
	return a.trusted && h.Tenant == a.tenant && h.DeviceID == a.device && h.SessionID == a.session && h.OwnerInstance == a.runtime.instance && h.RecoveryEpoch == a.runtime.epoch
}
func deadline(now time.Time, expires int64) time.Time {
	v := now.Add(30 * time.Second)
	cap := time.UnixMilli(expires)
	if v.After(cap) {
		v = cap
	}
	return v
}
func leaseAudit(ctx context.Context, tx pgx.Tx, h LeaseHandle, action string, ms int64) error {
	id, e := freshID("audit_")
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit(id,tenant,actor,action,target,outcome,created) VALUES($1,$2,$3,$4,$5,'committed',$6)`, id, h.Tenant, h.OwnerInstance, action, h.DeviceID, ms)
	return e
}
func clearSnapshot(ctx context.Context, tx pgx.Tx, h LeaseHandle) error {
	_, e := tx.Exec(ctx, `UPDATE device_sessions SET gateway_owner=NULL,gateway_fence=NULL,gateway_lease_until=NULL,gateway_connection=NULL WHERE tenant=$1 AND device=$2 AND id=$3 AND gateway_owner=$4 AND gateway_fence=$5 AND gateway_connection=$6`, h.Tenant, h.DeviceID, h.SessionID, h.OwnerInstance, h.Fence, h.ConnectionID)
	return e
}
func writeSnapshot(ctx context.Context, tx pgx.Tx, h LeaseHandle) error {
	tag, e := tx.Exec(ctx, `UPDATE device_sessions SET gateway_owner=$4,gateway_fence=$5,gateway_connection=$6,gateway_lease_until=$7 WHERE tenant=$1 AND device=$2 AND id=$3 AND NOT revoked`, h.Tenant, h.DeviceID, h.SessionID, h.OwnerInstance, h.Fence, h.ConnectionID, h.LeaseUntil)
	if e == nil && tag.RowsAffected() != 1 {
		return admissionError("INTEGRITY_CONFLICT")
	}
	return e
}
func (s *LeaseStore) AcquireDeviceLease(ctx context.Context, a GatewayAuthority, q AcquireLeaseRequest) (LeaseHandle, error) {
	return s.acquireDeviceLease(ctx, a, q, "")
}

// Only the gateway's already-consumed, server-generated physical nonce may use
// current-fence selection. Public expectedFence semantics are unchanged.
func (s *LeaseStore) acquireConsumed(ctx context.Context, a GatewayAuthority, nonce string) (LeaseHandle, error) {
	return s.acquireDeviceLease(ctx, a, AcquireLeaseRequest{ConnectionID: nonce}, nonce)
}
func (s *LeaseStore) acquireDeviceLease(ctx context.Context, a GatewayAuthority, q AcquireLeaseRequest, consumedNonce string) (LeaseHandle, error) {
	var out LeaseHandle
	if !a.trusted || !asciiIdentifier(q.ConnectionID) || q.ExpectedFence < 0 {
		return out, admissionError("INVALID_INPUT")
	}
	if q.ExpectedFence == math.MaxInt64 {
		return out, admissionError("FENCE_EXHAUSTED")
	}
	e := s.run(ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		old, e := readLease(ctx, tx, a.tenant, a.device)
		if e != nil {
			return e
		}
		if e = lockRuntime(ctx, tx, a.runtime, "gateway"); e != nil {
			return e
		}
		f, revoked, e := lockGateway(ctx, tx, a, old)
		if e != nil {
			return e
		}
		if consumedNonce != "" {
			var consumed bool
			var nonce string
			e = tx.QueryRow(ctx, `SELECT ws_consumed,COALESCE(ws_connection_id,'') FROM device_sessions WHERE tenant=$1 AND device=$2 AND id=$3`, a.tenant, a.device, a.session).Scan(&consumed, &nonce)
			if e != nil {
				return e
			}
			if !consumed || nonce != consumedNonce || nonce != q.ConnectionID {
				return admissionError("SESSION_NOT_CURRENT")
			}
		}
		now, ms, e := s.now(ctx, tx)
		if e != nil {
			return e
		}
		if e = liveGateway(f, ms); e != nil {
			return e
		}
		if consumedNonce != "" && old != nil {
			sameAcquisition := old.SessionID == a.session && old.OwnerInstance == a.runtime.instance && old.ConnectionID == consumedNonce && old.RecoveryEpoch == a.runtime.epoch
			if sameAcquisition && old.state == "active" && old.LeaseUntil.After(now) {
				out = old.LeaseHandle
				return nil
			}
			if old.Fence == math.MaxInt64 {
				return admissionError("FENCE_EXHAUSTED")
			}
			q.ExpectedFence = old.Fence
		}
		h := LeaseHandle{a.tenant, a.device, a.session, a.runtime.instance, q.ConnectionID, a.runtime.epoch, q.ExpectedFence + 1, deadline(now, f.expires)}
		if old != nil {
			if old.state == "active" && old.LeaseUntil.After(now) && sameHandle(old.LeaseHandle, h) {
				out = old.LeaseHandle
				return nil
			}
			if old.Fence != q.ExpectedFence {
				return admissionError("STALE_FENCE")
			}
			if old.Fence == math.MaxInt64 {
				return admissionError("FENCE_EXHAUSTED")
			}
			if old.ConnectionID == q.ConnectionID {
				return admissionError("STALE_LEASE")
			}
			if old.RecoveryEpoch != a.runtime.epoch {
				return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
			}
			if old.state == "active" && old.LeaseUntil.After(now) && !revoked {
				return admissionError("LEASE_BUSY")
			}
			if _, e = retireCommands(ctx, tx, old.LeaseHandle, ms); e != nil {
				return e
			}
			if e = clearSnapshot(ctx, tx, old.LeaseHandle); e != nil {
				return e
			}
			// Retirement may wait on a command/evidence row after the first
			// decision. Time is not frozen by auth locks: recheck immediately
			// before installing ownership, rolling the entire retirement back
			// if the new session expired while that later lock was pending.
			now, ms, e = s.now(ctx, tx)
			if e != nil {
				return e
			}
			if e = liveGateway(f, ms); e != nil {
				return e
			}
			h.LeaseUntil = deadline(now, f.expires)
			_, e = tx.Exec(ctx, `UPDATE device_gateway_leases SET session_id=$3,owner_instance=$4,connection_id=$5,recovery_epoch=$6,fence=$7,lease_until=$8,state='active',updated_at=$9 WHERE tenant=$1 AND device=$2`, h.Tenant, h.DeviceID, h.SessionID, h.OwnerInstance, h.ConnectionID, h.RecoveryEpoch, h.Fence, h.LeaseUntil, now)
		} else {
			if q.ExpectedFence != 0 {
				return admissionError("STALE_FENCE")
			}
			_, e = tx.Exec(ctx, `INSERT INTO device_gateway_leases(tenant,device,session_id,owner_instance,connection_id,recovery_epoch,fence,lease_until,state) VALUES($1,$2,$3,$4,$5,$6,1,$7,'active')`, h.Tenant, h.DeviceID, h.SessionID, h.OwnerInstance, h.ConnectionID, h.RecoveryEpoch, h.LeaseUntil)
		}
		if e != nil {
			return e
		}
		if e = writeSnapshot(ctx, tx, h); e != nil {
			return e
		}
		if e = leaseAudit(ctx, tx, h, "gateway.lease_acquired", ms); e != nil {
			return e
		}
		out = h
		return nil
	})
	if e != nil {
		return LeaseHandle{}, e
	}
	return out, nil
}
func (s *LeaseStore) RenewDeviceLease(ctx context.Context, a GatewayAuthority, h LeaseHandle) (LeaseHandle, error) {
	var out LeaseHandle
	if !validHandle(h) || !authorityHandle(a, h) {
		return out, admissionError("INVALID_INPUT")
	}
	e := s.run(ctx, h.Tenant, func(ctx context.Context, tx pgx.Tx) error {
		old, e := readLease(ctx, tx, h.Tenant, h.DeviceID)
		if e != nil {
			return e
		}
		if e = lockRuntime(ctx, tx, a.runtime, "gateway"); e != nil {
			return e
		}
		if old == nil || old.state != "active" || !sameHandle(old.LeaseHandle, h) {
			return admissionError("STALE_LEASE")
		}
		f, _, e := lockGateway(ctx, tx, a, old)
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
		if !old.LeaseUntil.After(now) {
			return admissionError("LEASE_EXPIRED")
		}
		until := deadline(now, f.expires)
		if old.LeaseUntil.After(until) {
			return admissionError("CLOCK_BOUNDARY_CONFLICT")
		}
		h.LeaseUntil = until
		_, e = tx.Exec(ctx, `UPDATE device_gateway_leases SET lease_until=$3,updated_at=$4 WHERE tenant=$1 AND device=$2`, h.Tenant, h.DeviceID, until, now)
		if e != nil {
			return e
		}
		if e = writeSnapshot(ctx, tx, h); e != nil {
			return e
		}
		out = h
		return leaseAudit(ctx, tx, h, "gateway.lease_renewed", ms)
	})
	if e != nil {
		return LeaseHandle{}, e
	}
	return out, nil
}
