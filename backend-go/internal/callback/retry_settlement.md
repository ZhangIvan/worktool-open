# Authoritative retry settlement (optional, no deployment activation)

The PostgreSQL CallbackStore implements RetryCompletionStore and
RetryCompletionObserver. Sender uses them only for retryable_failure and
delivery_unknown outcomes. Other outcomes and stores without the optional
interface retain the strict legacy Complete/ObserveCompletion path.

## Why the retry decision belongs to the store

Claim's conservative local deadlines subtract a safety margin and elapsed call
time. They continue to bound send authority exactly as before. They are not an
exact database retry horizon. Also, completion locks can take time after Sender
has received the response. Predicting ready/dead_letter locally can therefore
disagree with the final locked database clock in either direction and halt the
worker on a valid response.

RetryCompletionIntent contains only the exact Handle, Outcome, Code and Status.
It has no caller-selected state or schedule. The existing finite code/status
matrix, preparation evidence, immutable graph, current generation, exact slots,
live lease/hard deadline and delivery expiry all remain required. At the final
locked database clock, the store derives ready plus the existing attempt backoff,
or dead_letter at the attempt cap or when next >= expiry. The attempt, delivery,
released slots and deterministic audit still commit atomically.

RetryCompletionReceipt returns that exact intent and confirmed State/NextAt.
It is an API result, unrelated to the persisted first-result
callback_replay_receipts. It provides neither send authority nor proof of
receiver effects. A receipt accompanying any error is discarded. Sender
validates intent equality and finite state/schedule shape, then reports the
actual durable state. A malformed or late result requires reconciliation.

## Unknown commit and observation

Sender never falls back to legacy Complete after invoking SettleRetry. It never
retries a mutation or transport call. Only ErrCommitUnknown without
ErrCleanupUnconfirmed permits one ObserveRetrySettlement call, if supported.
Legacy ObserveCompletion is not a fallback. Settlement and observation each use
a fresh independent cooperative one-second context; worker Stop joins both.

Observation shares the existing locked evidence validator and cannot reach
business DML. It derives state and NextAt from the original finished_at plus the
attempt backoff and stored expiry, not observation time. It validates exact
attempt intent and ownership, state/schedule, released lease/slots, immutable
graph and deterministic audit, then requires clean Runner completion and the
post-cleanup generation/context checks. Missing/conflicting evidence, changed
fence/runtime, late reads or cleanup uncertainty produce no receipt. Observing
a durable delivery_unknown settlement still does not establish receiver effects.

Legacy explicit Completion continues to reject state disagreement. No deadline
was widened and no migration, retry budget, startup activation, recovery daemon,
worker restart, cross-generation recovery or production provider was added.

## Evidence scope

The same baseline-compatible Sender/mapper/scripted-pgx regression exposes both
boundary failures in the old source and passes with this implementation. New
unit cases cover retry equality/caps, immutable-intent observation after expiry,
unknown commit, corruption/authority/slot/audit rejection, receipt validation,
no legacy fallback and worker join/halt behavior. Scripted pgx tests execute
production control flow, not PostgreSQL SQL/locks. DNS, keys and transport in
these cases are in-memory; no receiver effect or real network is claimed.

Full offline unit/race/vet/build results and exact source hashes are recorded in
the external candidate verification package. Gated real-PG groups remain
NOT_RUN for this candidate. Previous commits' PostgreSQL evidence is historical,
not evidence for these new methods. This is not deployment or device acceptance.
