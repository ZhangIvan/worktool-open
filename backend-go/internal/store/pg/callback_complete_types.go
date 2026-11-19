package pg

import (
	"errors"
	"time"
)

// CallbackCompletionHandle is internal worker identity, not an HTTP DTO. It is
// deliberately separate from device-command LeaseHandle and the frozen sender.
type CallbackCompletionHandle struct {
	Tenant, DeliveryID, Owner, Recovery string
	Fence                               int64
	Attempt, MaxAttempts                int
}

func (CallbackCompletionHandle) String() string { return "CallbackCompletionHandle(redacted)" }

// CallbackCompletion mirrors the frozen sender's completion projection. NextAt
// is only a nonzero/zero intent check; PostgreSQL determines the actual retry time.
// Status zero means SQL NULL, never an HTTP status code zero in storage.
type CallbackCompletion struct {
	Handle               CallbackCompletionHandle
	Outcome, State, Code string
	Status               int
	NextAt               time.Time
}

func (CallbackCompletion) String() string { return "CallbackCompletion(redacted)" }

var (
	ErrCallbackCompletionInvalid     = errors.New("CALLBACK_COMPLETION_INVALID")
	ErrCallbackCompletionStale       = errors.New("CALLBACK_COMPLETION_STALE")
	ErrCallbackCompletionConflict    = errors.New("CALLBACK_COMPLETION_CONFLICT")
	ErrCallbackCompletionIntegrity   = errors.New("CALLBACK_COMPLETION_INTEGRITY")
	ErrCallbackCompletionUnavailable = errors.New("CALLBACK_COMPLETION_UNAVAILABLE")
	ErrCallbackCompletionUnknown     = errors.New("CALLBACK_COMPLETION_COMMIT_UNKNOWN")
)

// The finite code matrix is the boundary to callback_attempts CHECK constraints.
// No caller-selected SQL, arbitrary outcome, or raw adapter error is accepted.
func validateCallbackCompletion(c CallbackCompletion) (prepared bool, err error) {
	h := c.Handle
	if !asciiIdentifier(h.Tenant) || !asciiIdentifier(h.DeliveryID) || !asciiIdentifier(h.Owner) || !asciiIdentifier(h.Recovery) || h.Fence <= 0 || h.Attempt < 1 || h.MaxAttempts < 1 || h.MaxAttempts > 4 || h.Attempt > h.MaxAttempts {
		return false, ErrCallbackCompletionInvalid
	}
	retry := c.State == "ready" || c.State == "dead_letter"
	if (c.State == "ready") != !c.NextAt.IsZero() {
		return false, ErrCallbackCompletionInvalid
	}
	ok := false
	switch c.Code {
	case "HTTP_ACKNOWLEDGED":
		prepared, ok = true, c.Outcome == "delivered" && c.State == "delivered" && c.Status >= 200 && c.Status <= 299
	case "HTTP_RETRYABLE":
		prepared, ok = true, c.Outcome == "retryable_failure" && retry && (c.Status == 408 || c.Status == 425 || c.Status == 429 || c.Status >= 500 && c.Status <= 599)
	case "HTTP_PERMANENT":
		prepared, ok = true, c.Outcome == "permanent_failure" && c.State == "dead_letter" && c.Status >= 400 && c.Status <= 499 && c.Status != 408 && c.Status != 425 && c.Status != 429
	case "REDIRECT_DENIED":
		prepared, ok = true, c.Outcome == "permanent_failure" && c.State == "dead_letter" && c.Status >= 300 && c.Status <= 399
	case "HTTP_STATUS_INVALID", "TRANSPORT_OUTCOME_UNKNOWN":
		prepared, ok = true, c.Outcome == "delivery_unknown" && retry && c.Status == 0
	case "TRANSPORT_POLICY_DENIED", "SIGNATURE_INPUT_INVALID":
		prepared, ok = true, c.Outcome == "permanent_failure" && c.State == "dead_letter" && c.Status == 0
	case "PERMIT_EXPIRED":
		prepared, ok = true, c.Outcome == "retryable_failure" && retry && c.Status == 0
	case "AUTHORITY_DENIED", "LOCAL_AUTHORITY_DENIED":
		ok = c.Outcome == "permanent_failure" && c.State == "cancelled" && c.Status == 0
	case "DESTINATION_DENIED", "DNS_ANSWER_DENIED", "KEY_BINDING_INVALID":
		ok = c.Outcome == "permanent_failure" && c.State == "dead_letter" && c.Status == 0
	case "CLAIM_EXPIRED", "KEY_PROVIDER_UNAVAILABLE", "DNS_UNAVAILABLE", "LOCAL_GATE_UNAVAILABLE", "PREPARATION_EXPIRED":
		ok = c.Outcome == "retryable_failure" && retry && c.Status == 0
	}
	if !ok {
		return false, ErrCallbackCompletionInvalid
	}
	return prepared, nil
}
