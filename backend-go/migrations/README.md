# G-P1 candidate schema source — not yet executed on PostgreSQL

Baseline: `dd13ce7`; approved integration-plan SHA256
`a7d9bcdeec1b743b3ef8ed6e8dec85d15c95d27c948e11f18dd53d17e0763484`.
Input is frozen 19+7 design plus P0 admission binding revision v1.1 (two tables).
This directory is an **empty isolated schema** migration candidate, not a deployed
migration tool or a migration of customer data. External IDs remain TEXT; original
Unix milliseconds remain epoch_ms BIGINT, ownership leases TIMESTAMPTZ. Android
production Kotlin + local SQLite is unchanged.

| Source | Implemented candidate | Not claimed |
|---|---|---|
| 0001_base19.sql | 19 baseline tables, typed user/app provenance, five single-role CHECK, same-app/tenant FKs | RBAC application transactions, production imports |
| 0002_control7.sql | seven gateway/callback/scheduling tables, same-device dispatch/session bindings, immutable callback bytes | running worker, network, claim/fence algorithm correctness |
| 0003_admission2.sql | subscription versions + reservations, budget columns and explicit admission snapshot fields | acceptance transaction, hash computation, counter conservation |
| 0004_admission_constraints.sql | complete forward/reverse deferred reservation/delivery identity FKs, single-use unique key, lifecycle B/immutable guards, retained identity protection | automatic charge/decrement, retention eligibility or GC orchestration |
| 0005_indexes_roles.sql | existing proposed indexes; revoke public table access in current isolated schema | creating roles/passwords; full deployment grants/RLS |
| migrations.go | embed five files in explicit order | connection/apply-on-start, checksum migration ledger |

Normal migration source does not create schema/roles/credentials. The test harness
creates a cryptographically random `wt_it_<24hex>` schema, applies one file per
transaction, and grants a separately provisioned nonowner store role only test
permissions. It cannot DELETE reservation/subscription identity rows; guards also
reject deletion/revival. Public table access is revoked. No blanket delete cascade
exists; test-only final DROP SCHEMA CASCADE is limited to that generated schema.
Guard functions are invoker-rights, not SECURITY DEFINER. Existing outbox guard
uses SQLSTATE 23514 for consistent constraint classification.

No placeholder digest is automatically supplied: new command snapshot/digest/
admission version columns must be explicit. Test fixtures use visibly synthetic
noncredential values and zero digest solely to exercise referential graphs, not
as claims of a fully authorized accepted command. SQL CHECK/FK does not implement
RBAC. App creator is provenance, not an inherited operator permission; owner does
not become admin. Application RBAC/acceptance transactions are still NOT_IMPLEMENTED.

## Test entrypoint / strict gate

`internal/store/pg/integration_test.go` requires all three:

- `WORKTOOL_PGTEST_ALLOW=isolated-synthetic-schema-only`
- `WORKTOOL_PGTEST_OWNER_DSN` for pre-provisioned migration identity
- `WORKTOOL_PGTEST_STORE_DSN` for different pre-provisioned store identity

Both DSNs must point to the same explicit host/port/database; host must be literal
127.0.0.1 or absolute Unix socket directory, database `wt_test_` plus a nonempty
suffix, and no pgx fallback hosts. For isolated non-TLS CI use explicit sslmode=disable
(no default prefer/fallback). Store must not have superuser/CREATEROLE/CREATEDB/
BYPASSRLS or inherit migration owner; effective current_user is checked. No DSN or
password is logged. The harness creates no roles, login credentials or default
connection. No network callback/real endpoint is used.

All three absent: top-level DB tests emit **SKIP / NOT_RUN**, not evidence of PG
success. Partial/bad config fails before connecting. CI bootstrap must explicitly
fail preflight if required inputs are missing, and inspect that real DB tests did
not skip; a green no-environment unit run is insufficient. Bootstrap/workflow is
separately owned and reviewed. No PG bootstrap was executed while preparing this
source.

## Current first batch coverage

Four gated top-level integration groups (all **NOT_RUN**):
1. 28-table inventory, both initially-deferred precise FK catalog checks;
   PG-GW-07/14/15 wrong tenant/device/session paths.
2. ADMISSION-11/12/13/14/16/17/19/20/21/22 update/insert negatives, single
   reservation reuse, immutable identities and partial GC failures.
3. Valid bidirectional materialization COMMIT; lifecycle B cancel retains charged
   reservation state; full reference-graph GC COMMIT; purged revival/delete denied.
   **This tests graph lifecycle, not billing/retention-policy correctness.**
4. Initial INSERT wrong command/app/destination/event must yield actual FK SQLSTATE
   23503 (not only an UPDATE guard); ADMISSION-15 result-kind and ADMISSION-18 held
   reservations rejected by the exact `delivery_reserved_identity` constraint.

Current double-use negative uses a distinct second event and asserts exact
`delivery_one_reservation` UNIQUE violation, avoiding masking by older event/destination
constraints. Initial command/app/destination negatives set the reservation reverse
event pointer equal to the attempted delivery event and assert exact
`delivery_reserved_identity`; the wrong-event case asserts
`delivery_event_same_command`, so reverse-FK failure cannot falsely satisfy these tests. It is sequential, not a two-connection
concurrency test. Negative statement or deferred COMMIT failures roll back before
reusing connections. Cleanup contexts are independent and bounded; only test-owned
schema/handles are closed, never arbitrary PID termination.

**Not yet implemented / NOT_RUN:** two-connection claim/fence/reaper cases,
concurrent same-key admission, concurrent reservation race, budget admission and
GC counter decrement/conservation, real runner timeout/cancellation, Commit unknown
fault proxy, permission matrix via business store methods, snapshot migration,
PITR/HA, physical capacity, Android/HTTP end-to-end. No production readiness claim.

Local no-PG verification: Go compile + existing fake runner tests + textual
migration-inventory test. Text counting checks 28 tables/selected key markers,
**does not parse PostgreSQL SQL**. Four actual PG groups skipped. Real migration
syntax and behavior require the separately approved isolated PostgreSQL run.
