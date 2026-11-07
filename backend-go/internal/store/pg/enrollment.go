package pg

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

// TrustedAdminAuthority is an internal fixture/control bridge, NOT an HTTP
// authentication mechanism. There is deliberately no public provisioning route.
type TrustedAdminAuthority struct {
	tenant, actor string
	trusted       bool
}

func TrustedAdminFromControl(tenant, actor string) (TrustedAdminAuthority, error) {
	if !wireID(tenant) || !wireID(actor) {
		return TrustedAdminAuthority{}, admissionError("INVALID_INPUT")
	}
	return TrustedAdminAuthority{tenant, actor, true}, nil
}
func (TrustedAdminAuthority) String() string { return "TrustedAdminAuthority(redacted)" }

type EnrollmentRequest struct {
	DeviceLabel      string
	Capabilities     []string
	ExpiresInSeconds int64
}
type EnrollmentGrant struct{ EnrollmentID, EnrollmentToken, ExpiresAt string }

func (EnrollmentGrant) String() string { return "EnrollmentGrant(redacted)" }

type EnrollmentExchange struct {
	EnrollmentID, EnrollmentToken, DeviceLabel string
	LocalConfirmed                             bool
}
type DeviceCredentials struct {
	DeviceID, KeyID, DeviceSecret string
	PolicyVersion                 int64
}

func (DeviceCredentials) String() string { return "DeviceCredentials(redacted)" }
func capsValid(caps []string) bool {
	if len(caps) < 1 || len(caps) > 2 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range caps {
		if seen[c] || (c != "message.send_text" && c != "file.export") {
			return false
		}
		seen[c] = true
	}
	return true
}
func auditGateway(ctx context.Context, tx pgx.Tx, tenant, actor, action, target, outcome string, ms int64) error {
	id, e := freshID("audit_")
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO audit(id,tenant,actor,action,target,outcome,created) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, tenant, actor, action, target, outcome, ms)
	return e
}
func (s *GatewayStore) ProvisionEnrollment(ctx context.Context, a TrustedAdminAuthority, r EnrollmentRequest) (EnrollmentGrant, error) {
	var out EnrollmentGrant
	if !a.trusted || !labelValid(r.DeviceLabel) || !capsValid(r.Capabilities) || r.ExpiresInSeconds < 30 || r.ExpiresInSeconds > 300 {
		return out, admissionError("INVALID_INPUT")
	}
	raw, e := randomSecret()
	if e != nil {
		return out, e
	}
	id, e := freshID("enroll_")
	if e != nil {
		return out, e
	}
	caps, e := json.Marshal(r.Capabilities)
	if e != nil {
		return out, admissionError("INVALID_INPUT")
	}
	e = s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		b, e := lockScope(ctx, tx, a.tenant, "tenant", a.tenant)
		if e != nil {
			return e
		}
		if !b.enabled {
			return admissionError("ROLE_DENIED")
		}
		var role string
		var active bool
		e = tx.QueryRow(ctx, `SELECT role,active FROM users WHERE tenant=$1 AND id=$2 FOR SHARE`, a.tenant, a.actor).Scan(&role, &active)
		if e != nil {
			return e
		}
		if !active || role != "admin" {
			return admissionError("ROLE_DENIED")
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		expires := ms + r.ExpiresInSeconds*1000
		if expires > maxWireMillis {
			return admissionError("INVALID_INPUT")
		}
		_, e = tx.Exec(ctx, `INSERT INTO enrollments(id,tenant,token,capabilities,expires) VALUES($1,$2,$3,$4::jsonb,$5)`, id, a.tenant, hashText(raw), string(caps), expires)
		if e != nil {
			return e
		}
		if e = auditGateway(ctx, tx, a.tenant, a.actor, "enrollment.create", id, "created", ms); e != nil {
			return e
		}
		at, e := wireTime(expires)
		if e != nil {
			return e
		}
		out = EnrollmentGrant{id, raw, at}
		return nil
	})
	if e != nil {
		return EnrollmentGrant{}, e
	}
	return out, nil
}
func (s *GatewayStore) ConsumeEnrollment(ctx context.Context, r EnrollmentExchange) (DeviceCredentials, error) {
	var out DeviceCredentials
	if !wireID(r.EnrollmentID) || !bearerSecret(r.EnrollmentToken) || !labelValid(r.DeviceLabel) || !r.LocalConfirmed {
		return out, admissionError("INVALID_INPUT")
	}
	secret, e := randomSecret()
	if e != nil {
		return out, e
	}
	id, e := freshID("dev_")
	if e != nil {
		return out, e
	}
	key, e := freshID("key_")
	if e != nil {
		return out, e
	}
	e = s.run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var tenant string
		e := tx.QueryRow(ctx, `SELECT tenant FROM enrollments WHERE id=$1`, r.EnrollmentID).Scan(&tenant)
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
		var fp, caps string
		var expires int64
		var consumed bool
		e = tx.QueryRow(ctx, `SELECT token,capabilities::text,expires,consumed FROM enrollments WHERE tenant=$1 AND id=$2 FOR UPDATE`, tenant, r.EnrollmentID).Scan(&fp, &caps, &expires, &consumed)
		if e != nil {
			return e
		}
		var c []string
		if json.Unmarshal([]byte(caps), &c) != nil || !capsValid(c) {
			return admissionError("INTEGRITY_CONFLICT")
		}
		ms, e := s.time(ctx, tx)
		if e != nil {
			return e
		}
		if consumed || expires <= ms || subtle.ConstantTimeCompare([]byte(fp), []byte(hashText(r.EnrollmentToken))) != 1 {
			return admissionError("UNAUTHENTICATED")
		}
		_, e = tx.Exec(ctx, `UPDATE enrollments SET consumed=true WHERE tenant=$1 AND id=$2`, tenant, r.EnrollmentID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO devices(tenant,id,name,key_id,secret,capabilities) VALUES($1,$2,$3,$4,$5,$6::jsonb)`, tenant, id, r.DeviceLabel, key, hashText(secret), caps)
		if e != nil {
			return e
		}
		if e = auditGateway(ctx, tx, tenant, id, "device.enrolled", id, "created", ms); e != nil {
			return e
		}
		out = DeviceCredentials{id, key, secret, 1}
		return nil
	})
	if e != nil {
		return DeviceCredentials{}, e
	}
	return out, nil
}
