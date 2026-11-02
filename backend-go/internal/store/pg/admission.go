package pg

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type admissionError string

func (e admissionError) Error() string { return string(e) }

// AppAuthority has no exported fields and no JSON decoding authentication path.
// Only a trusted authentication adapter may invoke the explicit constructor.
// The constructor does NOT authenticate a token; all current DB facts are checked
// inside AdmitAppText. Never log this value or use it as a request DTO.
type AppAuthority struct {
	tenant, app, device, fingerprint string
	epoch, deviceEpoch               int64
	trusted                          bool
}

func AppAuthorityFromAuthenticated(tenant, app, device, credentialFingerprint string, epoch, deviceEpoch int64) (AppAuthority, error) {
	if !boundedID(tenant) || !boundedID(app) || !asciiIdentifier(device) || epoch < 0 || epoch > maxSafeInteger || deviceEpoch < 0 || deviceEpoch > maxSafeInteger || len(credentialFingerprint) != 64 {
		return AppAuthority{}, admissionError("INVALID_INPUT")
	}
	for _, c := range credentialFingerprint {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return AppAuthority{}, admissionError("INVALID_INPUT")
		}
	}
	return AppAuthority{tenant, app, device, credentialFingerprint, epoch, deviceEpoch, true}, nil
}
func (a AppAuthority) String() string { return "AppAuthority(redacted)" }

func boundedID(s string) bool { return len(s) > 0 && len(s) <= 128 && utf8.ValidString(s) }

// AppTextRequest is a narrow internal DTO, not an HTTP JSON parser.
type AppTextRequest struct {
	IdempotencyKey, DeviceID, TargetID, Text, Mode string
	Type, ExpiresInSeconds                         int64
}
type Accepted struct{ Status, Mode, BatchID, MessageID, ChildID, CommandID, AcceptedAt, ExpiresAt string }
type AdmissionStore struct {
	runner    *Runner
	localGate func(context.Context) error
}

// gate is a mandatory local recovery/admission interlock. It must not do network
// I/O. An always-open gate is allowed only for explicitly synthetic tests.
func NewAdmissionStore(r *Runner, gate func(context.Context) error) (*AdmissionStore, error) {
	if r == nil || gate == nil {
		return nil, admissionError("INVALID_INPUT")
	}
	return &AdmissionStore{r, gate}, nil
}

type scopeBudget struct {
	jobs, bytes, usedJobs, usedBytes int64
	enabled, admission               bool
}
type subscription struct {
	App, ID, Destination, Event, Schema          string
	Version, Policy, Key, Events, Body, Attempts int64
}

