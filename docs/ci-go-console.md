# Go console PostgreSQL/browser batch: NOT_RUN

This is a local source proposal. Publication and execution need separate approval.
It does not modify the existing 43-case PG harness, inventory, migrations or gates.
Its passing pure checks do not count as real PG, browser or socket evidence.

## Exact proposed execution

One trusted-branch push-only workflow on a disposable GitHub-hosted Ubuntu 24.04
runner. No fork, pull_request_target, main change, deployment or device operation.
Read-only repository permission; pinned checkout/setup Actions and existing Go/npm
lockfiles. npm lifecycle scripts remain disabled. The runner must already contain
Chrome and a matching ChromeDriver; absence/mismatch fails without installation.
Chrome keeps its sandbox enabled. No no-sandbox or host security-setting fallback.

One official linux/amd64 PG18.6 image at the existing fixed digest
`sha256:41a369edc91d9bdd7a58be6bc71ea0f8f7a3f49e82818a9f0829021ea7840b7c`.
PG retains 2 CPUs, 1 GiB memory=memory+swap, 64 MiB shared memory, 2 GiB bounded
tmpfs covering data and WAL, ten connections and checked statement/lock/idle
timeouts. Docker inspect gates reject privileged, host-network/PID, foreign mounts
and non-loopback publishing. No persistent volume. Existing resource validators
are reused without changing the original harness.

Go, Vite and ChromeDriver run on the same ephemeral runner, bound to loopback
3101, 5173 and 9515. Existing listeners cause failure, not takeover. There is one
fresh Chrome session/profile and no user's browser or saved credentials. The PG
1 GiB cap is not a claimed aggregate limit for the browser/application processes.
The operation deadline is 10 minutes with a 15-minute job deadline and bounded
individual browser/HTTP waits. Background-network suppression/DNS flags are not
claimed to be a kernel egress firewall. Test navigation targets only the local app;
no callback destinations, external page, device session or commands are created.

## Data and assertions

Generate one fresh wt_test database/schema, distinct temporary bootstrap,
migration-owner and restricted store identities, and four synthetic browser
accounts in two tenants. The app store receives SELECT only and no role membership
or schema CREATE privilege. Apply the existing eight migrations unchanged. Seed
two harmless device metadata rows, an empty second tenant, and a matching active
API/gateway runtime. No device keys usable for authentication or live device
sessions are provisioned. Browser passwords use random per-run PBKDF2 records.

Actual browser phases assert visible login failure, admin/operator exact tenant-A
rows, tenant-B's true empty list, owner denial, logout, user revocation, closed-gate
clearing and restart invalidation. The backend is restarted against the same PG
fixture. Direct HTTP checks verify Host/Origin/duplicate-cookie rejection,
runtime revocation, expired-gate denial, owner-role device denial and server-side
logout invalidation. Each browser logout is also reloaded to reject local-only
success. Fixture-only user/runtime changes are
restored. No claim of actually waiting the full 15-minute browser-session TTL.

Ten exact result groups are required; a missing group fails. The original 43 PG
groups are neither rerun nor relabeled by this new batch. Browser proof uses the
rendered DOM through a repository CI test, not an alternate control channel for
the assistant's current browser. No new browser-testing dependency is installed.

## Secrets, cleanup and evidence

Use 0700 run directories and 0600 credentials/config/gate/session files. Passwords
do not enter argv, GITHUB_ENV, logs or uploaded traces. SQL goes over stdin.
Raw child logs remain private and are deleted. Only fixed result IDs, version,
resource and cleanup evidence is printed; no screenshot/HAR/artifact upload.

Browser session deletion precedes owned process teardown. Go must exit cleanly
before the test reports its restart and final shutdown successful. Cleanup checks
process PID/start-time/group/command identity and exact container name+run label.
It never prunes containers or kills generic browser processes. Verify owned groups,
loopback ports, container, profile and secret directory are gone; failure is not
success. Cleanup runs in finally and the workflow's always step. Runner teardown
is a last resort, not evidence that explicit cleanup passed.

## Current platform and approval boundary

The present cloud workspace has no PG/container engine or accessible cgroup,
so it cannot faithfully reproduce the PG container resource limits. Native PG,
prlimit, a namespace workaround, a public tunnel or spoofed CI environment are
not substitutes. A read-only inventory found its existing Chromium, but it is
not a browser on the GitHub runner and will not be tunneled to this batch.

Before publication, freeze the exact local source manifest and independently
review this batch. Approval must cover publication to the existing draft PR's
worktool/synthetic-prototype branch, resulting existing CI runs, this new one-off
PG/browser workflow, official image/locked-dependency downloads, ephemeral test
identities/data/loopback services, specified revocation/restart checks and cleanup.
No merge, main write, deployment, real credentials/data, device, external callback
or changed host-security settings is included.
