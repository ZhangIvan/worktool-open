package pg

import (
	"context"
	"errors"
	"time"

	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
	"github.com/jackc/pgx/v5"
)

// CallbackStore is explicit trusted worker composition. No daemon, network or
// HTTP route is registered. Policies are copied and bounded at construction.
type CallbackStore struct {
	runner       *Runner
	runtime      RuntimeAuthority
	gate         func(context.Context) error
	completer    *CallbackCompleter
	policies     map[string]cb.Policy
	destinations *security.Policy
	now          func() time.Time
}

func NewCallbackStore(r *Runner, runtime RuntimeAuthority, gate func(context.Context) error, policies []cb.Policy) (*CallbackStore, error) {
	if r == nil || !runtime.trusted || gate == nil || len(policies) == 0 || len(policies) > 32 {
		return nil, cb.ErrDenied
	}
	copied := make(map[string]cb.Policy, len(policies))
	rules := make([]security.Rule, 0, len(policies))
	urls := map[string][]string{}
	for _, p := range policies {
		b := p.Binding
		if !asciiIdentifier(b.Tenant) || !asciiIdentifier(b.Destination) || !callbackKeyRef(b.KeyRef) || b.PolicyVersion < 1 || b.KeyVersion < 1 {
			return nil, cb.ErrDenied
		}
		key := b.Tenant + "\x00" + b.Destination
		if _, ok := copied[key]; ok {
			return nil, cb.ErrDenied
		}
		p.CIDRs = append([]string(nil), p.CIDRs...)
		copied[key] = p
		// URL sharing with a different network policy is rejected, not merged.
		if previous, ok := urls[b.URL]; ok {
			if len(previous) != len(p.CIDRs) {
				return nil, cb.ErrDenied
			}
			for i := range previous {
				if previous[i] != p.CIDRs[i] {
					return nil, cb.ErrDenied
				}
			}
		} else {
			urls[b.URL] = p.CIDRs
			rules = append(rules, security.Rule{URL: b.URL, CIDRs: p.CIDRs})
		}
	}
	destinations, e := security.NewPolicy(rules)
	if e != nil {
		return nil, cb.ErrDenied
	}
	completer, e := NewCallbackCompleter(r, runtime, gate)
	if e != nil {
		return nil, e
	}
	return &CallbackStore{r, runtime, gate, completer, copied, destinations, time.Now}, nil
}
func callbackKeyRef(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

var errCallbackCandidateBusy = errors.New("CALLBACK_CANDIDATE_BUSY")

func callbackStoreError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, ErrCommitOutcomeUnknown) || errors.Is(e, ErrCallbackCompletionUnknown) {
		return cb.ErrCommitUnknown
	}
	if errors.Is(e, cb.ErrDenied) {
		return cb.ErrDenied
	}
	if errors.Is(e, cb.ErrStale) || errors.Is(e, ErrCallbackCompletionStale) {
		return cb.ErrStale
	}
	return errors.New("CALLBACK_STORE_UNAVAILABLE")
}
func (s *CallbackStore) Complete(ctx context.Context, c cb.Completion) error {
	h := c.Handle
	e := s.completer.Complete(ctx, CallbackCompletion{Handle: CallbackCompletionHandle{Tenant: h.Tenant, DeliveryID: h.DeliveryID, Owner: h.Owner, Recovery: h.Recovery, Fence: h.Fence, Attempt: h.Attempt, MaxAttempts: h.MaxAttempts}, Outcome: c.Outcome, State: c.State, Code: c.Code, Status: c.Status, NextAt: c.NextAt})
	return callbackStoreError(e)
}

// Scope locks serialize policy changes and quota scheduling across processes.
func (s *CallbackStore) lockScopes(ctx context.Context, tx pgx.Tx, tenant, dest string) error {
	return s.lockScopeMode(ctx, tx, tenant, dest, false)
}
func (s *CallbackStore) lockScopeMode(ctx context.Context, tx pgx.Tx, tenant, dest string, skip bool) error {
	var id string
	tenantSQL, destSQL := callbackLockTenant, callbackLockDestination
	if skip {
		tenantSQL += " SKIP LOCKED"
		destSQL += " SKIP LOCKED"
	}
	if e := tx.QueryRow(ctx, tenantSQL, tenant).Scan(&id); e != nil {
		if skip && errors.Is(e, pgx.ErrNoRows) {
			return errCallbackCandidateBusy
		}
		return e
	}
	if id != tenant {
		return cb.ErrStale
	}
	if e := tx.QueryRow(ctx, destSQL, tenant, dest).Scan(&id); e != nil {
		if skip && errors.Is(e, pgx.ErrNoRows) {
			return errCallbackCandidateBusy
		}
		return e
	}
	if id != dest {
		return cb.ErrStale
	}
	if e := lockRuntime(ctx, tx, s.runtime, "worker"); e != nil {
		return cb.ErrStale
	}
	return nil
}

// The immutable version is resolved by the frozen outbox tuple, not the current
// pointer. The same row lock fences per-version revocation at Claim and Prepare.
const callbackReadDestinationVersion = `SELECT canonical_url,key_ref,revoked_at IS NULL FROM callback_destination_versions WHERE tenant=$1 AND app=$2 AND destination_id=$3 AND policy_version=$4 AND key_version=$5 FOR SHARE`