func (s subscription) value() any {
	return map[string]any{"app": s.App, "subscriptionId": s.ID, "subscriptionVersion": s.Version, "destinationId": s.Destination, "destinationPolicyVersion": s.Policy, "keyVersion": s.Key, "maxAttempts": s.Attempts, "eventType": s.Event, "eventSchemaVersion": s.Schema, "maxEventsPerChild": s.Events, "maxBodyBytes": s.Body}
}
func snapshotValues(subs []subscription) []any {
	v := make([]any, len(subs))
	for i, s := range subs {
		v[i] = s.value()
	}
	return v
}
func subscriptions(ctx context.Context, tx pgx.Tx, a AppAuthority, locked bool) ([]subscription, error) {
	q := `SELECT app,subscription_id,destination_id,event_type,event_schema_version,version,destination_policy_version,key_version,max_events_per_child,max_body_bytes,max_attempts FROM callback_subscription_versions WHERE tenant=$1 AND app=$2 AND retired_at IS NULL ORDER BY destination_id COLLATE "C" LIMIT 5`
	if locked {
		q += " FOR SHARE"
	}
	rows, e := tx.Query(ctx, q, a.tenant, a.app)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []subscription{}
	for rows.Next() {
		var s subscription
		if e = rows.Scan(&s.App, &s.ID, &s.Destination, &s.Event, &s.Schema, &s.Version, &s.Policy, &s.Key, &s.Events, &s.Body, &s.Attempts); e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func lockScope(ctx context.Context, tx pgx.Tx, tenant, kind, id string) (scopeBudget, error) {
	var b scopeBudget
	e := tx.QueryRow(ctx, `SELECT capacity_jobs,capacity_bytes,charged_jobs,charged_bytes,enabled,admission_enabled FROM scope_limits WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3 FOR UPDATE`, kind, tenant, id).Scan(&b.jobs, &b.bytes, &b.usedJobs, &b.usedBytes, &b.enabled, &b.admission)
	if errors.Is(e, pgx.ErrNoRows) {
		return b, admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
	}
	return b, e
}
func checkBudget(b scopeBudget, jobs, bytes int64) error {
	if !b.enabled || !b.admission {
		return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
	}
	if b.usedJobs < 0 || b.usedBytes < 0 || b.usedJobs > b.jobs || b.usedBytes > b.bytes {
		return admissionError("INTEGRITY_CONFLICT")
	}
	if jobs > b.jobs-b.usedJobs || bytes > b.bytes-b.usedBytes {
		return admissionError("CAPACITY_EXHAUSTED")
	}
	return nil
}
func changeBudget(ctx context.Context, tx pgx.Tx, tenant, kind, id string, jobs, bytes int64) error {
	tag, e := tx.Exec(ctx, `UPDATE scope_limits SET charged_jobs=charged_jobs+$4,charged_bytes=charged_bytes+$5 WHERE scope_kind=$1 AND tenant=$2 AND scope_id=$3 AND admission_enabled AND enabled AND $4<=capacity_jobs-charged_jobs AND $5<=capacity_bytes-charged_bytes`, kind, tenant, id, jobs, bytes)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return admissionError("CAPACITY_EXHAUSTED")
	}
	return nil
}

type authFacts struct {
	creator         string
	expires, policy int64
}

func authorize(ctx context.Context, tx pgx.Tx, a AppAuthority) (authFacts, error) {
	var f authFacts
	var fp, state string
	var epoch, deviceEpoch int64
	var active, scope, capable bool
	e := tx.QueryRow(ctx, `SELECT creator,token_hash,auth_epoch,token_expires,active,scopes ? 'commands:write' FROM developer_apps WHERE tenant=$1 AND id=$2 FOR SHARE`, a.tenant, a.app).Scan(&f.creator, &fp, &epoch, &f.expires, &active, &scope)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	if e != nil {
		return f, e
	}
	if !active || !scope || epoch != a.epoch || subtle.ConstantTimeCompare([]byte(fp), []byte(a.fingerprint)) != 1 {
		return f, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	e = tx.QueryRow(ctx, `SELECT state,epoch,policy,capabilities ? 'message.send_text' FROM devices WHERE tenant=$1 AND id=$2 FOR SHARE`, a.tenant, a.device).Scan(&state, &deviceEpoch, &f.policy, &capable)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	if e != nil {
		return f, e
	}
	if state != "active" || deviceEpoch != a.deviceEpoch {
		return f, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	if !capable {
		return f, admissionError("CAPABILITY_UNAVAILABLE")
	}
	var device string
	e = tx.QueryRow(ctx, `SELECT device FROM developer_bindings WHERE tenant=$1 AND app=$2 AND device=$3 FOR SHARE`, a.tenant, a.app, a.device).Scan(&device)
	if errors.Is(e, pgx.ErrNoRows) {
		return f, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	return f, e
}
func decision(ctx context.Context, tx pgx.Tx, f authFacts) (int64, error) {
	var now int64
	e := tx.QueryRow(ctx, `SELECT floor(extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now)
	if e != nil {
		return 0, e
	}
	if now < 0 || now > maxWireMillis || f.expires < 0 || f.expires > maxSafeInteger || f.policy < 1 || f.policy > maxSafeInteger {
		return 0, admissionError("INTEGRITY_CONFLICT")
	}
	if f.expires <= now {
		return 0, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	return now, nil
}
func wireTime(ms int64) (string, error) {
	if ms < 0 || ms > maxWireMillis {
		return "", admissionError("INVALID_INPUT")
	}
	return time.Unix(ms/1000, 0).UTC().Format("2006-01-02T15:04:05Z"), nil
}
func findReceipt(ctx context.Context, tx pgx.Tx, a AppAuthority, key string) (Accepted, string, bool, error) {
	var r Accepted
	var digest string
	var created, expires int64
	e := tx.QueryRow(ctx, `SELECT m.batch,m.message,m.child,m.command,m.request_hash,c.created,c.expires FROM developer_messages m JOIN commands c ON c.tenant=m.tenant AND c.app_id=m.app AND c.id=m.command WHERE m.tenant=$1 AND m.app=$2 AND m.key_hash=$3`, a.tenant, a.app, key).Scan(&r.BatchID, &r.MessageID, &r.ChildID, &r.CommandID, &digest, &created, &expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, "", false, nil
	}
	if e != nil {
		return r, "", false, e
	}
	r.Status = "accepted"
	r.Mode = "synthetic"
	r.AcceptedAt, e = wireTime(created)
	if e != nil {
		return r, "", false, e
	}
	r.ExpiresAt, e = wireTime(expires)
	return r, digest, true, e
}
func freshID(prefix string) (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", admissionError("INTEGRITY_CONFLICT")
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// AdmitAppText performs one DB-only transaction; quota is internal capacity, not
// money. Never returns Accepted on rollback or unknown Commit. No auto-retry.
func (s *AdmissionStore) AdmitAppText(ctx context.Context, a AppAuthority, r AppTextRequest) (Accepted, error) {
	return s.admitText(ctx, a, r, false)
}

// AdmitGatewayTextF0 is the narrow HTTP/WS P0 admission path. The existing
// general ATC method retains F<=4 semantics; this check is inside its transaction.
func (s *AdmissionStore) AdmitGatewayTextF0(ctx context.Context, a AppAuthority, r AppTextRequest) (Accepted, error) {
	return s.admitText(ctx, a, r, true)
}
func (s *AdmissionStore) admitText(ctx context.Context, a AppAuthority, r AppTextRequest, f0 bool) (Accepted, error) {
	var accepted Accepted
	if ctx == nil {
		return accepted, admissionError("INVALID_INPUT")
	}
	if !a.trusted || r.DeviceID != a.device {
		return accepted, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	canonical, e := requestCanonical(r)
	if e != nil {
		return accepted, e
	}
	requestHash, keyHash := hashText(canonical), hashText(r.IdempotencyKey)
	if e = s.localGate(ctx); e != nil {
		return accepted, admissionError("AUTHORITY_REVOKED_OR_CHANGED")
	}
	e = s.runner.Run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(ctx context.Context, tx pgx.Tx) error {
		tenantBudget, e := lockScope(ctx, tx, a.tenant, "tenant", a.tenant)
		if e != nil {
			return e
		}
		original, originalHash, exists, e := findReceipt(ctx, tx, a, keyHash)
		if e != nil {
			return e
		}
		if exists {
			if f0 {
				var valid bool
				e = tx.QueryRow(ctx, `SELECT c.subscription_snapshot='[]'::jsonb AND (SELECT count(*) FROM admission_reservations r WHERE r.tenant=c.tenant AND r.command=c.id AND r.kind='result')=1 AND NOT EXISTS(SELECT 1 FROM admission_reservations r WHERE r.tenant=c.tenant AND r.command=c.id AND r.kind='callback') FROM commands c WHERE c.tenant=$1 AND c.id=$2`, a.tenant, original.CommandID).Scan(&valid)
				if e != nil {
					return e
				}
				if !valid {
					return admissionError("CAPABILITY_UNAVAILABLE")
				}
			}
			facts, e := authorize(ctx, tx, a)
			if e != nil {
				return e
			}
			again, h, still, e := findReceipt(ctx, tx, a, keyHash)
			if e != nil {
				return e
			}
			if !still || again != original || h != originalHash {
				return admissionError("INTEGRITY_CONFLICT")
			}
			if e = s.localGate(ctx); e != nil {
				return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
			}
			if _, e = decision(ctx, tx, facts); e != nil {
				return e
			}
			if h != requestHash {
				return admissionError("IDEMPOTENCY_CONFLICT")
			}
			accepted = again
			return nil
		}
		subs, e := subscriptions(ctx, tx, a, false)
		if e != nil {
			return e
		}
		if f0 && len(subs) != 0 {
			return admissionError("CAPABILITY_UNAVAILABLE")
		}
		if len(subs) > 4 {
			return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
		}
		budgets := make([]scopeBudget, len(subs))
		last := ""
		for i, v := range subs {
			if !boundedID(v.ID) || !boundedID(v.Destination) || v.Destination == last {
				return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
			}
			last = v.Destination
			budgets[i], e = lockScope(ctx, tx, a.tenant, "destination", v.Destination)
			if e != nil {
				return e
			}
		}
		facts, e := authorize(ctx, tx, a)
		if e != nil {
			return e
		}
		for _, v := range subs {
			var app, state string
			var policy, key int64
			var enabled bool
			e = tx.QueryRow(ctx, `SELECT app,state,policy_version,key_version,enabled FROM callback_destinations WHERE tenant=$1 AND id=$2 FOR SHARE`, a.tenant, v.Destination).Scan(&app, &state, &policy, &key, &enabled)
			if errors.Is(e, pgx.ErrNoRows) {
				return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
			}
			if e != nil {
				return e
			}
			if app != a.app || state != "active" || !enabled || policy != v.Policy || key != v.Key || v.Version < 1 || v.Version > maxSafeInteger || v.Policy < 1 || v.Policy > maxSafeInteger || v.Key < 1 || v.Key > maxSafeInteger || v.Attempts < 1 || v.Attempts > 4 || v.Event != "command.result" || v.Schema != "bounded-result-v1" || v.Events != 1 || v.Body != 65536 {
				return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
			}
		}
		locked, e := subscriptions(ctx, tx, a, true)
		if e != nil {
			return e
		}
		before, e := canonicalRestricted(snapshotValues(subs))
		if e != nil {
			return e
		}
		after, e := canonicalRestricted(snapshotValues(locked))
		if e != nil {
			return e
		}
		if before != after {
			return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
		}
		// A nonconforming writer bypassing the tenant gate must not yield a second graph.
		if _, _, exists, e = findReceipt(ctx, tx, a, keyHash); e != nil {
			return e
		} else if exists {
			return admissionError("INTEGRITY_CONFLICT")
		}
		var snapshotSize int
		e = tx.QueryRow(ctx, `SELECT octet_length(($1::jsonb)::text)`, before).Scan(&snapshotSize)
		if e != nil {
			return e
		}
		if snapshotSize > 16384 {
			return admissionError("SUBSCRIPTION_POLICY_UNAVAILABLE")
		}
		jobs, bytes := int64(1+len(subs)), int64(196608+len(subs)*90112)
		if e = checkBudget(tenantBudget, jobs, bytes); e != nil {
			return e
		}
		for _, b := range budgets {
			if e = checkBudget(b, 1, 90112); e != nil {
				return e
			}
		}
		if e = s.localGate(ctx); e != nil {
			return admissionError("AUTHORITY_REVOKED_OR_CHANGED")
		}
		now, e := decision(ctx, tx, facts)
		if e != nil {
			return e
		}
		ttl := r.ExpiresInSeconds * 1000
		if now > maxWireMillis-ttl {
			return admissionError("INVALID_INPUT")
		}
		expires := now + ttl
		wireExpires, e := wireTime(expires)
		if e != nil {
			return e
		}
		payload := map[string]any{"targetId": r.TargetID, "text": r.Text}
		payloadBytes, e := canonicalRestricted(payload)
		if e != nil {
			return e
		}
		commandBytes, e := commandCanonical(r, facts.policy, wireExpires)
		if e != nil {
			return e
		}
		snapDigestBytes, e := snapshotCanonical(a.tenant, a.app, subs)
		if e != nil {
			return e
		}
		ids := make([]string, 5+len(subs))
		for i := range ids {
			ids[i], e = freshID("ad_")
			if e != nil {
				return e
			}
		}
		if e = changeBudget(ctx, tx, a.tenant, "tenant", a.tenant, jobs, bytes); e != nil {
			return e
		}
		for _, v := range subs {
			if e = changeBudget(ctx, tx, a.tenant, "destination", v.Destination, 1, 90112); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO commands(tenant,id,device,actor,capability,payload,digest,policy,confirm,status,created,expires,actor_type,app_id,app_epoch,subscription_snapshot,subscription_snapshot_digest,admission_version) VALUES($1,$2,$3,$4,'message.send_text',$5,$6,$7,false,'created',$8,$9,'app',$10,$11,$12::jsonb,$13,'bounded-result-v1')`, a.tenant, ids[0], a.device, facts.creator, payloadBytes, hashText(commandBytes), facts.policy, now, expires, a.app, a.epoch, before, hashText(snapDigestBytes))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO developer_messages(tenant,app,batch,message,child,command,key_hash,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.tenant, a.app, ids[1], ids[2], ids[3], ids[0], keyHash, requestHash)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES($1,$2,$3,'result',0,'held',1,196608)`, a.tenant, ids[4], ids[0])
		if e != nil {
			return e
		}
		for i, v := range subs {
			_, e = tx.Exec(ctx, `INSERT INTO admission_reservations(tenant,reservation_id,command,kind,app,subscription_id,subscription_version,destination_id,destination_policy_version,key_version,max_attempts,event_ordinal,state,reserved_jobs,reserved_bytes) VALUES($1,$2,$3,'callback',$4,$5,$6,$7,$8,$9,$10,0,'held',1,90112)`, a.tenant, ids[5+i], ids[0], a.app, v.ID, v.Version, v.Destination, v.Policy, v.Key, v.Attempts)
			if e != nil {
				return e
			}
		}
		for _, step := range [][3]string{{"created", "authorize", "authorized"}, {"authorized", "enqueue", "queued"}} {
			event, e := freshID("evt_")
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO command_events(tenant,command,event_id,source,event,target,created) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.tenant, ids[0], event, step[0], step[1], step[2], now)
			if e != nil {
				return e
			}
			tag, e := tx.Exec(ctx, `UPDATE commands SET status=$3 WHERE tenant=$1 AND id=$2 AND status=$4`, a.tenant, ids[0], step[2], step[0])
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return admissionError("INTEGRITY_CONFLICT")
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO outbox(tenant,device,command,kind,state) VALUES($1,$2,$3,'command_ready','pending')`, a.tenant, a.device, ids[0])
		if e != nil {
			return e
		}
		audit, e := freshID("audit_")
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO audit(id,tenant,actor,action,target,outcome,created) VALUES($1,$2,$3,'developer.message_accepted',$4,'accepted',$5)`, audit, a.tenant, a.app, ids[0], now)
		if e != nil {
			return e
		}
		createdWire, e := wireTime(now)
		if e != nil {
			return e
		}
		accepted = Accepted{"accepted", "synthetic", ids[1], ids[2], ids[3], ids[0], createdWire, wireExpires}
		return nil
	})
	if e != nil {
		if errors.Is(e, ErrCommitOutcomeUnknown) {
			return Accepted{}, admissionError("COMMIT_OUTCOME_UNKNOWN")
		}
		var classified admissionError
		if errors.As(e, &classified) {
			return Accepted{}, classified
		}
		if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
			return Accepted{}, admissionError("STORE_TIMEOUT")
		}
		var pe *pgconn.PgError
		if errors.As(e, &pe) && (pe.Code == "57014" || pe.Code == "55P03") {
			return Accepted{}, admissionError("STORE_TIMEOUT")
		}
		return Accepted{}, admissionError("INTEGRITY_CONFLICT")
	}
	return accepted, nil
}
