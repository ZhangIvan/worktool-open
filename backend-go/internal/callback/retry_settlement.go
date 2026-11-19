package callback

import (
	"context"
	"errors"
	"time"
)

// RetryCompletionIntent identifies the immutable observation to settle. It has
// no caller-selected retry state or clock: the durable store owns that decision.
// Only retryable_failure and delivery_unknown use this optional contract.
type RetryCompletionIntent struct {
	Handle        Handle
	Outcome, Code string
	Status        int
}

func (RetryCompletionIntent) String() string { return "RetryCompletionIntent(redacted)" }

// RetryCompletionReceipt is a confirmed API result, not a stored replay token
// or send permission. NextAt is the authoritative schedule for ready, zero for
// dead_letter. A receipt returned alongside any error is unusable.
type RetryCompletionReceipt struct {
	Intent RetryCompletionIntent
	State  string
	NextAt time.Time
}

func (RetryCompletionReceipt) String() string { return "RetryCompletionReceipt(redacted)" }

type RetryCompletionStore interface {
	SettleRetry(context.Context, RetryCompletionIntent) (RetryCompletionReceipt, error)
}

// ObserveRetrySettlement proves only an already finished matching intent,
// including its original finished-time-derived schedule, slots and audit.
// It cannot mutate business rows or turn a missing attempt into a settlement.
type RetryCompletionObserver interface {
	ObserveRetrySettlement(context.Context, RetryCompletionIntent) (RetryCompletionReceipt, error)
}

func retryReceiptValid(r RetryCompletionReceipt, intent RetryCompletionIntent) bool {
	if r.Intent != intent {
		return false
	}
	switch r.State {
	case "ready":
		return intent.Handle.Attempt < intent.Handle.MaxAttempts && !r.NextAt.IsZero() && r.NextAt.UnixMilli() >= 0 && r.NextAt.UnixMilli() <= 9007199254740991
	case "dead_letter":
		return r.NextAt.IsZero()
	default:
		return false
	}
}

func (s *Sender) finishRetry(store RetryCompletionStore, d Delivery, sent bool, outcome, code string, status int) Result {
	intent := RetryCompletionIntent{Handle: d.Handle, Outcome: outcome, Code: code, Status: status}
	unconfirmed := Result{Claimed: true, TransportInvoked: sent, Reconcile: true, Code: "SETTLEMENT_UNCONFIRMED"}
	until := s.d.Clock.Now().Add(time.Second)
	cleanup, cancel := s.d.Clock.Deadline(context.Background(), until)
	receipt, err := store.SettleRetry(cleanup, intent)
	timely := s.live(cleanup, until)
	cancel()
	if errors.Is(err, ErrCommitUnknown) && !errors.Is(err, ErrCleanupUnconfirmed) {
		if observer, ok := s.d.Store.(RetryCompletionObserver); ok {
			until = s.d.Clock.Now().Add(time.Second)
			observe, stop := s.d.Clock.Deadline(context.Background(), until)
			receipt, err = observer.ObserveRetrySettlement(observe, intent)
			timely = s.live(observe, until)
			stop()
		}
	}
	// No legacy fallback or mutation retry, including for malformed/late receipts.
	if err != nil || !timely || !retryReceiptValid(receipt, intent) {
		return unconfirmed
	}
	return Result{Claimed: true, TransportInvoked: sent, State: receipt.State, Outcome: intent.Outcome, Code: intent.Code}
}
