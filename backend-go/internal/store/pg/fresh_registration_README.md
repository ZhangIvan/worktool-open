# Unwired fresh pending registration candidate

Local source candidate based on b4d3fd889647c9c912b11d8daf539cf1a84d04f8.
This is not an authenticated registrar, an installed migration or a worker.
No exported registration function, constructor or concrete production verifier
exists. The only verifier implementation is a synthetic `_test.go` fixture.
Package-private containment is not a database privilege boundary.

## Exact scope

The pure `internal/callback/registration` descriptor introduces an explicitly
separate `worktool-destination-descriptor-v1` schema. Existing `cbplan-v1` bytes
and code are unchanged. Descriptor decoding is strict and bounded to 16384 bytes,
reuses existing URL/public-CIDR policy checks, sorts CIDRs and rejects duplicates.
It binds the provider authority namespace as well as the exact declared revision.
That namespace string and revision syntax do not authenticate a provider.
Policy and key content identities separately include their own numeric version,
so advancing the other dimension cannot reinterpret the same projection.
Identity encoding is fixed-struct Go JSON with documented escaping, not JCS.

`registerFreshPending` uses the real existing Runner and pgx.Tx interfaces, but
is unexported, has no runtime caller and cannot obtain a production verifier.
Its candidate schema is fixed to `wt_synthetic_registry`; it does not select a
production schema, connect, create a pool, inspect environment variables or start
services. A local session search path is pinned before business queries, and
all relation names are additionally schema-qualified. The only first business
lock is the existing tenant gate, followed by the tenant-qualified active app.
App activity does not confer registration authority.

The verifier contract requires exact current grant and actor, approval/fence,
provider mapping, schema preflight, live exclusion of unmediated writers, complete
identity history/restore provenance, and persistent mutation/restart authority.
It must be local-only, bounded and rechecked before resources, after tenant/app
locks, and after constraints immediately before commit. **None of those trusted
services is implemented here.** The fixture assumes them explicitly. It does not
turn empty current tables, opaque references or a private method into proof.

Freshness checks span all apps for `(tenant,destination_id)`: current and exact
versions, all four candidate tables, scopes/slots, every subscription version,
reservations, outbox and command subscription snapshots. Four fixed JSON probes
reject malformed/unclassifiable snapshots; no array-expansion loop is used.
An absence query is still not proof of never-before-existing identity. The future
verifier must refuse missing history provenance. EXISTS bounds returned data, not
SQL rows scanned or CPU; actual query plans/resource usage remain unmeasured.

## Fixed transaction and immutable audit choice

The fresh path has five explicit INSERTs: current destination (literal
`pending_review`), policy projection, key projection, complete binding and operation
receipt. Unchanged migration 0009 synchronously creates the exact version, for
six logical records. The receipt itself is the immutable registration audit;
no mutable generic `audit` row is reused or represented as protected.

The full canonical intent records action and pending-only outcome, schema,
authority/operation namespace, actor, approval/fence/history references, expected
revision zero and new revision one, optional full-plan audit digest and complete
descriptor. It stores no secret bytes and makes no claim of a database event
timestamp or an authenticated audit trail. Operation uniqueness is
`(authority_id,tenant,operation_id)` across targets/apps, plus one receipt per
`(tenant,destination_id)`. The authority namespace is a future verifier-owned
boundary, not a caller-controlled way to evade idempotency.

Every insert must affect exactly one row. No UPSERT, overwrite or insert-ignore
path exists. A complete exact graph is read back, all constraints are forced,
the final authority decision is rechecked, then Runner commits once. Existing
objects, partial/orphan history and changed intents are rejected, never adopted,
repaired, backfilled, rotated or deleted. Duplicate recognition checks the same
complete pending graph and performs no registration DML. Explicit observation
cannot reach any INSERT and cannot clear an incident or confer send authority.
No scope, slot, subscription, delivery, key, grant or worker is created.

The additive SQL text lives only under `testdata`, not `migrations/`. It is not
embedded, installed or loaded by the existing integration fixture. Four tables
have independent projection keys, exact identity-bearing FKs and immutable
UPDATE/DELETE/TRUNCATE guards. Insert guards require the matching pending current
and unrevoked exact version. Existing destinations may remain unbound. Old
migrations, quotas, retained graph, replay receipts, CI and browser gates are
unchanged. The candidate authors no roles, grants, revokes or SECURITY DEFINER.

SQL constraints do not authenticate descriptor/intent bytes, recompute the Go
canonical identities, prove historical freshness or prevent an existing broadly
privileged principal from inserting/activating a destination directly. A future
actual writer boundary and real nonowner/direct-DML tests are hard blockers.
Do not connect this candidate by creating a `trusted=true` constructor.

## Failure and observation

Runner performs no automatic retry. Known rejection/SQL failure returns an empty
outcome; only a confirmed clean commit returns the pending-only status. Commit or
cleanup uncertainty closes the verifier's durable barrier contract and returns
unknown, with cleanup/closure uncertainty retained. A closure returning after its
cleanup deadline is unconfirmed. Panics are redacted and also attempt bounded
closure. Candidate-only driver adapters convert Begin/Commit/Rollback/Exec/row
panics to errors and taint the transaction so Runner performs bounded discard.
A tainted rollback cannot be mistaken for clean ErrTxClosed. Verifier panics
are converted inside the transaction callback so simultaneous rollback/discard
uncertainty is retained rather than lost during panic unwinding. The shared Runner
remains unchanged. Pool acquisition, connection release/discard and arbitrary
provider implementations still obey their existing interface contracts; a panic
there can leave resource health uncertain and is not proof of cleanup. The fixture's Boolean is only a synthetic model of closure; it does not
survive a process restart or implement operator reconciliation. No new instance
may be treated as production authority in its place.

Observation of a matching graph does not clear the barrier, repair partial data,
prove rollback when nothing is found, or authorize retry/activation. If observation
itself has cleanup uncertainty, it also fails closed. Locking observations use an
ordinary transaction, not PostgreSQL READ ONLY, but issue no business DML.

## Verification limits and next gate

Offline descriptor tests and strict fake-driver tests exercise real Go parsing,
identity encoding, transaction sequence, exact arguments, rejection branches,
cleanup/outcome classification and absent runtime entry points. They do **not**
parse/execute PostgreSQL, prove SQL uniqueness/FKs/triggers or actual concurrency,
nonowner permissions, SQL rollback durability, COMMIT response loss, persistent
barrier recovery, authentic grants or actual provider immutability.

Before any later publication or isolated database acceptance, review and define
an actual least-privilege writer boundary without widening existing grants.
Then separately verify candidate syntax/install, both competing writer orders,
operation ID reuse across targets/apps, projection immutability/direct DML,
wrong/unsafe namespace and temp shadowing, malformed graph/history refusal,
transaction rollback and complete graph, and real unknown/cleanup recovery.
The existing required PG evidence gate is unchanged; current green CI on b4d3fd88
is not evidence for this new candidate. No production callback is enabled.
