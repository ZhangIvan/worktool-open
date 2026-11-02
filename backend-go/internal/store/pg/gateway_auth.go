package pg

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
	"unicode/utf8"
)

// GatewayStore supplies narrow authenticated HTTP/WS DB operations. It does
// not listen or own the pool. Runtime and gate come only from trusted startup.
type GatewayStore struct {
	runner    *Runner
	gate      func(context.Context) error
	runtime   RuntimeAuthority
	leases    *LeaseStore
	admission *AdmissionStore
}

func NewGatewayStore(r *Runner, gate func(context.Context) error, runtime RuntimeAuthority) (*GatewayStore, error) {
	if r == nil || gate == nil || !runtime.trusted {
		return nil, admissionError("INVALID_INPUT")
	}
	l, e := NewLeaseStore(r, gate)
	if e != nil {
		return nil, e
	}
	a, e := NewAdmissionStore(r, gate)
	if e != nil {
		return nil, e
	}
	return &GatewayStore{r, gate, runtime, l, a}, nil
}
func (s *GatewayStore) run(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if ctx == nil {
		return admissionError("INVALID_INPUT")
	}
	if e := s.gate(ctx); e != nil {
		return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	return leaseError(s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn))
}
func (s *GatewayStore) time(ctx context.Context, tx pgx.Tx) (int64, error) {
	_, ms, e := s.leases.now(ctx, tx)
	return ms, e
}
func randomSecret() (string, error) {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", admissionError("STORAGE_UNAVAILABLE")
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func bearerSecret(raw string) bool {
	if len(raw) < 32 || len(raw) > 256 {
		return false
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func hexToken(raw string) bool {
	if len(raw) != 64 {
		return false
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func wireID(s string) bool {
	if len(s) < 3 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func labelValid(s string) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) >= 1 && utf8.RuneCountInString(s) <= 64 && !strings.ContainsRune(s, 0)
}

type SessionAuth struct {
	a          GatewayAuthority
	expires    int64
	connection string
	consumed   bool
}

func (SessionAuth) String() string { return "SessionAuth(redacted)" }
func (a SessionAuth) Identity() (tenant, device, session string, epoch int64) {
	return a.a.tenant, a.a.device, a.a.session, a.a.deviceEpoch
}
func (a SessionAuth) ExpiresAt() time.Time        { return time.UnixMilli(a.expires) }
func (a SessionAuth) Authority() GatewayAuthority { return a.a }
func (s *GatewayStore) AuthenticateSession(ctx context.Context, raw string) (SessionAuth, error) {
	var out SessionAuth
	if !bearerSecret(raw) {
		return out, admissionError("UNAUTHENTICATED")
	}
	fp := hashText(raw)
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if e := lockRuntime(ctx, tx, s.runtime, "gateway"); e != nil {
			return e
		}
		var tenant, device, id, state, conn string
		var epoch, de, expires int64
		var revoked, consumed bool
		e := tx.QueryRow(ctx, `SELECT s.tenant,s.device,s.id,s.epoch,s.expires,s.revoked,d.state,d.epoch,s.ws_consumed,COALESCE(s.ws_connection_id,'') FROM device_sessions s JOIN devices d ON d.tenant=s.tenant AND d.id=s.device WHERE s.token=$1`, fp).Scan(&tenant, &device, &id, &epoch, &expires, &revoked, &state, &de, &consumed, &conn)
		if errors.Is(e, pgx.ErrNoRows) {
			return admissionError("UNAUTHENTICATED")
		}
		if e != nil {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		if revoked || state != "active" || de != epoch || expires <= ms {
			return admissionError("UNAUTHENTICATED")
		}
		a, e := GatewayAuthorityFromAuthenticated(s.runtime, tenant, device, id, fp, epoch)
		if e != nil {
			return e
		}
		out = SessionAuth{a, expires, conn, consumed}
		return nil
	})
	if e != nil {
		return SessionAuth{}, e
	}
	return out, nil
}

// AppPrincipal is internal authenticated context, never JSON-decoded identity.
type AppPrincipal struct {
	tenant, id, fingerprint string
	epoch                   int64
	trusted                 bool
}

func (AppPrincipal) String() string { return "AppPrincipal(redacted)" }
func (s *GatewayStore) AuthenticateApp(ctx context.Context, raw, scope string) (AppPrincipal, error) {
	var out AppPrincipal
	if !hexToken(raw) || (scope != "commands:write" && scope != "results:read") {
		return out, admissionError("UNAUTHENTICATED")
	}
	fp := hashText(raw)
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var tenant, id string
		var epoch, expires int64
		var active, allowed bool
		e := tx.QueryRow(ctx, `SELECT tenant,id,auth_epoch,token_expires,active,scopes ? $2 FROM developer_apps WHERE token_hash=$1`, fp, scope).Scan(&tenant, &id, &epoch, &expires, &active, &allowed)
		if errors.Is(e, pgx.ErrNoRows) {
			return admissionError("UNAUTHENTICATED")
		}
		if e != nil {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		if !active || expires <= ms {
			return admissionError("UNAUTHENTICATED")
		}
		if !allowed {
			return admissionError("SCOPE_DENIED")
		}
		out = AppPrincipal{tenant, id, fp, epoch, true}
		return nil
	})
	if e != nil {
		return AppPrincipal{}, e
	}
	return out, nil
}
func (s *GatewayStore) AdmitText(ctx context.Context, p AppPrincipal, r AppTextRequest) (Accepted, error) {
	if !p.trusted {
		return Accepted{}, admissionError("UNAUTHENTICATED")
	}
	var epoch int64
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT epoch FROM devices WHERE tenant=$1 AND id=$2`, p.tenant, r.DeviceID).Scan(&epoch)
	})
	if e != nil {
		return Accepted{}, e
	}
	a, e := AppAuthorityFromAuthenticated(p.tenant, p.id, r.DeviceID, p.fingerprint, p.epoch, epoch)
	if e != nil {
		return Accepted{}, e
	}
	return s.admission.AdmitGatewayTextF0(ctx, a, r)
}
func lockAppRead(ctx context.Context, tx pgx.Tx, p AppPrincipal, requiredScope string) (int64, error) {
	if !p.trusted {
		return 0, admissionError("UNAUTHENTICATED")
	}
	var fp string
	var expires, epoch int64
	var active, scope bool
	e := tx.QueryRow(ctx, `SELECT token_hash,auth_epoch,token_expires,active,scopes ? $3 FROM developer_apps WHERE tenant=$1 AND id=$2 FOR SHARE`, p.tenant, p.id, requiredScope).Scan(&fp, &epoch, &expires, &active, &scope)
	if e != nil {
		return 0, e
	}
	if !active || !scope || epoch != p.epoch || subtle.ConstantTimeCompare([]byte(fp), []byte(p.fingerprint)) != 1 {
		return 0, admissionError("UNAUTHENTICATED")
	}
	return expires, nil
}

type MessageReceipt struct {
	BatchID, MessageID, ChildID, CommandID, Status, EffectState string
	Result                                                      *string
}

func (s *GatewayStore) Message(ctx context.Context, p AppPrincipal, message string) (MessageReceipt, error) {
	return s.message(ctx, p, message, "results:read")
}
func (s *GatewayStore) SubmissionReceipt(ctx context.Context, p AppPrincipal, message string) (MessageReceipt, error) {
	return s.message(ctx, p, message, "commands:write")
}
func (s *GatewayStore) message(ctx context.Context, p AppPrincipal, message, requiredScope string) (MessageReceipt, error) {
	var out MessageReceipt
	if !wireID(message) {
		return out, admissionError("NOT_FOUND")
	}
	e := s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		expires, e := lockAppRead(ctx, tx, p, requiredScope)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `SELECT m.batch,m.message,m.child,m.command,c.status,c.effect,c.result FROM developer_messages m JOIN commands c ON c.tenant=m.tenant AND c.app_id=m.app AND c.id=m.command WHERE m.tenant=$1 AND m.app=$2 AND m.message=$3 AND c.actor_type='app'`, p.tenant, p.id, message).Scan(&out.BatchID, &out.MessageID, &out.ChildID, &out.CommandID, &out.Status, &out.EffectState, &out.Result)
		if errors.Is(e, pgx.ErrNoRows) {
			return admissionError("NOT_FOUND")
		}
		if e != nil {
			return e
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		if expires <= ms {
			return admissionError("UNAUTHENTICATED")
		}
		return nil
	})
	if e != nil {
		return MessageReceipt{}, e
	}
	return out, nil
}
