# Gateway P0 — candidate source, not a deployment

This slice wires actual `net/http` + pinned `coder/websocket` to PostgreSQL store
transactions. It does not start a listener on import, supply production credentials,
or implement a public admin/login plane. `httpapi.NewServer` accepts only literal
loopback bind addresses; the router also rejects non-loopback peers, Origin, query
credentials and cookies. The React management UI is **not connected** to this Go
slice. Android Kotlin and its durable local SQLite ledger are unchanged.

## Implemented scope

- Trusted internal control-plane admin provisioning (current exact admin role is
  rechecked), one-use enrollment, hashed device credentials, fresh device sessions.
  Owner is not admin. Issuing app credentials/bindings remains trusted test setup,
  not a new public endpoint. Secrets are random once-only response values and are
  not logged. Invalid labels containing NUL are rejected before PostgreSQL TEXT;
  this is explicit ingress hardening, not a claim of identical old acceptance sets.
- HTTP enrollment/session return 201 at the Kotlin paths. Session bearer is in
  Authorization, including WS upgrade. Valid header shape precedes durable
  consumption; consume commit precedes 101, then hello, lease acquire, hello ACK,
  dispatch. Failure after consumption burns the token. Commit uncertainty closes
  without retry/reset. A fresh POST session is required after failed activation.
- Version 1.0 accepts the client's two legacy capability declarations, but effective
  dispatch is only synthetic `message.send_text` (external type 203), confirmation
  false. Explicit/mixed 1.1 offers fail, never downgrade. Export, fixture driver,
  callback materialization and all management routes remain unsupported.
- F0 enforced inside the admission transaction. Nonzero active subscriptions fail
  before acceptance/charging; existing general ATC F<=4 entry is unchanged. Frozen
  accepted snapshots stay frozen. Logical result reservation remains materialized,
  not refunded on result ACK. It is internal capacity accounting, never payment.
- Single reader, single writer, 16 live/pending sockets including displaced sockets,
  65536-byte frame cap, 8 frames/s burst16, 64KiB/s burst128KiB, 256 frames/512KiB
  per session. Outbound queue is 16/1MiB; ACK coalescing occurs before consecutive
  server sequence allocation. Failed write closes before any later sequence.
- Current app authorization gates execution; current same-device identity can
  still submit immutable old-attempt evidence after app revocation/expiry. Device
  revocation closes that evidence channel. New session does not mean new attempt.
  Unknown effects are not replayed. Server history, not merely journal strings,
  gates unstarted reconciliation. First result + state/audit/inbox/reservation +
  pending ACK intent are one transaction. Conflicts preserve original result and
  write only trusted redacted metadata audit after rollback.
- Advisory heartbeat/transport ACK/device event frames do not append durable inbox
  rows. First accepted/started/result do; duplicate same result with new event ID
  ACKs that current event without growing the ledger. Logical command/result
  projection plus conservative 32KiB metadata allowance fits the 196608-byte
  reservation; this is **not a physical disk bound** (WAL/index/MVCC excluded).

## Small compatibility changes

0008 is append-only: exact catalog guards replace only the original result-object
JSONB cast with JSON object validation over canonical TEXT, preserving escaped
NUL bytes. Nonrevoked preexisting sessions cause fail-closed migration; no guessed
consumption backfill. Old 0001–0007 SQL remains unchanged.

Public AcquireDeviceLease keeps expectedFence/CAS semantics. Its new private
consumed-nonce entry selects current fence only while holding the same transaction's
tenant/lease/runtime/device/session locks and verifying durable nonce identity.
Same live nonce returns the old deadline unchanged; retired/expired nonce cannot
revive. New session takeover keeps busy/epoch/max-fence checks and repeats PG time
checks after potentially blocking retirement. Claim/close always use captured
handles, not an automatically refreshed tuple. A DB fence cannot recall an already
sent frame or establish physical exactly-once effects.

## Verification boundary

`TestPGGatewayGW01` through `GW12` require all three existing PG gate variables and
`GITHUB_ACTIONS=true`. No gate means explicit top-level SKIP before opening PG or
sockets. Approved isolated CI must require all 12 plus the existing 22 PG cases;
SKIP is not success. Fixture uses distinct nonsuper owner/store roles, unique
synthetic schemas, pool max2, bounded contexts and cleanup. Shutdown joins every
live/replaced connection and exact-close transaction before pool/schema teardown.

The fake device journal uses atomic file replacement, not Android SQLite or a
power-loss guarantee. GW11 commit uncertainty is deterministic driver injection
**after a real transaction commit**, not a TCP fault proxy. Reconnect cases use
bounded samples (four retries), not an endurance/capacity measurement. No real
Android, external receiver, production auth provider, PITR, TLS deployment,
throughput or complete management integration is validated by these tests.

At source preparation: offline pure test/race compilation passed; real PG and
loopback integration remain NOT_RUN until the separately approved official CI.

### Source scenario coverage (all real-PG/socket cases still NOT_RUN)

| Group | Present source assertions |
|---|---|
| GW01–03 | Enrollment replay/owner denial; secret/epoch/device auth; two-cap hello, 1.1 rejection and consumed-token replay |
| GW04–05 | Complete text result/ACK/query; idempotency; subscription insertion barrier/F0 zero-write rejection |
| GW06–07 | Lost result/lost ACK, four reconnect retries, immutable conflict metadata |
| GW08 | Physical replacement, old exact close before/after takeover, same nonce simultaneous acquire, nonce rewrite/consumption mismatch, late command lock across new-session expiry |
| GW09–10 | Revoked/expired app and policy change vs old evidence; revoked device; NUL result, wrong attempt, burst ingress bound; appended migration guard subcases |
| GW11 | Late audit failure rollback and real-commit-then-driver-error uncertainty; no ACK followed by current-session reconciliation |
| GW12 | Join replaced/live sockets before storage cleanup, unknown attempt retained, consumed sessions remain consumed |

Not yet covered by a dedicated real-socket fault case: process kill exactly between
consume/101/hello/lease commit; consume-transaction commit uncertainty; TCP-level
commit response loss; sequence write failure injected between consecutive writes;
100+ reconnect endurance; cross-process/multi-gateway load; exact 512KiB session
byte exhaustion and 1MiB outbound exhaustion under a slow network receiver.
Pure queue/budget tests do not substitute for these network cases. The real
transaction uncertainty injection is a wrapper around pgx.Tx.Commit: it first
commits PostgreSQL, then returns a synthetic error; it is not merely a successful
business callback returning an error, and it is not a network fault experiment.
