# Authority PostgreSQL acceptance: offline candidate

**All real PostgreSQL cases: NOT_RUN.** This is a six-file preparation candidate,
not execution approval or evidence of a PostgreSQL pass. Production code, schema,
migrations, workflows and existing diagnostic hash chains remain unchanged.

The `authority_pg` build tag isolates sixteen test groups. An absent Stage A gate
produces skips; skips are never acceptance. The exact unchanged candidate schema
is embedded only in the test binary. No production installer or startup caller is
added. The manifest contains **16 parents and 150 mandatory leaves**; **21 leaves
in A12–A15** additionally require exactly one positive lock-observation marker.

## Preparation and route limitations

The selected environment has Go1.27.3 linux/amd64 but no Docker/Podman executable.
Only offline compilation, pure checks and source review are permitted here.
Neither the run nor cleanup entry was invoked in preparation. No schema, roles,
container, database, network callback, CI, PR publication or deployment was run.

No workflow is added. A new manual-only workflow present only on the draft branch
has no established initial dispatch route. Do not merge, modify the default
branch, install a runtime or substitute a trigger to bypass that limitation.
Later execution requires a separately selected, explicitly authorized
Docker-capable environment and review of the exact candidate and resource scope.

Publishing the Go files would match existing callback-path workflow filters and
trigger broader automatic Go/PG/Node and applicable PR checks, including the old
gateway/callback socket tests. Those publication effects and aggregate concurrent
resources require separate review and authorization. They are not Stage A.

## Offline checks

    bash scripts/authority-pg-ci.sh --selftest

The selftest uses in-memory fixtures. All subprocess launch entrypoints are
replaced with rejecting functions throughout the tests. The actual stream
supervisor, fixture orchestration and cleanup functions execute against recording
fake transports and fake owned state, not Docker or a database. The latest local
check passed 545 assertions. Bash syntax and in-memory Python compilation were
also checked; those results do not verify container compatibility or SQL behavior.

Checks cover every missing identity, proof and mandatory lock marker; malformed
or unknown critical records; post-terminal evidence; explicit null test IDs;
FailedBuild; duplicate JSON keys; independent test/converter failures; secret
redaction; missing/wrongly typed inspect fields; unknown settings; invalid mounts
and namespaces; mixed-stream overflow and deadlines; descendant-process detection;
failed process-group shutdown; uncertain container creation; ownership mismatch;
cleanup failure overriding apparent PASS; and unknown daemon shutdown.

## Future run entry, not permission

The exact execution interlock is `--execute-reviewed-stage-a`. Its presence does
not grant permission. After separate authorization and review, the entry shape is:

    bash scripts/authority-pg-ci.sh run /tmp/worktool-authority-<uid> /absolute/go/bin/go --execute-reviewed-stage-a

Replace `<uid>` with the numeric runner UID. No command above was run. Unknown,
missing, duplicate or extra flags are refused before nonce generation, resource
calls or cleanup. The shell has no EXIT trap; it replaces itself with the Python
supervisor. Only that supervisor can register cleanup after acquiring owned state.

This version requires the pinned image already present. It does **not** pull an
image and rejects `--allow-image-pull`. Container creation explicitly uses `--pull=never`, including if the image
disappears after preflight. Missing image is failure. Image acquisition
is a separate future approved preparation action, not an automatic fallback.
Likewise the build uses only existing verified toolchain/dependency caches with
GOENV off, GOTOOLCHAIN local, GOPROXY off, GOSUMDB off and readonly modules. A future
hosted setup must separately disclose source/toolchain/dependency acquisition.

The whole supplied toolchain tree, not merely its version text, is checked before
build and after conversion. Its deterministic digest hashes each relative filename,
a NUL separator, and that file's SHA256 digest in lexical path order. Symlinks are
rejected. Exact pinned identities are:

- Go version: `go1.27.3`.
- Go executable SHA256: `5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b`.
- Full toolchain tree SHA256: `da3e906bf3b01e50e398146823c6bf2b16a22951597834c5f4808ec38ca5e308`.
- Schema SHA256: `f029a454b01705e2f02479f4280285f0aeeab57824c3d2d259f4bb27ef375294`.

The manifest hash is pinned in the Python harness. The compiled binary hash is
verified by the Go test process and included with the schema, toolchain and
manifest hashes in a successful sanitized report. Source review must still verify
the exact six-file candidate and frozen baseline. Hashes establish content
identity, not authentication or authorization of an arbitrary report.

## Daemon, ownership and cleanup

Only the default local Unix socket `unix:///var/run/docker.sock` is supported.
Ambient DOCKER_* variables and a persisted nondefault Docker context are refused,
not silently overridden. The Docker executable is resolved from fixed system
paths, normalized, then opened through descriptor-pinned directories. The binary
and every ancestor must be root-owned and not group/world writable; set-ID files,
links encountered during the pinned walk, and mutable user installations are
refused. Executable and ancestor device/inode identities, the binary hash, and
the socket device/inode are recorded and rechecked before calls and recovery. Every client invocation receives an explicit host,
a fresh private config containing no credentials or helpers, and a minimal
environment. No remote endpoint or existing registry credentials are used.

The root must be exactly `/tmp/worktool-authority-<uid>`, owner-controlled mode
0700 beneath the trusted root-owned sticky `/tmp`. Directory descriptors pin the
root/run state; file creation, bounded reads, inode checks and removals are relative
to those descriptors with no-follow handling. Secret files have mode 0600; the
binary is 0700. Host tools access the pinned directory through the supervisor's
`/proc/<pid>/fd` path, not a replaceable pathname. Linux/procfs is required.

