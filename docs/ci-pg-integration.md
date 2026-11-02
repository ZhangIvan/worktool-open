# G-P1 disposable PostgreSQL CI — SOURCE CANDIDATE ONLY

**NOT_RUN:** no Docker command, image pull, PostgreSQL, role creation, real PG
integration case or workflow dispatch has been executed while preparing this
candidate. Independent review and parent's explicit CI execution approval are
still required. Workflow runs only on pushes to `worktool/synthetic-prototype`
matching this new workflow/harness or migrations/integration test sources. There
is no PR trigger (avoids PR+push duplicate PG jobs), no default-main change, and
no nonfunctional pre-main manual-only gate. **Publishing this exact candidate to
the named branch will trigger one CI job.** Publisher must first receive parent
approval of the exact reviewed source and execution resource scope; current
source-preparation authorization does NOT authorize publishing or execution.

## Fixed resource boundary

One GitHub-hosted Ubuntu 24.04 runner, one linux/amd64 Docker Official PostgreSQL
18.6 image pinned by architecture digest
`sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c`.
The reviewed resource proposal is G-P1-EXECUTION-RESOURCES.md, SHA256
`463beca4b8c4f8eb738b5c36e80dde53dea9079297c02dfe04f8e621b3575005`.
No fault proxy, application API/server, device or callback. Workflow/ref concurrency
group with cancel-in-progress=false serializes runs on the same branch instead
of allowing one concurrent PG container per run ID.

Docker launch fixes 2 CPUs, memory=memory-swap=1GiB, shm=64MiB,
max_connections=10, statement_timeout=5s, lock_timeout=1s,
idle_in_transaction_session_timeout=5s (each asserted with SHOW), data parent `/var/lib/postgresql` tmpfs≤2GiB; no host bind,
anonymous/persistent volume, privileged mode or host network/PID namespace.
PG18's declared VOLUME parent is covered, not its obsolete pre-18 child.
Actual inspect mounts/tmpfs/resources/namespaces/loopback binding are asserted;
Config.Env is never requested. `SHOW server_version_num`, `SELECT version()`,
`SHOW max_connections`, `SHOW data_directory` and resolved WAL directory are
checked. tmpfs capacity is NOT memory availability; OOM fails, never auto-scales.
Only host 127.0.0.1 random port is published, no LAN/public bind. Readiness uses
container pg_isready -h 127.0.0.1 -p 5432 (final TCP server), never the temporary
Unix-socket-only initialization server that stops before normal startup.

CSPRNG generates three per-run users/passwords: bootstrap superuser only for
initialization, migration owner NONSUPERUSER/NOCREATEDB/NOCREATEROLE/NOBYPASSRLS
owning only the fresh wt_test_<hex> DB, and nonowner store with all four flags
false and no migration-owner role membership. Store gets CONNECT, with object
permissions assigned by reviewed test fixtures. No reused repository secrets.
Identifiers are generated lowercase ASCII/hex; run/attempt identifiers are
strictly digit-validated before use in names, paths and labels.

Credentials are 0600 files inside a 0700 RUNNER_TEMP run directory. Passwords
enter Docker through env-file and SQL through psql stdin, not process argv.
Tests receive DSNs only in environment, never command arguments or GITHUB_ENV.
Docker still stores bootstrap environment internally: this is unavoidable to
this initialization mode and available to same-host privileged processes, not
an assurance against a compromised runner. Raw inspect, psql outputs, Go Output
records, PostgreSQL logs and DSNs are never printed/uploaded. subprocess errors
use a fixed literal whitelist of diagnostic codes, never exception strings.
Test failures expose only exact approved test IDs and enumerated SQLSTATE tokens
(23503/23505/23514/40001/40P01/57014/55P03), never raw Output text. There is no artifact-upload step.

## Actual execution gate (future reviewed push only)

Pinned checkout/setup-go SHAs mirror existing foundation workflow; official
Go1.27.3 must match, GOTOOLCHAIN=local, readonly module checksums, GOMAXPROCS=2,
local runner-temp cache paths set in a step (not runner context in job env).
`WORKTOOL_PGTEST_ALLOW=isolated-synthetic-schema-only` and both OWNER_DSN/STORE_DSN
are mandatory. Harness validates same 127.0.0.1/port/nonempty wt_test_ DB, different
users. No implicit localhost default or external DSN accepted.

Runs only `go test -json -count=1 -p 1 -timeout 10m ./internal/store/pg -run
^TestPG(Integration|Admission(ATC|Payload)|LeaseLF)`; job total15min, pull timeout3min, current test case context20s.
The exact 22 expected top-level test IDs (4 schema groups + ATC01–08 + 2 payload/migration-guard groups + LF01–08) must each PASS; any SKIP, failure, missing test,
non-JSON record or failed process makes CI fail. New reviewed test IDs require
updating this explicit set. Output is sanitized ID/PASS and resource/version/
migration SHA256 evidence, not raw Go error strings. Tests must not open more
than the reviewed six simultaneous connections; pool/load testing is excluded.

Python finally + shell EXIT trap + workflow always() cleanup each target only
the validated run-derived container name AND matching run label. No prune,
shared volume deletion or generic process kill. Cleanup confirms no named
container remains before deleting only that run's private directory. A daemon
error is failure, not proof of absence. SIGKILL/runner loss can prevent cleanup;
GitHub ephemeral runner teardown is the last-resort boundary, not a claimed
successful explicit cleanup.

## Checks actually performed

- `bash -n scripts/pg-integration-ci.sh`: PASS.
- Python in-memory compile: PASS, no bytecode artifacts.
- `bash scripts/pg-integration-ci.sh --selftest`: PASS. Pure fixture gates cover
  resource/identity/mount/namespace/port constraints, missing/unsafe DSNs,
  missing/failed/skipped results and diagnostic secret-filtering. readiness subprocess uses a pure recording fake with an exact TCP argv
  assertion; all other subprocess calls use a rejecting stub. Selftest cannot
  execute Docker, Go, PG or network.
- `git diff --check` on candidate files: PASS.

These are source/pure checks, **not PostgreSQL integration evidence**.

## Admission candidate extension

The original 4 PostgreSQL schema/lifecycle groups passed on source head 1ee5db0.
This candidate additionally requires ATC01–08 and both payload/migration-guard groups; those admission tests are not yet
claimed run. Their counters measure internal capacity quotas, not payments or fees.
The runner rejects logs containing only the previous four passes. No new services,
privileges, credentials retained, image, network exposure or resource budget are added.
Tenant-wide serialization is a correctness baseline, not a throughput benchmark.

## Lease/fence candidate extension

The preceding 14 groups passed actual PostgreSQL CI on source bab1a3c. This
candidate additionally requires LF01–08; those lease tests are not yet claimed
run. Receipt of a DB claim is not a socket-send or device-effect proof. The
runner rejects logs containing only the previous 14 passes. Resources, image,
roles, network exposure, cleanup and permission bounds are unchanged.
