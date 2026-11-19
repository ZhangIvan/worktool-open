package callback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/ZhangIvan/worktool-open/backend-go/internal/callback/security"
)

type Sender struct {
	d        Dependencies
	policies map[Binding]*security.Policy
}

func New(d Dependencies, policies []Policy) (*Sender, error) {
	if d.Store == nil || d.Clock == nil || d.Gate == nil || d.DNS == nil || d.Keys == nil || d.Transport == nil || len(policies) == 0 || len(policies) > 64 {
		return nil, errors.New("SENDER_CONFIGURATION_INVALID")
	}
	s := &Sender{d: d, policies: make(map[Binding]*security.Policy)}
	for _, p := range policies {
		if !bindingValid(p.Binding) {
			return nil, errors.New("SENDER_CONFIGURATION_INVALID")
		}
		if _, exists := s.policies[p.Binding]; exists {
			return nil, errors.New("SENDER_CONFIGURATION_INVALID")
		}
		compiled, err := security.NewPolicy([]security.Rule{{URL: p.Binding.URL, CIDRs: p.CIDRs}})
		if err != nil {
			return nil, errors.New("SENDER_CONFIGURATION_INVALID")
		}
		s.policies[p.Binding] = compiled
	}
	return s, nil
}

func idValid(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func bindingValid(b Binding) bool {
	if !idValid(b.Tenant) || !idValid(b.Destination) || len(b.KeyRef) == 0 || len(b.KeyRef) > 256 || b.PolicyVersion <= 0 || b.KeyVersion <= 0 {
		return false
	}
	for _, c := range []byte(b.KeyRef) {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

func snapshotValid(d Delivery) bool {
	h := d.Handle
	if !idValid(h.Tenant) || !idValid(h.DeliveryID) || !idValid(h.Owner) || !idValid(h.Recovery) || h.Fence <= 0 || h.Attempt < 1 || h.MaxAttempts < 1 || h.MaxAttempts > 4 || h.Attempt > h.MaxAttempts || !idValid(d.EventID) || !bindingValid(d.Binding) || d.Binding.Tenant != h.Tenant || len(d.Body) == 0 || len(d.Body) > 4096 {
		return false
	}
	digest := sha256.Sum256(d.Body)
	return hex.EncodeToString(digest[:]) == d.Digest && !d.LeaseUntil.IsZero() && !d.HardDeadline.IsZero() && !d.ExpiresAt.IsZero() && !d.LeaseUntil.After(d.HardDeadline) && !d.HardDeadline.After(d.ExpiresAt)
}

func earliest(t time.Time, rest ...time.Time) time.Time {
	for _, v := range rest {
		if v.Before(t) {
			t = v
		}
	}
	return t
}

func (s *Sender) live(ctx context.Context, deadline time.Time) bool {
	return ctx.Err() == nil && s.d.Clock.Now().Before(deadline)
}

func (s *Sender) localCheck(ctx context.Context, d Delivery, deadline time.Time) (bool, Result) {
	err := s.d.Gate.Check(ctx, d.Handle)
	// Generation uncertainty dominates elapsed time: never settle from a runtime
	// whose current recovery authority is itself unknown, even during cleanup.
	if errors.Is(err, ErrStale) || errors.Is(err, ErrCommitUnknown) {
		return false, Result{Claimed: true, Reconcile: true, Code: "LOCAL_GENERATION_UNCONFIRMED"}
	}
	if !s.live(ctx, deadline) {
		return false, s.finish(d, false, "retryable_failure", "PREPARATION_EXPIRED", 0)
	}
	if err == nil {
		return true, Result{}
	}
	if errors.Is(err, ErrDenied) {
		return false, s.finish(d, false, "cancelled", "LOCAL_AUTHORITY_DENIED", 0)
	}
	return false, s.finish(d, false, "retryable_failure", "LOCAL_GATE_UNAVAILABLE", 0)
}

// RunOne never retries a mutation or transport call or claims another delivery.
// Caller may invoke it for later claims, subject to the durable attempt budget.
func (s *Sender) RunOne(ctx context.Context) Result {
	if ctx == nil || ctx.Err() != nil {
		return Result{Code: "CONTEXT_CLOSED"}
	}
	d, found, err := s.d.Store.Claim(ctx)
	if err != nil {
		return Result{Code: "CLAIM_FAILED", Reconcile: errors.Is(err, ErrCommitUnknown)}
	}
	if !found {
		return Result{Code: "NO_READY_DELIVERY"}
	}
	if len(d.Body) == 0 || len(d.Body) > 4096 {
		return Result{Claimed: true, Reconcile: true, Code: "INVALID_SNAPSHOT"}
	}
	// Snapshot caller-owned bytes before any other adapter can be invoked.
	d.Body = append([]byte(nil), d.Body...)
	if !snapshotValid(d) || d.HardDeadline.After(s.d.Clock.Now().Add(10*time.Second)) {
		return Result{Claimed: true, Reconcile: true, Code: "INVALID_SNAPSHOT"}
	}
	deadline := earliest(d.LeaseUntil, d.HardDeadline, d.ExpiresAt)
	claimCtx, cancel := s.d.Clock.Deadline(ctx, deadline)
	defer cancel()
	if !s.live(claimCtx, deadline) {
		return s.finish(d, false, "retryable_failure", "CLAIM_EXPIRED", 0)
	}
	if ok, result := s.localCheck(claimCtx, d, deadline); !ok {
		return result
	}
	policy := s.policies[d.Binding]
	target, err := policy.Authorize(d.Binding.URL)
	if err != nil {
		return s.finish(d, false, "permanent_failure", "DESTINATION_DENIED", 0)
	}
	key, err := s.d.Keys.Resolve(claimCtx, d.Binding)
	defer clear(key.Bytes)
	if err != nil {
		return s.finish(d, false, "retryable_failure", "KEY_PROVIDER_UNAVAILABLE", 0)
	}
	if key.Binding != d.Binding || len(key.Bytes) != 32 || !key.ExpiresAt.After(s.d.Clock.Now()) {
		return s.finish(d, false, "permanent_failure", "KEY_BINDING_INVALID", 0)
	}
	if !s.live(claimCtx, deadline) {
		return s.finish(d, false, "retryable_failure", "PREPARATION_EXPIRED", 0)
	}
	// DNS starts the single network budget; it cannot be extended by a permit.
	networkDeadline := earliest(s.d.Clock.Now().Add(2*time.Second), deadline, key.ExpiresAt)
	networkCtx, stop := s.d.Clock.Deadline(claimCtx, networkDeadline)
	defer stop()
	answers, err := s.d.DNS.Resolve(networkCtx, target.Host())
	if err != nil {
		return s.finish(d, false, "retryable_failure", "DNS_UNAVAILABLE", 0)
	}
	if !s.live(networkCtx, networkDeadline) {
		return s.finish(d, false, "retryable_failure", "PREPARATION_EXPIRED", 0)
	}
	pins, err := target.Pin(answers)
	if err != nil {
		return s.finish(d, false, "permanent_failure", "DNS_ANSWER_DENIED", 0)
	}
	prepared := Prepared{Handle: d.Handle, Binding: d.Binding, EventID: d.EventID, Digest: d.Digest, IP: pins[0], Deadline: networkDeadline}
	permit, err := s.d.Store.Prepare(networkCtx, prepared)
	if err != nil {
		// No permission on uncertain commit or stale lease; recovery owns it.
		if errors.Is(err, ErrCommitUnknown) || errors.Is(err, ErrStale) {
			return Result{Claimed: true, Reconcile: true, Code: "PREPARE_UNCERTAIN_OR_STALE"}
		}
		if errors.Is(err, ErrDenied) {
			return s.finish(d, false, "cancelled", "AUTHORITY_DENIED", 0)
		}
		return Result{Claimed: true, Reconcile: true, Code: "PREPARE_FAILED"}
	}
	if !permit.Fresh || permit.Prepared != prepared {
		return Result{Claimed: true, Reconcile: true, Code: "PERMIT_MISMATCH_OR_REPLAY"}
	}
	if !s.live(networkCtx, networkDeadline) {
		return s.finish(d, false, "retryable_failure", "PERMIT_EXPIRED", 0)
	}
	now := s.d.Clock.Now().UnixMilli()
	requestBody := append([]byte(nil), d.Body...)
	signature, err := security.Sign(key.Bytes, d.EventID, now, requestBody)
	clear(key.Bytes)
	if err != nil {
		return s.finish(d, false, "permanent_failure", "SIGNATURE_INPUT_INVALID", 0)
	}
	if ok, result := s.localCheck(networkCtx, d, networkDeadline); !ok {
		return result
	}
	if !s.live(networkCtx, networkDeadline) {
		return s.finish(d, false, "retryable_failure", "PERMIT_EXPIRED", 0)
	}
	request := Request{URL: target.URL(), Host: target.Host(), IP: pins[0], Method: "POST", ContentType: "application/json", ContentLength: len(requestBody), EventID: d.EventID, Timestamp: strconv.FormatInt(now, 10), Signature: signature, Body: requestBody}
	response, err := s.d.Transport.Send(networkCtx, request)
	if err != nil || !s.live(networkCtx, networkDeadline) {
		if s.live(networkCtx, networkDeadline) && errors.Is(err, ErrTransportPolicy) {
			return s.finish(d, true, "permanent_failure", "TRANSPORT_POLICY_DENIED", 0)
		}
		return s.finish(d, true, "delivery_unknown", "TRANSPORT_OUTCOME_UNKNOWN", 0)
	}
	status := response.Status
	switch {
	case status >= 200 && status <= 299:
		return s.finish(d, true, "delivered", "HTTP_ACKNOWLEDGED", status)
	case status >= 300 && status <= 399:
		return s.finish(d, true, "permanent_failure", "REDIRECT_DENIED", status)
	case status == 408 || status == 425 || status == 429 || status >= 500 && status <= 599:
		return s.finish(d, true, "retryable_failure", "HTTP_RETRYABLE", status)
	case status >= 400 && status <= 499:
		return s.finish(d, true, "permanent_failure", "HTTP_PERMANENT", status)
	default:
		return s.finish(d, true, "delivery_unknown", "HTTP_STATUS_INVALID", 0)
	}
}

func (s *Sender) finish(d Delivery, sent bool, outcome, code string, status int) Result {
	if outcome == "retryable_failure" || outcome == "delivery_unknown" {
		if store, ok := s.d.Store.(RetryCompletionStore); ok {
			return s.finishRetry(store, d, sent, outcome, code, status)
		}
	}
	c := Completion{Handle: d.Handle, Outcome: outcome, Code: code, Status: status}
	now := s.d.Clock.Now()
	switch outcome {
	case "delivered":
		c.State = "delivered"
	case "cancelled":
		// Existing callback_attempts CHECK has no 'cancelled' outcome. Delivery
		// cancellation is terminal refusal of this attempt, without new SQL enum.
		c.Outcome = "permanent_failure"
		c.State = "cancelled"
	case "permanent_failure":
		c.State = "dead_letter"
	default:
		c.NextAt = now.Add(time.Second << (d.Handle.Attempt - 1))
		if d.Handle.Attempt >= d.Handle.MaxAttempts || !c.NextAt.Before(d.ExpiresAt) {
			c.State, c.NextAt = "dead_letter", time.Time{}
		} else {
			c.State = "ready"
		}
	}
	// Independent bounded cleanup; cancellation is not evidence of rollback.
	cleanup, cancel := s.d.Clock.Deadline(context.Background(), now.Add(time.Second))
	defer cancel()
	err := s.d.Store.Complete(cleanup, c)
	cancel()
	if errors.Is(err, ErrCommitUnknown) && !errors.Is(err, ErrCleanupUnconfirmed) {
		if observer, ok := s.d.Store.(CompletionObserver); ok {
			// Preserve the exact intent. Observation is neither a Complete retry
			// nor a new send permission, even after the send context is cancelled.
			until := s.d.Clock.Now().Add(time.Second)
			observe, stop := s.d.Clock.Deadline(context.Background(), until)
			defer stop()
			observed := observer.ObserveCompletion(observe, c)
			confirmed := observed == nil && s.live(observe, until)
			stop()
			if confirmed {
				err = nil
			}
		}
	}
	if err != nil {
		return Result{Claimed: true, TransportInvoked: sent, Reconcile: true, Code: "SETTLEMENT_UNCONFIRMED"}
	}
	return Result{Claimed: true, TransportInvoked: sent, State: c.State, Outcome: c.Outcome, Code: code}
}
