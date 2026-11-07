package pg

import (
	"context"
	cb "github.com/ZhangIvan/worktool-open/backend-go/internal/callback"
	"time"
)

var _ cb.RetryCompletionStore = (*CallbackStore)(nil)
var _ cb.RetryCompletionObserver = (*CallbackStore)(nil)

func (s *CallbackStore) SettleRetry(ctx context.Context, intent cb.RetryCompletionIntent) (cb.RetryCompletionReceipt, error) {
	return s.completer.settleRetry(ctx, intent, false)
}
func (s *CallbackStore) ObserveRetrySettlement(ctx context.Context, intent cb.RetryCompletionIntent) (cb.RetryCompletionReceipt, error) {
	return s.completer.settleRetry(ctx, intent, true)
}

// State/NextAt below are validation placeholders, never caller input or SQL
// decisions. The shared locked validator derives the actual retry state/time.
// Legacy Complete/ObserveCompletion keep their exact caller-state contract.
func (s *CallbackCompleter) settleRetry(ctx context.Context, intent cb.RetryCompletionIntent, observeOnly bool) (cb.RetryCompletionReceipt, error) {
	if intent.Outcome != "retryable_failure" && intent.Outcome != "delivery_unknown" {
		return cb.RetryCompletionReceipt{}, callbackStoreError(ErrCallbackCompletionInvalid)
	}
	c := callbackCompletionIntent(cb.Completion{Handle: intent.Handle, Outcome: intent.Outcome, Code: intent.Code, Status: intent.Status, State: "ready", NextAt: time.Unix(0, 1)})
	var resolved CallbackCompletion
	if err := s.complete(ctx, c, observeOnly, &resolved); err != nil {
		return cb.RetryCompletionReceipt{}, callbackStoreError(err)
	}
	return cb.RetryCompletionReceipt{Intent: intent, State: resolved.State, NextAt: resolved.NextAt}, nil
}
