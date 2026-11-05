# First-result replay receipt migration contract

Status: independent local candidate; SQL has not been executed against PostgreSQL.
Base: public head `bd2452bff61deaa023288ee735de4f4e73fed961`. This is not the
unavailable earlier local candidate or evidence of its behavior.

## Scope

0010 adds one bounded receipt row per `(tenant, command)` and two invoker-rights
guards. It performs no historical backfill, row deletion, capacity debit/refund,
reservation transition, callback delivery, service startup, or role creation.
Existing migrations remain unchanged. The table has no additional role grants;
the existing isolated fixture/composition must separately supply its ordinary
store table permissions through its existing mechanism.

The supported application contract remains synthetic `message.send_text`, with
`bounded-result-v1` admission. Default F=0 remains supported. Its first result is
also immutable; no callback subscription is required by this migration.

## Migration preconditions

Run the entire migration in one transaction through the existing migration runner.
Lock and statement timeouts are 1 second and 5 seconds. The schema must be owned
by the executing migration role, nonpublic and nonsystem, with no PUBLIC CREATE
permission. Every inspected relation must resolve to an ordinary table in that
schema, not a temporary or earlier search-path object. The nine history tables are
then locked ACCESS EXCLUSIVE before history inspection and installation.

The migration refuses any existing nonnull result or terminal/unknown command
status; materialized/purged/cancelled-retained result or callback reservation;
any developer callback or callback outbox row; audit action `command.result` or
`command.result_conflict`; result ACK outbox; tombstone;
terminal/result/reconciliation event; or inbox result ACK reply. These checks are
deliberately conservative and whole-schema: a legitimate pre-result unknown or
terminal state or cancelled-retained reservation can also require reconciliation.
Callback and result-audit history are checked independently even if command/result
evidence was cleared; both callback tables and audit participate in shadowing
checks and exclusive locks. A cleared `commands.result` alone
does not qualify a historical graph as new. The migration does not infer missing
facts or erase blockers. It cannot discover history already erased from every
checked source, and is not an audit-tampering recovery mechanism.

## Receipt SQL contract

`callback_replay_receipts(tenant text, command text, receipt text)` has a primary
key on `(tenant, command)` and a noncascading foreign key to commands. Receipt text
must be a JSON object of at most 16384 UTF-8 database bytes (`octet_length`).

Insert checks reject duplicate top-level JSON fields, missing/null fields, and
wrong field types. String fields are `schema`, `tenant`, `device`, `command`,
`app`, `attempt`, `commandDigest`, `resultDigest`, `status`, and `effect`.
`appEpoch` must be a JSON number with the exact decimal text of the stored integer.
The schema value is `callback-result-replay-v1`. All identity/status/effect fields
must match a locked, supported app command with a nonnull result and attempt.
`resultDigest` is the lowercase hex SHA-256 of the exact UTF-8 commands.result
bytes, using PostgreSQL built-ins without pgcrypto or another extension.

Every UPDATE and DELETE attempt on a receipt is rejected, including no-op updates.
The guard's search path is pinned to pg_catalog, the migration schema, and pg_temp
last. It does not run with elevated definer rights. Ordinary triggers are not a
defense against the schema owner disabling triggers or truncating tables; do not
grant the store those privileges.

SQL does not implement the application's canonical JSON serializer or fully
validate additional provenance/callback fields. The Go writer must construct and
validate the complete bounded canonical receipt, reject unknown/duplicate nested
fields, and validate exact first-result provenance and all 0–4 callback bindings.
The Go duplicate path must rederive and byte-compare that receipt against the
retained graph before either replay early return. SQL identity binding is defense
in depth, not a receipt-only replay authority or a reclamation certificate.

## Commands guard

After a supported app command first has a nonnull result, DELETE is rejected.
Updates cannot clear/replace the result or change tenant, command, device, actor,
actor type, capability, app/epoch, attempt, command digest, policy, confirmation,
creation/expiry, status/effect, or the original dispatch session/owner/fence/
recovery epoch/connection. Nullable values are compared with IS DISTINCT FROM.
Existing admission snapshot guards continue to apply. Nonidentity lifecycle
fields such as cancellation flags and command versions are not frozen by this
guard. The Go replay validator still requires the retained payload/admission graph;
this migration does not authorize payload clearing or any other retention work.

First-result UPDATE is allowed because OLD.result is null. The Go transaction
must then persist the receipt and existing callback/inbox/audit/ACK changes in the
same transaction, with receipt size included in its logical reservation budget.
There is no deferred SQL constraint requiring every result to have a receipt:
missing receipts must fail closed in the Go replay path, and transaction-level
atomicity must be exercised by the integration suite.

## Required verification before any live application

- Fresh schema migration succeeds; unsafe/shadowed schemas and every historical
  marker fail without partial schema changes or historical data mutation.
- Inserts accept the exact supported binding and reject each missing/null/wrong
  typed field, duplicate top-level field, changed result digest, oversized receipt,
  missing command/result, and wrong tenant/device/app/attempt/status/effect.
- Receipt no-op/update/delete and command result clear/replace/delete or identity
  mutation fail; permitted nonidentity lifecycle changes remain possible.
- F=0, F=1/F=4, reconnect provenance, duplicate old/fresh inbound event, rollback,
  and uncertain commit preserve existing ACK, graph, and capacity semantics.

These are verification requirements, not claims of executed database tests.
