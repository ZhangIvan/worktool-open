# Phase A: immutable callback destination identity

Status: local candidate, not deployed. PostgreSQL cases are authored but **NOT_RUN**
for this change. Baseline `d1e334c75aabc8eb313d97441637975e1dd0e4a0` passed 43 real PG
groups (including CB01–CB05) at 2026-11-04 03:28 UTC in
[run 41767151007](https://github.com/ZhangIvan/worktool-open/actions/runs/41767151007),
using 2 CPU / 1 GiB with three cleanup confirmations. That does not validate this
new migration or the added SQL cases.

## Migration prerequisite, not an automatic compatible upgrade

Migration `0009_callback_destination_versions.sql` refuses to run if **any**
callback subscription, callback reservation, callback outbox row, legacy callback
row, or nonempty command subscription snapshot already exists. This includes
retired subscriptions and terminal deliveries. The old mutable destination row
cannot prove which URL or key reference was authorized when that history froze.
The migration neither invents that history nor deletes it. An existing system
with any such history needs a separately designed, reviewed reconciliation or
migration plan. Clearing tables to pass this guard is not part of this design.

A destination-only database with no callback history can freeze its current
configuration safely for *future* use. State is preserved, including revoked
state. This does not make unknown historical targets trusted. The original eight
migrations are unchanged; the ninth migration is transactional and uses bounded
locks/timeouts. It adds one table, taking the candidate inventory from 28 to 29.

## One identity across the existing result graph

- `callback_destinations` remains the compatibility current pointer and
  destination-wide state. Tenant, app and destination identity cannot be reassigned,
  deleted or recreated through ordinary DML.
- `callback_destination_versions` retains the exact URL and opaque key reference
  for `(tenant, app, destination, policy_version, key_version)`. Rows cannot be
  rewritten or deleted. Only a first, irreversible `revoked_at` transition is
  allowed. Same-value updates are harmless no-ops.
- The current pointer has a deferred exact-tuple FK. Its publishing trigger makes
  the version and checks exact URL/key_ref equality within the same transaction.
  A direct version insert must match a share-locked current row; it cannot create
  a detached future version. Functions are invoker-rights, with resolution pinned
  to `pg_catalog`, the migration schema, then `pg_temp`.
- Subscriptions now reference the full destination-version tuple. Existing
  immutable subscription → reservation → outbox constraints remain unchanged.
  Thus the frozen delivery already carries the historical lookup key; no new
  body serialization, copied secret or replacement result graph is needed.
- Admission locks/checks the exact version. Result materialization prelocks that
  same version and checks its revocation state while preserving frozen evidence.
  Claim and Prepare read URL/key_ref **only from the outbox's exact version**,
  then compare the entire binding with copied runtime configuration. The current
  pointer decides eligibility, never supplies a historical delivery's target.
  An absent historical row fails closed, with no current-target fallback.

The database guard does not replace least-privilege deployment grants. Tests use
existing dedicated synthetic roles. No production grants/roles or administration
endpoint are introduced here. A future configuration writer must take the tenant
scope gate, destination scope gate, current destination, exact version and
subscription locks in that order, before changing config/subscriptions. The
current row lock serializes compatibility writes even for writers that omit the
scope gates, but such writers may deadlock with runtime transactions and are not
a supported control-plane protocol. Transaction failure grants no authority.

## Rotation versus revocation

Normal rotation advances policy and/or key version monotonically. A URL change
requires a strictly newer policy version; a key_ref change requires a strictly
newer key version even if policy also changes. Neither dimension can regress.
The previous version is retained and **not marked revoked** merely by rotation.
New work needs a matching new subscription version; retirement remains terminal.

Phase A deliberately preserves conservative **cancel-on-rotation**, not graceful
old-version draining. Old held reservations materialize as cancelled evidence;
old ready deliveries are cancelled when Claim can process them; an already
claimed old delivery gets no new Prepare permission. Rotation does not
synchronously sweep the backlog or release retention quota. A bounded explicit
RecoverExpired/completion path still owns leased attempts. The existing
`DELIVERY_AUTHORITY_REVOKED` cancellation classification covers lost eligibility,
including rotation; it is not proof that a destination-wide revoke occurred.

An exact-version revoke marks that version's `revoked_at` permanently. It blocks
new admission/materialization/Claim/Prepare authority for that version, while a
newer replacement identity can still be created under a nonrevoked destination.
A **destination-wide revoke** makes `state='revoked'` terminal and revokes its
current exact version. It also blocks all versions through the current authority
check. That destination identity cannot be restarted by advancing versions.

Revocation/rotation cannot retract a permission already committed by Prepare or
prove that a receiver did not process a request. They serialize with the version
share lock and prevent later permits. Complete intentionally remains able to
settle a previously issued attempt without a new destination authority check.
All existing failed/unknown-commit, quota, slot, fence, app/device/tenant and result
provenance checks remain in place.

## Precise immutability boundary and remaining gates

This phase freezes **URL and key_ref identity**, not all policy contents. CIDRs
remain bounded, copied, independently reviewed process configuration and can
still differ between process configurations with the same version numbers. The
signature protocol and secret bytes behind a key_ref are not persisted/versioned
by this migration. A key provider must preserve exact binding/version semantics;
changing key bytes behind a handle is not solved here. Full configuration-content
identity and admission are separate production gates, not a claimed capability.

Store configuration still permits at most 32 policies and one version per
(tenant, destination); Sender retains its 64-policy bound and fanout remains at
most four. No multi-version scheduler/draining, new sender, key provider, listener,
admin API or background daemon is installed. Default production sending remains
disabled. Runtime composition/reaper orchestration, retention/GC, shared-endpoint
policy, retry budgets and production egress/UTC health remain later work.

## Verification

Pure scripted tests call actual Claim and Prepare with historical/current
mismatches, same-version URL/key_ref drift and runtime reload, revocation,
subscription mismatch and absent history. They prove Go decision paths and
cleanup, not SQL semantics. Existing unknown-commit and completion tests remain.

Named cases under existing `TestPGIntegrationSchemaAndDeviceBindings` cover exact
trigger/FK errors, rotation preserving outbox identity and allowing new
subscriptions, terminal/idempotent revoke, version-specific revoke, temp-schema
shadowing, version share-lock conflict, config-only backfill and legacy-history
migration refusal with row preservation. They inherit the original strict gate
and are locally **NOT_RUN**. The 43 top-level CI selector, diagnostic harness and
script hash chain are unchanged. The new subcase names are not added to the
redacted diagnostic name allowlist; a future approved CI run still propagates any
failure through its existing parent group but may not identify the new child in
its sanitized report. No database or listener was started for this phase.
