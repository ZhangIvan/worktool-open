# Unwired persistent callback record adapter

This package adds actual PostgreSQL statement/transaction code and pure complete
binding validation. It is not an authenticated provider, worker, launch issuer,
production durable authority or installed schema. Startup and the existing nine
composition blockers are unchanged. The exported constructor is inert and the
only public operation is content-based historical observation. Mutation methods
are package-private with no production callers; tests exercise their real code.
Private scope is containment, not a privilege/security boundary against trusted
source changes or a caller retaining a physical pool.

## Identity and provenance

`Bind` requires a valid existing composition.Plan and exactly matching complete
registration descriptors. It checks app, tenant/destination pair, policy/key
versions, URL/CIDRs, signature profile, key reference and exact revision. The
provider namespace comes from the complete descriptor and participates in its
identity. cb.Binding is never used as sufficient identity. No key material,
provider credentials, DNS or secret retrieval exists here. App activity, hashes,
reference syntax and equality do not authenticate provider or plan provenance.

The registry stores bounded plan_raw. composition.Parse(raw, expectedIdentity)
recomputes the existing semantic identity, including budgets. Raw whitespace is
not canonicalized or represented as canonical bytes. Descriptors are re-decoded
using the existing parser; their canonically ordered CanonicalBytes must match
the exact stored array. No duplicate/missing/extra descriptor is adopted.

The binding digest is SHA256 of UTF-8 `worktool-callback-authority-binding-v1`,
NUL, then Go encoding/json of bindingProjectionV1. Field order/tags are exactly
scope, allocation, instance, boot, epoch, provenance_ref, registry_revision,
plan_identity, descriptor_identities. Descriptor identities sort by tenant then
destination. JSON string escaping follows encoding/json (including HTML escaping),
not JCS. Explicit versioned private fields prevent public Identity evolution from
silently changing this encoding; an encoding change requires a new domain.
Reference and revision are content declarations, not proof of authenticated grants.

An externally owned stable deployment/partition scope is required before future
admission. Config/epoch/allocation/owner/provider namespace are contents, not
replacement scope keys. Missing registry is blocked. This adapter does not
authenticate which scope a caller belongs to, prevent a privileged party creating
another registry row, or establish prior history. Future authenticated scope
assignment, exact provider mapping/revision authority and history/PITR provenance
remain hard gates. No bool or constructor claims to satisfy them.

## Schema, transaction order and concurrency

SQL is unexecuted candidate text in testdata/schema_candidate.sql, not a migration,
embedded installer, or part of existing integration setup. It changes no existing
migration, role, GRANT, database or startup configuration. Fixed schema-qualified
names and local search_path are used, with no caller-selectable authority schema.

Registry is immutable preprovisioned content, and has no insertion API here.
Ledger is one permanently occupied row per scope with identity-bearing composite
FK (scope/allocation/instance/boot/epoch/revision/binding digest). First insertion
is running intent. Only running→halted is permitted; identity and first halt code
cannot change. Deletes and truncates are guarded. All identities are NOT NULL;
bounds and state/code checks are explicit. These guards do not protect against
owners/superusers or prove actual writer exclusion, canonical hashes or trust.

Lock order is registry FOR SHARE → ledger FOR UPDATE → exact event operation.
Registry immutability lets competing initial writers both observe absent ledger;
the scope primary key then allows at most one INSERT, with the loser rejected,
never retried/adopted. Existing ledger rows serialize event appends for a scope.
Cross-scope event races serialize on globally unique operation_id and reject the
loser; no alternative namespace, UPSERT or automatic retry exists. All writes
force constraints and exact readback before the one commit. SQL locks/uniqueness
have not been executed against PostgreSQL in this deliverable.

Running-intent success requires commit AND cleanup before an inert recorded
receipt. Future work must never begin before that. Crash before work, during work,
or before terminal recording leaves running occupied. A new adapter cannot
replace occupied scope by changing configuration or runtime metadata. This slice
never retires/reuses a scope, even after clean JOINED. No live capability exists.

## Incidents and reconciliation facts

Events are append-only, bounded declarations with globally unique operation_id,
exact identity FK to ledger, fixed kind/code and an opaque bounded evidence_ref.
No arbitrary free-text narrative, operator credentials or signatures are accepted.
Evidence references are unverified audit locators. They are not authenticated
operator decisions or verified receiver/cleanup facts.

