# Bounded retained-capacity audit, v1 candidate

This internal, explicitly constructed auditor reports conservation of the current
supported reservation projection. It has **zero release, deletion, counter repair,
replay settlement or deployment authority**. Nothing calls it at startup or from a
worker, listener, HTTP handler or scheduler. No production role is provisioned.

## Trusted invocation and transaction

Construct `RetentionAuditor` with the existing `Runner` and a mandatory local
read/recovery interlock. Construct `TrustedCapacityTenant` only after an internal
adapter has independently authorized that one tenant on the authoritative primary.
The constructor validates an identifier; it does not authenticate or authorize it.
There is no caller-selected cross-tenant filter or public request DTO.

The interlock must pin the expected recovery generation and fail sticky on
recovery-generation or clock-validity loss throughout the call. A boolean that
closes and reopens for a different generation between checks is insufficient.
The gate must do no I/O, waits or side effects. An always-open implementation is
suitable only for explicitly synthetic fixtures. Production primary selection,
authorization, clock health and resource approval remain the caller's duties.

`AuditRetainedCapacity` uses exactly one `Runner.Run` with explicit
`pgx.RepeatableRead` and `pgx.ReadOnly`. Runner is unchanged. Fixed transaction-local
statement/lock timeouts are 1500/500 ms, inside the Runner's separately bounded
acquire/begin/transaction/commit/cleanup timeouts. The first SELECT reads
`transaction_timestamp()` and establishes the snapshot. Its validated observation
time is historical, not a release clock, authorization token or current-state
promise. A concurrent writer may commit after the snapshot without invalidating
this historical audit.

Only ordinary SELECT relation locks are acquired. There is no row lock, tenant
writer-gate lock, advisory lock, `SKIP LOCKED`, transaction retry or DML. No transport,
listener or goroutine is started by the auditor. Result candidates stay private
until the Runner has returned, including deferred cleanup, and the final local
interlock/caller context checks pass. Each Rows is closed and checked for errors,
including sentinel early exits, and the transaction context/gate is rechecked.
Read, gate, clock, commit or cleanup uncertainty suppresses all candidate data.
`ErrCleanupUnconfirmed` and `ErrCommitOutcomeUnknown` remain discoverable through
the redacted error wrapper. Error causes are private diagnostic data; do not unwrap
and log them. A later successful read does not resolve earlier cleanup uncertainty.

## Fixed bounds and metadata

The eight inputs are independent tenant-scoped reads of `scope_limits`,
`admission_reservations`, `commands`, `callback_outbox`, `developer_callbacks`,
`callback_attempts`, `scope_slots` and `outbox`. Every read has `LIMIT 1025`; 1024 is
the fixed per-table v1 cap. Sentinel rows are detected without scanning them into
Go values, and immediately stop the audit. Exact-cap populations are accepted if
the independent metadata budget also permits them. Empty slots, disabled scopes,
purged identities, terminal deliveries and legacy rows all count. There is no
pagination, aggregate, join multiplication or sort before these caps. Reference
maps and arithmetic are built only after all eight populations are complete.

Each transferred identifier is SQL-bounded to at most 128 UTF-8 bytes. Invalid
identifiers return an empty sentinel and a separate false validity bit. Enums
return only known finite strings or an invalid sentinel. Arbitrary error/release
text is not transferred. The frozen command snapshot is checked against the
existing array/4-entry/16384-byte schema bound, but only four individually bounded
destination identifiers and validity/count flags cross the connection. No payload,
result contents, callback body, URL, key reference, credential, request/response
content or unbounded metadata is fetched. Body/result presence is a boolean.

Before scanning each row, the reader charges a conservative fixed 4096-byte
metadata envelope against an 8 MiB total budget, allowing at most 2048 retained
input rows across the eight tables. These constants are not caller-configurable.
This deliberately conservative budget can refuse tenants with many small rows.
It bounds input metadata and subsequent bounded model work, not exact Go heap use,
PostgreSQL physical page scans, planning, TOAST work, server memory or lock waits.
PostgreSQL plans and realistic performance remain **NOT_MEASURED**.

Output has a fixed vocabulary of issue/hold codes and at most 1024 separate scope
comparisons. Raw tenant, command and scope identifiers are not returned. Scope
identifiers are represented by stable SHA-256 tokens for correlation; these are
not an anonymity guarantee and the report remains restricted internal data. The
String methods redact metadata. Holds are a global union, not per-command release
instructions. No body hashes or content authentication claims are made.

## Meaning of each status

- `UNAVAILABLE`: input/authority constructor misuse or an unconfirmed acquisition,
  begin, statement, scan, row read, clock, context, interlock, commit or cleanup.
  Returns an error and no observation time, totals, holds or candidate report.
- `INCOMPLETE`: a confirmed row/metadata bound abort. No observation time, totals
  or conservation claim. An accompanying read/cleanup/context failure takes
  precedence and produces `UNAVAILABLE`.
- `UNSUPPORTED`: any non-app/non-`message.send_text`/non-`bounded-result-v1` command
  or unmanaged legacy callback event. No observation time or totals. Rows are not
  filtered out to manufacture a pass.
