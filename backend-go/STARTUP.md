# Go standalone and read-only console candidate

This is an opt-in Linux, loopback-only **synthetic** source slice. It is not a
production deployment, a complete Node replacement, a database migration, or an
Android/device integration. Existing Node entry points remain unchanged.

## Implemented path

`cmd/worktool-server` composes the existing bounded PostgreSQL runner, gateway
and bearer HTTP router with a separate browser router. The browser slice provides
login, current session, logout and a tenant-scoped **read-only** device list.
Only admin/operator may list devices; owner does not inherit that permission.
The Go console has no enrollment, revoke, command, export or audit controls.
Other bearer routes retain the existing no-Origin/no-Cookie rules.

The dedicated `console:go` / `build:go` frontend mode points to port 3101;
the existing Node mode still points to 3100. Both dev modes use frontend origin
`http://127.0.0.1:5173`, with the proxy preserving that exact Host/Origin.
Run only one dev mode at a time. No frontend or backend service is started by a
build or by reading this document.

## Explicit startup, no automatic provisioning

With an authorized verified Go 1.27.3 toolchain:

```sh
cd backend-go
go build -o ./worktool-server ./cmd/worktool-server
./worktool-server --config /absolute/private/config.json --check-config
./worktool-server --config /absolute/private/config.json --serve
```

`--check-config` checks only the private configuration shape and opens no DB or
listener. `--serve` is explicit and may only be used in a separately authorized
isolated synthetic environment. There is no implicit serve/default DSN, schema
creation, migration, credential generation, runtime registration or data reset.

`config/synthetic.example.json` is a non-secret template, **not a runnable
configuration**. Copy/edit it only in the authorized environment; keep the real
config, password and external recovery-gate files private (0600 or stricter),
outside the repository. Symlinks and broad permissions are rejected. Password
files are read only at explicit serving startup, never printed or included in
CLI arguments; one trailing newline is allowed. Database secrets are not browser
passwords. Do not upload real config, secrets or runtime databases.

Required pre-existing resources:

- PostgreSQL 18.6, a database named `wt_test_<8-64 lowercase hex>`, and a dedicated
  schema named `wt_it_`, `wt_test_` or `wt_console_` followed by 8-64 hex digits.
  Only literal loopback database addresses are accepted. No DNS or alternate
  targets are used. The pool has two connections and bounded timeouts.
- A non-superuser store identity, without create-database, create-role,
  replication, bypass-RLS, role memberships or schema CREATE authority. Apply the
  existing schema separately under the established isolated approval process.
- A pre-provisioned active runtime row whose instance, boot and external recovery
  epoch match the config and whose roles include api and gateway. Startup only
  verifies it; it cannot invent or repair the identity.
- An independently maintained private recovery-gate document, matching those
  identities, explicitly open and expiring within five minutes. It is re-read
  before DB operations. Missing/closed/expired/mismatched gates fail closed.
  The committed example is closed and expired deliberately. This local interlock
  is not a complete production recovery authority or high-availability protocol.
- Explicit synthetic users in the PG users table. Browser password records use
  `worktool-pbkdf2-sha256$600000$<base64 16-byte salt>$<base64 32-byte digest>`.
  Existing Node scrypt records and `!disabled` fixture accounts **cannot log in**.
  No password migration/provisioning helper or default credential is supplied.

## Authentication and shutdown boundaries

Browser sessions are hashed, bounded in memory, expire after 15 minutes and are
lost on restart. Each request rechecks current user authority/password and the
external gate. Device reads also check runtime and user in the same read-only
snapshot. Login derivation is rate/concurrency bounded and occurs outside DB
transactions. Host/Origin, CSRF, duplicate-cookie and loopback-peer checks remain
mandatory. Cookies are HttpOnly/SameSite=Strict, without Domain. They are not
Secure because this candidate deliberately uses loopback HTTP; it is not a TLS
production authentication solution.

Shutdown stops HTTP acceptance and joins HTTP handlers and hijacked WebSockets
while the PG pool remains available. Incomplete joins leave the pool unclosed
and return a nonzero error rather than claim clean cleanup. Durable gateway
cleanup errors are reported separately. Unknown commit outcomes are not retried.

## Verification and remaining work

New tests cover configuration/target rejection, private files, fail-closed gate,
runtime preflight, shutdown ordering, browser isolation/session lifecycle,
tenant/role scope, same-snapshot authorization and frontend session races. Test
fakes/HTTP recorders do not prove real PG SQL, actual socket behavior or browser
rendering. The prior 43-group CI result belongs to baseline 3eca18d's predecessor;
it is not a result for this uncommitted slice.

Real startup with pre-provisioned synthetic PG, full console parity, production
credentials/TLS/recovery authority, actual Android SQLite recovery, devices and
WeCom remain separate uncompleted gates. Do not remove the Node prototype merely
because this slice compiles or its isolated unit tests pass.
