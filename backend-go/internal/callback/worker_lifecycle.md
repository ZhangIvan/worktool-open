# Default-off same-generation callback worker

This is source-only trusted composition. Startup remains synthetic/F0. No
command-line, HTTP, environment, database toggle or registration enables it. No
secret provider, DNS provider, production generation/UTC authority, pool,
listener or deployment is created. Importing or constructing it starts no work.

## Composition and resources

Build the existing `pg.CallbackStore` with a trusted runtime, local generation
gate, bounded Runner and copied reviewed policies. Build the existing concrete
`callback.Sender` around that store and explicit Clock, LocalGate, DNS, Keys and
Transport adapters. `NewWorker(sender, config)` obtains typed `RecoveryStore`
from that same Sender's store, preventing an unrelated recovery store from being
accidentally wired alongside it. Start it only through trusted composition after
all authority and adapter prerequisites are independently met. Do not share its
Sender/store with another loop or close the pool before joining it.

- Zero-value config is disabled; Start returns `CALLBACK_WORKER_DISABLED` and
  Stop does nothing. Enabled construction validates every budget. Construction
  does not acquire a connection or invoke adapters.
- One goroutine, one current operation, no queue, no detached timeout tasks,
  no overlapping recovery/sending. Start is single-use and concurrent calls
  cannot create another loop. Stop before Start prevents a later start.
- Each cycle attempts at most one committed recovery, then calls actual
  `Sender.RunOne`, then waits even if work succeeded. Poll interval is explicitly
  100 ms–1 minute; recovery and send budgets are each 10 ms–10 seconds. Each
  Stop waits at most the smaller caller deadline and its configured 10 ms–1
  minute shutdown budget. Context cancellation is checked after each operation
  and before each subsequent iteration. Trusted adapters must honor contexts.
- Store policies stay capped at 32 destinations (Sender at 64); recovery returns
  at most one candidate per configured pair and 32 in total. Each hint resolves
  through one bounded transaction. Existing runner, slot, attempt, body and
  key/transport limits remain unchanged. No new pool or unbounded goroutines.
- Health is bounded lifecycle state/code plus saturating counts. Running is not
  evidence of live database authority, UTC health, egress safety or readiness.
  Empty Claim/recovery scans check the local gate, but do not lock the database
  runtime row. Mutation/permit paths retain their authoritative lock checks.
  No identity, destination, body, key, raw error or adapter-provided code appears.

## Recovery and fairness

`RecoverOneExpired` selects only leased, database-expired rows in the store's
external recovery epoch and copied tenant/destination policies. It uses existing
partial indexes and oldest-expiry ordering. One hot destination cannot fill the
candidate list with its entire backlog. This is bounded recovery selection, not
an unrestricted/multi-version scheduler or a production capacity measurement.

Selection is a read-only hint, never recovery permission. The shared internal
RecoverExpired implementation rechecks the gate, takes tenant → destination →
runtime → delivery → attempt/slot locks, validates immutable graph and exact
lease/slot fences and epoch, and reads database time after locking. It does not
re-read current destination authorization to erase an already-issued attempt.
Revoked/disabled scopes can release old leases, as explicit recovery already
allowed. No retention quota is released and no fairness/token counters change.

Before settlement or an unexpired-row skip, recovery rejects contradictory
unfinished evidence under those same locks and the final database clock. The
attempt must start at or after the claim and no later than the observed time;
any preparation marker must be between the attempt start and observed time.
An unfinished attempt must have no status/error result, and a leased delivery
must have no delivered timestamp. Missing claim/start evidence or an out-of-range
database timestamp also fails closed. Explicit and selected recovery leave the
attempt, delivery, slots and audit untouched on these errors, and selected
recovery stops rather than trying another candidate. An expired lease may
legitimately settle after its hard deadline; this check does not require a
recovery completion before that deadline, repair history, or infer receiver
effects. Scripted pgx regressions cover this boundary; real PostgreSQL validation
of these new contradiction cases is separate and remains NOT_RUN locally.

Only an exact clean busy rollback skips to another bounded candidate. Joined
rollback/discard errors and uncertain commits stop the operation. A candidate
already terminal or with a fresh, unexpired successor lease is a clean no-op,
never reported as recovered. A true return means the settlement transaction was
confirmed committed. Different owners in the same externally approved epoch
may recover expired leases; a different epoch cannot. Prepared expiry remains
`delivery_unknown`; unprepared expiry remains `lease_lost`. Existing retry
backoff, durable attempt cap, fences, immutable bytes and exact slot release
remain authoritative. The loop gives Claim a turn after each recovery and
retains Claim's existing shared virtual-finish scheduling.