An incident or joined event halts running ledger in the same transaction. Later
events preserve the original halt reason, including later uncertainty after
JOINED. Observe uses bounded indexed EXISTS, never loads an event history; any
incident dominates JOINED permanently. Running with any terminal event, halted without a matching first terminal
event, and absent ledger with orphan events are rejected using bounded EXISTS
checks under the same lock protocol. A reconciliation_fact leaves state untouched and can never clear an
incident or provide retry authority. Exact repeated operation/payload returns
only HISTORICAL_FACT_NO_AUTHORITY, performs no business DML and does not certify
all incidents are recorded. Same operation with different scope or payload fails.
A terminal duplicate paired with a running ledger is rejected as malformed.

Observation and duplicate checks use locking transactions (not PostgreSQL READ
ONLY), but perform no business mutation. Absent ledger is explicitly
INTENT_ABSENT_NO_AUTHORITY, never fresh/safe/acquired. A later read cannot erase an
earlier unknown commit or unsafe connection cleanup, and no record yields a grant.

Manual reconciliation still requires independent authenticated operator decisions,
complete claim/prepare/attempt/slot/audit graph, receiver/deduplication evidence,
old owner join or independent fencing, and exact new-allocation authorization.
Absence of a completion row does not prove no receiver effect. A future successor must preserve
stable scope/history and add monotone fenced allocation; changing scope is not
reconciliation. There is no decision issuer, grant consumer, clear/reset/restart,
new-generation provisioner, CLI or callback wiring here.

## Failure, cleanup and ownership

Each operation borrows a pool and constructs the existing pg.Runner over local
operation-specific guards. It never creates/closes a physical pool. Outer owners
retain all dependencies until actual worker and adapter calls join. No operation
generates goroutines, detached timeouts, retries or external effects.

Driver guards cover acquire/begin/commit/rollback/discard/release and used SQL
methods, including panic and explicit wall deadlines. SQL/callback panic taints
transactions; tainted rollback cannot become a clean ErrTxClosed. Commit panic or
late result is unknown; cleanup failures remain independently detectable with
errors.Is(pg.ErrCleanupUnconfirmed), simultaneously with commit uncertainty.
Release has no context in the existing interface: its synchronous duration is
checked against Cleanup after return. No hard preemption is claimed. If an adapter
never returns, that operation remains pending with owned resources, not abandoned.

All externally returned errors are reduced to fixed classifications without raw
SQL/provider values or causes. Every failure/late/unknown returns empty Evidence;
receipt success follows Runner cleanup and final caller deadline check. A failed
halt append leaves old running/halted occupancy intact. Pool acquire panic with
no returned handle is cleanup-unconfirmed, not proof of released resources.

Important unresolved case: storage outage/lost acknowledgement cannot guarantee
an additional durable incident write. If uncertain intent did not commit, absence
cannot encode a cross-process cleanup incident. NO production acquisition/restart
issuer is provided, even for absence. A future independently authenticated
admission/fencing service must cover this gap or require fresh manual disposition.
Local latches or newly allocated adapters are not a substitute.

## Evidence limits

Tests exercise actual Go binding and Runner/SQL control flow with exact statement
hashes and arguments. Scripted snapshots are not a database or restart simulation.
Guard tests exercise lifecycle panic/late/cancellation and combined classifications.
No real PostgreSQL SQL parsing/install, locking, restart/PITR, nonowner/direct-DML,
provider, network, CI, service, device or deployment was executed. Actual provider
trust, privilege isolation and durable restart acceptance remain NOT_RUN/unresolved.

## Bounded result projections (V2)

Registry, ledger and exact-event SELECTs wrap every variable-length returned
text/bytea column in a fixed CASE WHEN octet_length(column) BETWEEN lower AND upper
THEN column ELSE NULL END. Values outside the bound are not truncated or adopted:
NULL text fails required-scalar Scan and NULL bytea fails binding validation.
Identity strings are 1–128 bytes, full binding digest 64 bytes, plan identity 81
bytes, plan_raw 1–262144 bytes and descriptors_raw 1–524288 bytes. State/kind/code
projections have their own fixed small bounds; exact enum validation still applies.
Integer and EXISTS boolean columns are already fixed-size results.

These projections bound returned value bytes under normal PostgreSQL statement
execution, including when an untrusted row violates the candidate DDL bounds.
They do not bound server scans, lock waits, CPU, TOAST decompression/storage costs,
or prove a physical wire-size bound against a malicious database server/driver.
Offline tests assert exact bounded statement text and refusal of scripted NULL
results; they do not execute CASE, SQL syntax or PostgreSQL transfer behavior.