// authority locks precede the delivery row; result evidence is not modified.
// Revocation does not invalidate Complete, which settles an already-issued send.
type callbackAuthority struct {
	valid   bool
	expires int64
	binding cb.Binding
}

func (s *CallbackStore) lockAuthority(ctx context.Context, tx pgx.Tx, tenant, id, dest string) (callbackAuthority, error) {
	var a callbackAuthority
	var app, device, command, sub string
	var appEpoch, deviceEpoch, version, policyVersion, keyVersion int64
	e := tx.QueryRow(ctx, `SELECT app,device,command,app_epoch,device_epoch,subscription_id,subscription_version,policy_version,key_version FROM callback_outbox WHERE tenant=$1 AND delivery_id=$2 AND destination_id=$3`, tenant, id, dest).Scan(&app, &device, &command, &appEpoch, &deviceEpoch, &sub, &version, &policyVersion, &keyVersion)
	if e != nil {
		return a, e
	}
	var appOK, deviceOK, bound, destOK, versionOK, subOK bool
	var epoch int64
	e = tx.QueryRow(ctx, `SELECT active AND scopes ? 'callbacks:read' AND scopes ? 'results:read',auth_epoch,token_expires FROM developer_apps WHERE tenant=$1 AND id=$2 FOR SHARE`, tenant, app).Scan(&appOK, &epoch, &a.expires)
	if e != nil {
		return a, e
	}
	appOK = appOK && epoch == appEpoch
	e = tx.QueryRow(ctx, `SELECT state='active',epoch FROM devices WHERE tenant=$1 AND id=$2 FOR SHARE`, tenant, device).Scan(&deviceOK, &epoch)
	if e != nil {
		return a, e
	}
	deviceOK = deviceOK && epoch == deviceEpoch
	var found string
	e = tx.QueryRow(ctx, `SELECT device FROM developer_bindings WHERE tenant=$1 AND app=$2 AND device=$3 FOR SHARE`, tenant, app, device).Scan(&found)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return a, e
	}
	bound = e == nil
	a.binding.Tenant = tenant
	a.binding.Destination = dest
	// Current config only determines eligibility, never the historical target.
	e = tx.QueryRow(ctx, `SELECT state='active' AND policy_version=$4 AND key_version=$5 FROM callback_destinations WHERE tenant=$1 AND id=$2 AND app=$3 FOR SHARE`, tenant, dest, app, policyVersion, keyVersion).Scan(&destOK)
	if e != nil {
		return a, e
	}
	a.binding.PolicyVersion, a.binding.KeyVersion = policyVersion, keyVersion
	e = tx.QueryRow(ctx, callbackReadDestinationVersion, tenant, app, dest, policyVersion, keyVersion).Scan(&a.binding.URL, &a.binding.KeyRef, &versionOK)
	if e != nil {
		return a, e
	}
	e = tx.QueryRow(ctx, `SELECT retired_at IS NULL AND destination_policy_version=$6 AND key_version=$7 FROM callback_subscription_versions WHERE tenant=$1 AND app=$2 AND subscription_id=$3 AND version=$4 AND destination_id=$5 FOR SHARE`, tenant, app, sub, version, dest, policyVersion, keyVersion).Scan(&subOK)
	if e != nil {
		return a, e
	}
	e = tx.QueryRow(ctx, `SELECT id FROM commands WHERE tenant=$1 AND app_id=$2 AND device=$3 AND id=$4 AND result IS NOT NULL FOR SHARE`, tenant, app, device, command).Scan(&found)
	if e != nil {
		return a, e
	}
	p, ok := s.policies[tenant+"\x00"+dest]
	a.valid = ok && p.Binding == a.binding && appOK && deviceOK && bound && destOK && versionOK && subOK
	return a, nil
}
func callbackReadLocked(ctx context.Context, tx pgx.Tx, tenant, id string) (callbackDeliveryRow, error) {
	var d callbackDeliveryRow
	e := tx.QueryRow(ctx, callbackReadDelivery, tenant, id).Scan(&d.destination, &d.state, &d.fence, &d.attempts, &d.maxAttempts, &d.owner, &d.recovery, &d.lease, &d.claimed, &d.hard, &d.expires, &d.delivered, &d.available, &d.body, &d.digest, &d.lastCode, &d.graph, &d.eventBody)
	if e != nil {
		return d, e
	}
	if !d.graph || len(d.body) == 0 || len(d.body) > 4096 || string(d.body) != string(d.eventBody) || hashText(string(d.body)) != d.digest {
		return d, cb.ErrDenied
	}
	return d, nil
}

// bridge subtracts the entire call duration plus a conservative 100ms margin.
// It is a short local deadline, not evidence that production UTC is healthy.
func callbackLocalDeadline(start, end, pgNow, expiry time.Time) (time.Time, error) {
	elapsed := end.Sub(start)
	left := expiry.Sub(pgNow) - elapsed - 100*time.Millisecond
	if elapsed < 0 || left <= 0 {
		return time.Time{}, cb.ErrStale
	}
	return end.Add(left), nil
}