## Stop, uncertainty and restart limits

Any recovery error, expired recovery budget or cancellation observed when
recovery returns latches `RECOVERY_UNCONFIRMED`, even if the adapter returns nil.
The loop checks the operation context and its deadline before cancelling its
own context. It does not count a late reported recovery as confirmed, call Claim,
retry recovery or erase the halt when Stop joins. This is conservative caller
evidence, not proof that a database mutation did or did not commit. A pending
Stop still retains the actual adapter call and dependencies until it returns.

Any Sender `Reconcile` result or claim failure also latches a halt.
The Sender may resolve only a completion commit uncertainty before
returning, using the [bounded observation contract](completion_observation.md).
It never retries Complete, Prepare or transport. Missing/conflicting evidence,
unknown Claim/Prepare, and any cleanup uncertainty still latch the halt. The
worker cannot clear a latched halt or create replacement authority. A terminal
instance cannot be restarted. A new instance requires independently confirmed current authority and
resolution of the specific uncertainty; reconstructing a Worker is not itself
reconciliation. This slice supplies no operator reconciliation workflow.

A normally stopped or crashed prior owner can be replaced with an explicitly
pre-provisioned new boot/owner in the same epoch. The new worker may only settle
its database-expired leases and then use existing Claim/Prepare paths. It cannot
undo a possible receiver effect or make at-least-once delivery exactly-once.
Cross-generation recovery, restore/PITR reconciliation, generation allocation,
lease renewal, automatic provisioning, retention/GC and forced termination are
not implemented. Migration 0009's old-history refusal is unchanged.

Stop cancels and **joins the real loop**, including independent settlement and
transaction cleanup. Nil means the loop is joined, not that durable settlement
succeeded: inspect the latched health code. An in-flight request can still have
an unknown effect. Cancellation may race a final send check, so only the
completed join establishes no future loop work. Never close a pool on a pending
join or launch replacement work with the same owned dependencies.

Budgets are cooperative and additive, not a hard wall-clock kill switch. With
existing pg Runner Cleanup budget C, cancellation during recovery may still
need rollback plus discard, up to 2C. Cancellation inside RunOne may need the
current Runner's rollback/discard (up to 2C), Sender.finish's independent 1-second
Complete budget, that Complete Runner's rollback/discard (up to 2C), an optional
independent 1-second completion observation and its rollback/discard (up to 2C):
a conservative remaining allowance is **2 seconds + 6C**, plus scheduling
overhead, assuming every dependency honors its context. Normal operation
similarly may exceed RunTimeout by independent cleanup and observation. For
C=1 second, budget at least eight seconds plus headroom for a cooperative send
join; this is not a measured SLA.
The Runner permits C up to 30 seconds, so even a 60-second Stop can honestly
return pending. Retry Stop with retained dependencies. A noncooperative adapter
can delay forever: no goroutine is abandoned and Active stays true while the
join is pending. An external process supervisor's behavior is not supplied here.

## Evidence

The baseline at `d51358806623fd4179a87a802f1d09c6a8cc810f`, including
migration 0009 and the CB03 selection/worker children, passed all 43 real
PostgreSQL groups in
[41780269941](https://github.com/ZhangIvan/worktool-open/actions/runs/41780269941).
That evidence does not execute the later completion-observation changes.

Pure/race tests exercise the actual Sender's claim → prepare → send → independent
settlement path, cancellation, single/concurrent start/stop, pending joins,
reconciliation halt, pacing and redacted health. Scripted pgx tests exercise
actual selection/recovery Go control flow, old-owner same-epoch recovery,
policy/epoch arguments, busy and stale hints, post-selection/post-lock gate loss,
commit ambiguity and rollback/discard failures. Fakes do not prove SQL locking,
query plans, actual network cancellation or restart behavior.

Worker lifecycle cases are children of existing CB03, preserving the 43 top-level
harness groups: policy/epoch filtering, unexpired/terminal skip, real busy-scope
skip, and same-epoch successor recovery through the real Worker, Sender and
PostgreSQL store. Its final transport is test-only HMAC verification with no
socket; CB01/CB05 retain separate existing HTTP evidence. Those CB03 children are
included in the baseline run above. The new CB02/CB05
completion-observation children are **NOT_RUN** locally. They compile and skip at
the existing gate before any PG or listener. No migration, CI/harness/hash,
startup or production activation changes are included.