A persistent exclusive `fixture.lock` admits only one fixture per runner UID on
this approved socket, regardless of nonce. It is released only after exact
container destruction and private-file cleanup are confirmed. Different UIDs or
other independent tools are outside this admission lock; the approved environment
must dedicate this fixture resource budget and must not run competing fixtures.
The lock is not an engine-wide cross-user scheduler.

Container cleanup matches the strong random name/label and, when creation returned
an ID, that recorded full ID. An uncertain create still inspects the exact owned
name/label; it never assumes a timeout means nothing was created. Removal uses the
verified full ID, then a second daemon query must prove absence. No generic prune,
volume purge or broad deletion exists. Unknown ownership or daemon outcome leaves
the lease and files intact and reports CLEANUP_UNCONFIRMED. Partial setup failure
also retains its lease for review, rather than guessing which files may be removed.

Explicit recovery has the form below and is only for a separately reviewed,
recorded owned run on the same executable/socket identity:

    bash scripts/authority-pg-ci.sh cleanup /tmp/worktool-authority-<uid> <recorded-nonce>

It cannot force-clear an unknown/partial lease or adopt a foreign container. A
changed daemon, missing state or mismatched identity needs investigation, not a
retry with another host/context. SIGKILL, lost runner, compromised same-UID/root
administrator or uninterruptible OS I/O cannot establish confirmed cleanup.

## Resource, secret and result boundaries

One official PG18.6 linux/amd64 image is pinned to
`postgres@sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c`.
The fixture uses network none and internal 127.0.0.1:5432, no host ports/binds,
no persistent/anonymous volumes, private IPC/cgroup namespace, runc, no added
capabilities, NET_RAW dropped, no-new-privileges, no restart, and no Docker log
driver. Strict allowlisted inspect data rejects extra or missing fields,
unreviewed namespaces/settings, mounts, propagation, devices and ports.

Limits are 2 CPUs, memory=memory-swap=1GiB, shm=64MiB, data/WAL tmpfs 2GiB,
max_connections=10 and three test sessions. PGDATA is explicitly
`/var/lib/postgresql/18/docker`; fixed readlink commands verify both it and pg_wal
resolve to their expected paths inside the one tmpfs. The binary is copied into
that same tmpfs. The image's writable root layer remains enabled for official
bootstrap/socket compatibility. **No total writable-overlay/host-disk quota is
claimed.** No extra mounts or bootstrap rewrite were introduced to claim one.

The binary timeout is at most ten minutes. The overall supervisor starts before
preflight and reserves sixty seconds for cleanup plus five seconds for emergency
process-group kill/reap within the fifteen-minute budget. Each command and both
streams have independent bounds; the final alarm interrupts even host-side work.
Catchable interruption initiates owned cleanup; repeated ordinary termination
signals are ignored only during bounded cleanup. Hard kills or unresponsive
kernel/daemon operations remain unconfirmed, never converted to success.

Pipe backpressure and capped reads bound combined stdout/stderr memory, with no
unbounded temporary output files. On failure or surviving descendant detection,
the supervisor kills its own process group and reaps its child. Killing a Docker
client does not prove its in-container process stopped; confirmed whole-container
cleanup is required independently. Raw output, SQL, DSNs and errors are never
printed. Only fixed failure codes and validated summary fields leave the harness.

The official image initializes one generated privileged synthetic owner/database.
No application role/GRANT/REVOKE changes, trigger disabling or constraint removal
are permitted. Bootstrap credentials use protected env-file input; this does put
the ephemeral password in container Config.Env, visible to administrators. No
full inspect, environment dump or database logs are collected. Test credentials
travel only through private stdin; exec argv and its clean environment contain
neither password nor DSN. Go authenticates and verifies database/user, address,
port, role flags, ownership, version/settings and exact data directory before DDL.
TCP readiness is checked first, not the temporary bootstrap Unix socket.

The static binary runs only `^TestPGAuthorityA(0[1-9]|1[0-6])$`, with count one and
verbose test2json protocol. Actual Docker-exec exit and pinned host test2json exit
must each be zero. Every exact identity, leaf proof and required lock marker must
appear in order. Unknown AUTHORITY_* output, nonallowlisted SQLSTATE tokens,
skips, failures, malformed types and missing/duplicate terminals fail closed.
Cleanup failure overrides apparent test success. Only TESTS_AND_CLEANUP_CONFIRMED
is a complete harness success, and even that remains an owner-only mechanics result.

## Acceptance limits

The finite matrix tests constraints, immutable/monotone transitions, real adapter
transactions, history, rollback, bounded observed SQL lock races, cancellation
and controlled wrapper commit/cleanup ambiguity. It does not establish nonowner
least privilege, authenticated providers/scope, production writer exclusion,
actual commit-loss transport, crash/PITR durability, receiver acceptance or
manual recovery authorization. Tmpfs destruction cannot prove durable restart.

READ COMMITTED locked-row/event-EXISTS snapshot consistency remains an acceptance
risk, not a confirmed defect. Do not weaken an oracle, retry, or change production
SQL/schema to hide a failure. A reproduced defect needs a separately reviewed fix.
All nine production prerequisites remain unresolved; startup stays closed.

Independent review of the final candidate is still required. Pure checks are not
runtime compatibility evidence. No execution route, user permission or real-PG
PASS follows from this preparation document.
