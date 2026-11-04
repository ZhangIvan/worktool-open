# Bounded completion observation (default off)

This slice resolves only a lost acknowledgement of an already durable callback
completion. It does not resolve unknown Claim/Prepare commits, retry a mutation
or HTTP request, create send permission, restart a halted Worker, allocate a new
generation, release retention quota, or implement restore/PITR recovery. Startup
and worker defaults, migration 0009's old-history refusal, destination policies,
and all permission gates remain unchanged.

## One exact intent, one observation

Sender retains its original Completion, including handle, outcome, state, code,
status and NextAt. Only Complete returning ErrCommitUnknown without
ErrCleanupUnconfirmed allows one optional CompletionObserver call. Stores not
implementing that interface retain the previous unconfirmed result. A failed
rollback/discard keeps both the commit-unknown classification and a cleanup veto;
a later durable read cannot erase connection-cleanup uncertainty.

Complete and observation each get an independent cooperative 1-second context.
Observation may run after cancellation of the original send context, but nil
returned after its own deadline is not accepted. No retry, background poll or
replacement goroutine is created. Worker Stop joins the real call and keeps
Active true if the join is still pending. See worker_lifecycle.md for the
conservative additive **2 seconds + 6C** remaining cleanup allowance.

A confirmed observation returns the original bounded Result. A durable
`delivery_unknown` attempt remains `delivery_unknown`; confirming its database
settlement does not establish receiver effects or exactly-once delivery. Ready
state uses the existing durable attempt/backoff rules on a later authorized
claim, never an immediate resend in this RunOne invocation.

## PostgreSQL proof and failure boundary

CallbackStore forwards to CallbackCompleter.ObserveCompletion. Both completion
and observation share the existing locked validator, but observation exits
before the unfinished-attempt slot/DML path. It reads the current exact frozen
graph, attempt and delivery, using tenant → destination → runtime → delivery →
attempt locks. Finished attempts must match the original owner, generation,
fence, attempt count/cap, outcome, status and code. Delivery state, timestamps,
retry schedule and released lease must agree, and no matching old slot tuple
may remain. Slots reassigned to other deliveries are not locked or changed.

Observation additionally locks the deterministic completion audit row and checks
its tenant/ID, actor, action, target, state:code and created timestamp against the
same attempt's finished time. Missing history or inconsistent evidence is a
failure, never permission to reconstruct history or Complete again. The retry
NextAt value remains an intent-shape check; actual available_at must equal the
original finished_at plus the existing attempt backoff, not a later wall clock.
An expired former lease is compatible with a proven earlier completion. A new
fence/runtime generation is not.

These are ordinary read-committed transactions with SELECT FOR UPDATE/SHARE,
**not PostgreSQL READ ONLY transactions**. No business INSERT/UPDATE/DELETE is
reachable in observation. Locks and the Runner still consume bounded resources.
Database time and the local generation gate are checked after the final audit
lock. After Runner commit and cleanup return successfully, the local gate and
caller context are checked again. Read, commit, rollback, discard, context or
authority uncertainty leaves the Sender unconfirmed and the Worker halted.
Existing Complete finished-duplicate behavior is unchanged; its nil return is
not substituted for the stronger observation proof.

## Evidence and limits

Pure/race cases cover exact intent preservation, one-shot calls, representative finite
result classes, unknown receiver effects, cancellation and late nil returns,
cleanup veto/classification, Worker join/halt/no restart, matching durable
attempt/delivery/audit, stale generation/fence, missing/inconsistent history,
released/reassigned slots and failure at read/commit/cleanup boundaries.
Scripted pgx fakes are not SQL or lock evidence.

New gated children remain within existing CB02 and CB05; no new top-level
allowlist entry is required. They cover unfinished/missing/conflicting proof,
busy audit locks, unchanged xmin/rows, and the actual Sender/Runner/store after
a deterministic error following real COMMIT or real ROLLBACK. The fault adapter
only intercepts completion transactions. Its transport is test-only HMAC
verification with no added socket. This is not a TCP failure or process crash
test. These new PostgreSQL children are **NOT_RUN** locally and require the
existing isolated integration gate. Prior 43-group runs validate only their
recorded commits, not this working-tree change. No production capacity,
cross-generation recovery, retention GC or real-device evidence is claimed.
