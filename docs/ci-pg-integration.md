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

## Gateway P0 candidate extension (not yet run)

The 22 prior groups passed actual PG CI on source 07fb478. This candidate
requires all 22 plus GW01–12 in `internal/gateway`; a prior-only log is failure.
Tests use actual HTTP/WebSocket sockets bound only to ephemeral loopback ports
and synthetic fake-device journals within this same disposable runner and PG
suite. No external listener, Android device, production account, callback
request or additional persistent service is permitted. Tests without the full
existing explicit integration gate must skip before opening any socket or PG.

The fixed image, owner/store roles, memory/CPU/tmpfs limits, 10-minute test
deadline, 15-minute job limit and run-scoped cleanup remain unchanged. Socket
servers must shut down all live/replaced connections before their test-owned PG
pools close; uncertain shutdown is failure, not cleanup evidence. Successful
CI would prove only this 1.0 text/F0 internal slice, not browser control, export,
1.1 fixture support, physical Android interoperability, throughput or production.

## Callback vertical candidate extension (NOT_RUN, requires new batch approval)

Public 14a9363 completed the preceding 38 groups, including GW13–16. The new
candidate requires those 38 plus CB01–05: 43 exact top-level groups. A prior-only
38-pass log is rejected. The existing named-branch trigger, official image,
2 CPU / 1GiB memory+swap / 2GiB tmpfs, 10-minute test / 15-minute job limits,
two ephemeral non-super roles, pool size two per sequential fixture and exact
run-scoped cleanup are unchanged. Gateway fixtures retain their actual 2-second
lock timeout and 10-second idle transaction timeout overrides.

The additional receiver is a test-owned ephemeral loopback HTTP server. A
compile-time test adapter maps only one synthetic reviewed HTTPS identity to
that receiver; production transport policy still refuses private addresses.
No DNS lookup, third-party URL, persistent secret, device or production data is
used. HTTP response-loss is a controlled receiver socket close. CB03's expiry
is explicit DB fault injection, while CB05 observes the actual PG retry delay.
The real HTTP/PG groups are NOT_RUN during source preparation. Publishing would
trigger the expanded suite and therefore requires separate exact batch approval.

## Exact child evidence adapter (source candidate, 2026-11-05)

The preceding head `4cf8e85430502eb22ca77ad867da18c5a0f396a9` passed the
43-group isolated suite in run `41946952315`. Its sanitized report lists parent
groups, not each safety child. That is historical evidence for that head; the
adapter below has **not run PostgreSQL or CI during local preparation**.

The new `pg-integration-ci.evidence.py` validates a pinned, finite
`pg-integration-ci.cases.json` manifest and imports the exact verified bytes of
the existing resource harness. The original `pg-integration-ci.py`, its shell
entry and both browser diagnostic consumers remain byte-for-byte unchanged.
Their hash chain therefore remains valid without changing a browser-triggering
file. The new shell entry preserves the original working directory and cleanup
trap; the PG workflow's always-cleanup step still uses the original entry.

Only the existing PG workflow selects this new entry. Its two added path filters
are `retention*.go` and `runner*.go`; the existing `scripts/pg-integration-ci.*`
filter covers all three new evidence files. No browser workflow, production
startup, Go source, migration, dependency, privilege or resource limit changes.

The original result gate and the stricter gate must both accept the same raw Go
JSON output. In addition to all 43 original parents, the adapter requires:

- All 255 exact `(Package, Test)` child identities in the manifest, each with
  one terminal PASS: CB02 36, CB03 5, CB05 2, GW10 168, schema/device bindings 38,
  and retention audit 6. These are 243 leaf cases and 12 nested parent cases,
  not 255 independent leaf tests.
- Exactly one successful terminal event from each of the two selected packages.
- No observed skip/fail anywhere, including non-required tests or packages;
  no duplicate test/package terminal event; no wrong package for a known parent
  or its children; valid JSON objects and typed identity/action/Output fields.

The list includes all current children under those six roots: 23 corrupted or
missing-receipt no-ACK cases, F0/F1/F4 receipt controls, completion observation,
same-generation worker recovery, retention audit, 0009 version constraints and
the GW10 migration/0010 guard matrices. Go's second `missing_command` child is
explicitly named `missing_command#01`; it is not deduplicated out of the list.
New mandatory tests require a reviewed manifest update and matching digest.
This validates terminal evidence, not a complete Go event-order state machine
or cryptographic authentication of an arbitrary supplied log. The actual run
still requires the real `go test` process to exit zero under the pinned harness.

Diagnostics emit only reviewed package/case IDs, bounded terminal classifications,
allowlisted SQLSTATE values and finite source basenames/line numbers. They never
emit arbitrary test names, raw Output, SQL, DSNs, callback bodies or error text.
The existing 64 source-location cap remains. Missing children can be reported as
NOT_OBSERVED instead of silently relying on a passing parent. The new success
record is printed only after the original run, its Go-exit gate and its Python
finally-cleanup all return successfully. The shell trap and workflow always step
must still independently confirm their original cleanup duties; the new record
alone does not replace those checks.

`--selftest` executes only pure fixtures and original pure resource-gate checks.
It rejects each missing required identity, duplicate/conflicting/skip/fail events,
wrong packages, malformed critical fields, manifest tampering and unknown-output
disclosure. Mocked run cases ensure Go failure, cleanup failure, missing or double
report cannot produce the new success record. It starts no Go process, Docker,
database, listener or browser. A separate local gate check compiles the unchanged
Go packages with PG authorization unset; all 43 groups skip, and the adapter
correctly rejects that stream. Those skips are not PG execution evidence.
