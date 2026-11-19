# Synthetic backend milestone

Run from repository root with Node24: `node server/main.ts`. Binds only127.0.0.1:3100. Initial interactive bootstrap generates random independent passwords once; never checked-in credentials. Automated tests inject explicitly synthetic bootstrap via createApp. CLI refuses non-interactive first bootstrap rather than emit credentials to captured logs. Console origin exact allowlist3100/5173; no CORS wildcard. API responses and logs do not include passwords after login or device secret except one-time enrollment response. Device session issuance uses Authorization only.

createApp({dbPath,clock?,bootstrap?,origin?,allowedOrigins?}) returns a ready Fastify instance. bootstrap may be one tenant or an array of tenants. Users contain principalId/loginName/password/role, single role. Password min16 in tests; CLI generates24 random bytes. createApp is test/integration interface, not a network bootstrap endpoint.

## Persistence and reset

SQLite WAL with foreign_keys=ON; subset schema is in storage.ts. All foreign relations to users/devices are tenant-composite. Browser sessions intentionally revoked on restart; persisted users, device credentials, enrollment consumption, commands, transition events, idempotency, outbox, audit, exports and tombstones survive. Restart marks delivered/accepted/running as execution_unknown, revokes device sessions and never retransmits them as new work. Simulator must reconnect with credential and may replay durable result for the SAME command/attempt. Queued work reauthorized at dispatch, expires after TTL. Unknown never reexecuted automatically.

Reset is deliberately not an HTTP endpoint. Stop server/simulator, then operator may delete only its chosen synthetic test DB/WAL/SHM under the authorized local workspace; doing so removes audit/dedupe history and requires fresh pairing. Never reset a live DB as retry strategy. Tests use isolated temp DBs and remove them after close.

## ADR-001 proposed subset versus design DDL

This initial runnable slice uses16 tables, not the29-table future design. Public-key credentials, callbacks, production release signing, real exports/object storage, encrypted production content, key-management and membership UI are unavailable. Device secret scheme is synthetic-only. All messages must mode=synthetic; two schemas only, no arbitrary paths/shell/API calls. Source JSON protocol files remain authoritative with Ajv2020+formats and explicit semantic guards.

command_ready outbox and command state persist atomically before network dispatch. Disconnect after delivery becomes unknown, not automatic resend, preserving no-duplicate-side-effect policy. execution_result + audit + result_ack outbox + inbox reply commit atomically; replayed event produces durable ACK; new-session same-attempt result can reconcile uncertain state. No claim of exactly-once UI effects. Metadata/ACK outbox rows retained; no automatic garbage collection in this milestone, so minimum7day tombstones never expire prematurely. Future retention worker needs min-command-epoch enforcement before enabling GC.

Synthetic payloads are stored in local SQLite plaintext for inspectable tests; no production/customer content allowed. SHA256 wire digests follow RFC8785 via canonicalize; cryptographic secret entropy makes token hashes appropriate. Passwords use official Node crypto.scrypt. CSRF random value kept in process memory while hash stored, so browser sessions deliberately reset on restart.

Deletion endpoint provides LOGICAL live-column deletion and revocation only; SQLite WAL/free pages/backups and an offline simulator are not claimed physically erased. It reports deviceEraseState=pending and backupExpiryAt=null. Production crypto-erasure/backup lifecycle is future work. No automatic outbound fetch, callback delivery or APK installation exists.

## Tests

`node --test server/server.test.ts` covers actual SQLite constraints, cross-origin/CSRF, role denial, forbidden body secrets, persisted user/restart browser-session invalidation. Integration tests in root tests are owned by simulator integrator and exercise real loopback HTTP/WS. Unit tests are not evidence of Android behavior or production security completeness.

Export approval binding: requests include scope={deviceId,referenceIds}, whose canonical digest includes BOTH fields. Requester must be operator and device active in its tenant. Command creation AND final dispatch require the approved export's requesting actor and scoped device to match the command actor/device; another operator or device cannot borrow an approval. The approver remains independent and cannot widen scope. Unit tests include both substitution attacks and the authorized positive path.

## Security review corrections: live authorization and object scope

Single role is enforced without owner inheritance. owner reads only health/me overview; device/command operational reads require admin/operator, audit requires admin/auditor, export scopes require requesting operator or independent approver's pending review queue. operator command GET/list/cancel is restricted to requestedBy=self; admin cancel is tenant scoped. Export request/delivery binds requester+device+approved digest AND a currently active approver whose role remains approver and differs from requester.

Command creation and queued dispatch reread submitting principal active/operator. accepted/started ingress also rereads device policy, requester and export approval; loss of authority persists cancel intent + audit and sends stop_request without advancing state. A 1-second scan requests stop for already-delivered/accepted/running commands whose authorization has changed. These are real guards and safe-point stop requests, NOT a synchronous execution permit. transport_ack remains receipt only. The network interval between a device emitting started and applying an irreversible effect cannot be made atomically revocable by this protocol; in that race retain honest effect result or execution_unknown. Stronger pre-effect permit/lease protocol requires separate reviewed contract change; not silently implemented or claimed here.