- `INTEGRITY_CONFLICT`: missing or mismatched projected references/identities,
  duplicate typed keys, unsupported enum values, invalid fixed amounts, ranges,
  release markers or cardinalities. No usable scope totals are returned.
- `DRIFT`: the supported projection passes the selected checks but at least one
  scope's jobs or bytes differs from its reservation sum. Each scope is compared
  separately; opposing errors cannot cancel.
- `CONSERVED`: the complete bounded supported projection passes those checks and
  its reservation sums equal the stored counters. It is **not full graph integrity,
  retained-body accounting, replay eligibility, policy approval or reclaimability**.

All statuses return `ReleasableJobs = ReleasableBytes = 0`.

## Conservation and selected graph checks

Each nonpurged result reservation contributes 1 job / 196608 logical bytes to the
tenant. Each nonpurged callback reservation contributes 1 / 90112 to the tenant
and its destination view. Held, materialized and cancelled-retained states remain
charged. Purged identities contribute zero, but are retained for identity checks.
Tenant and destination views must never be added together as physical bytes.
One result plus two deliveries sharing one callback event therefore charges the
tenant 3 / 376832 and each destination 1 / 90112. Unused scopes must compare to zero.
All arithmetic is checked int64; malformed amounts cannot drive the sums.

Checks cover tenant isolation, scope identity/ranges, one result per supported
command, up to four distinct callback destinations matching the frozen snapshot,
reservation/delivery links in both directions, full copied immutable callback
binding between those two rows (app, subscription, destination, versions, attempt
limit and ordinal), command/event/app relationships, result/body presence, attempt
and slot parents/typed compound keys/fences, retained intent parents and release
markers. A missing tenant or callback destination scope fails, rather than being
hidden by a scope-led join. Stored bodies or current mutable config are not read.

This projection does not read/authenticate receipt cryptography, subscription
history, devices, application authority, tombstones, inbox rows or dedupe records.
It checks the frozen **destination set**, not every copied subscription field in
the original command JSON snapshot. It does not prove exactly one managed event
per command, uniform lifecycle states, all lease-owner relationships, complete
attempt chronology or the full gateway replay graph. Actual replay validators
have stricter uniform-state and event-cardinality requirements. The 64 lifecycle
unit subcases deliberately include mixtures that conserve arithmetic but would
not satisfy those replay validators. None become eligible for release.

## Retention always remains withheld

Every confirmed model report includes `RETENTION_POLICY_NOT_IMPLEMENTED` and
`REPLAY_RETENTION_NOT_PROVEN`. Additional bounded holds identify unset/future
retention deadlines, held/cancelled-retained reservations, unsettled or unknown
command effects, ready/leased deliveries, unresolved or historical unknown/lost
attempts, any slot reference (including expired ones), all retained outbox intents,
shared event cohorts, retained event bodies without deliveries, retained result
bodies after result-reservation purge and mixed purged/live replay graphs.

A past `retain_until` is not policy permission. NULL is unset, never expired.
Delivery expiry and completion concern scheduling/slots, not retained capacity.
A pending `result_ack` is not the device journal ACK. A historical unknown attempt
stays withheld even if a later attempt delivered. A retained shared event can
outlive all charged callback reservations while the ledger is still conserved;
the distinct hold makes that visible without claiming complete body accounting.

A future destructive slice needs a separately reviewed policy/hold authority,
closed replay window, shared-body final-owner protocol, atomic counter/body graph
update and durable release identity for unknown commits. This auditor implements
none of those and cannot be copied into a write transaction as authorization.

## Validation boundary

Pure and scripted-pgx tests cover conservation, all 64 reservation lifecycle
combinations, drift and missing graphs, identity/range failures, input bounds,
resource closure, transaction options, bounded reads, commit/cleanup uncertainty,
interlock/context failures, clock failure, deterministic output and zero release.
They are not a PostgreSQL parser, MVCC/locking proof or production-readiness claim.

Guarded real-PG children are registered by one call in the existing
`TestPGIntegrationValidMaterializationAndGC` group; the strict 43 top-level PG
groups and diagnostic harness are unchanged. Children use the existing explicit
isolated-synthetic fixture gate and separately provisioned nonowner store role.
They seed a fresh tenant, leaving the original incomplete/legacy fixture intact.
They author actual admission plus synthetic result/shared-event metadata, snapshot
barriers against concurrent admission/materialization, exact/over row caps with
locked rows, missing scopes/graphs and drift, before/after whole-row fingerprints,
and SELECT-only/nonowner failure checks. Their test-only barriers and fingerprint
aggregates are not production auditor behavior.

Real PostgreSQL execution, SQL syntax, driver behavior, actual MVCC, lock timing,
privilege checks and plans remain **NOT_RUN** here. Offline tests unset all three
`WORKTOOL_PGTEST_*` authorization/DSN variables and `GITHUB_ACTIONS`; the existing
gate skips before DSN parsing or socket creation. No services, sockets, downloads,
repository publication, CI, migrations or production configuration are performed.
