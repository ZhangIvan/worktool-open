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
- Single dedicated reader, serial handler processor and single writer. The reader
  starts before HELLO activation/PG and performs bounded complete `Read` → budget
  → nonblocking FIFO offer independently of PG. There are still at most 16 live/
  pending sockets including displaced sockets, 65536-byte data messages, 8
  messages/s burst16, 64KiB/s burst128KiB, and 256 messages/512KiB per session.
  The new ingress reservation is 16 items/1MiB of `cap(raw)`, including HELLO and
  the one processor-owned item until its PG/response-enqueue work returns.
  Queue overflow cancels immediately and selects 1008/RESOURCE_LIMIT; queued
  messages are discarded during joined cleanup, not processed after cancellation.
  Outbound queue remains 16/1MiB; ACK coalescing precedes server sequence allocation.
  A failed write closes before any later sequence. A PG call begun before stop
  can already have committed; ingress close does not imply zero durable writes.
  The reader samples the production clock after each successful complete WS
  message Read, not packet arrival. It never blocks for PG or queue space, but
  scheduler/library/network buffering still precedes this observation. Control
  frames are handled inside coder, not counted as data-message admissions. The
  retained-raw bound excludes one current Read allocation, decoder/PG copies,
  outbound allocations, library/kernel buffers and Go allocator/GC overhead.
  This is not a wire-arrival rate limiter or whole-process memory bound. Real
  wall-clock/slow-PG/slow-receiver behavior of this split remains NOT_RUN.
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

`TestPGGatewayGW01` through `GW16` require all three existing PG gate variables and
`GITHUB_ACTIONS=true`. No gate means explicit top-level SKIP before opening PG or
sockets. Approved isolated CI must require all 16 plus the existing 22 PG cases (38 total);
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
GW10 `rate_bound` retains real PG, 24 actual socket frames, closure and zero-inbox
assertions, but freezes only the private budget clock before connection startup.
This removes dependence on runner/PG processing speed for the burst-enforcement
assertion. Production construction always uses `time.Now`; PG authorization,
lease/session/heartbeat clocks, frame timestamps and all timeouts are unchanged.
It is deterministic budget integration evidence, not real wall-clock rate or
load/backpressure validation. Pure queue/budget tests do not substitute for these
network cases. The real
transaction uncertainty injection is a wrapper around pgx.Tx.Commit: it first
commits PostgreSQL, then returns a synthetic error; it is not merely a successful
business callback returning an error, and it is not a network fault experiment.

### Bounded close correction

The pinned WebSocket library's concurrent `CloseNow` waits when a graceful `Close`
is already in progress; it is not an interrupt in that state. The gateway now
captures only the raw transport returned by its own successful HTTP hijack. A
200ms close deadline closes that exact transport, then joins the close worker and
any fired deadline callback before retiring the lease/storage cleanup. Cancellation
still revokes authority immediately. No other connection or failed-hijack socket
is retained. GW10 keeps a peer that does not read replies, the original 5s cleanup
assertion and zero-inbox assertion; no test deadline was extended.

### Reader/processor split candidate

`ingress.go` owns a single-producer bounded FIFO. Dequeue retains count/capacity
until the serial handler finishes activation or frame apply and enqueue. Reader
EOF/error, binary data, cancellation or limit exhaustion stop the connection;
all residual queued references are dropped after workers join. The HELLO read
still has its 5s timeout. Current-state PG authorization, sequence/idempotency,
outbound ACK coalescing and exact-session cleanup retain their prior owners.

Pure fake-reader/queue tests cover burst arrival while the HELLO processor is
held, refilled-rate queue exhaustion, in-flight/capacity accounting, FIFO,
cancellation races, EOF, partial/error and binary reads, and joined cancellation.
These are not real WebSocket/PG or wire-level control/fragment tests. The original
24-frame GW10 flood and existing integration assertions are unchanged. A new
real-wall-clock slow-PG/slow-receiver integration run needs separate authorization.

The existing 200ms exact-transport closer is retained from gateway stop onward.
Coder v1.8.15 read-context cancellation can close the transport. The reader now
uses a sibling context, initialized before publication: authority/queue cancel
immediately, while the transport reader is canceled after normal close or the
200ms exact hard-close. Both siblings still honor request cancellation. Without
a socket, stop cancels the read immediately; finish joins every path. No
execution grace period is granted. The existing GW03 1008-close assertion is
unchanged; actual peer receipt of that status on this candidate remains NOT_RUN. Its read-limit/protocol-error
path can attempt a close write (own 5s timeout) before `Read` returns; this change
does not establish a 200ms deadline from malformed input arrival.

### Next real-ingress acceptance source (GW13–GW16, NOT_RUN)

Four new top-level PG/GW cases make the explicit integration set **38** (old 34
unchanged). Their first action remains the existing no-socket/no-PG gate. Local
pure/compile validation must show all 38 SKIP, never report them as live passes.
The original GW03 1008 and GW10 24-frame/5s/zero-inbox assertions are untouched.

- GW13 holds the exact first tenant PG gate during HELLO, then uses at most four
  valid 65536-byte whitespace-padded heartbeat messages. Bounded completed-Read
  observations must prove a real-clock byte-rate rejection while PG is held,
  before the fixture lock timeout. No activation or ACK may follow cancellation.
- GW14 holds real apply, fills 15 then 16 outstanding reservations, pacing the last
  two messages by 125ms after observed admission. All actual-clock budget takes
  must pass before the final queue-count rejection; missing prerequisites fail.
- GW15 stops a peer that performs no Read after HELLO and requires the existing
  bounded shutdown/join. It does not claim a saturated TCP receive/send buffer.
- GW16 releases a confirmed PG hold with one active plus one queued heartbeat and
  verifies FIFO event ACKs with contiguous server sequences.

The existing private budget clock seam only nonblocking-records the exact
`time.Now()` result, including monotonic time, before returning it unchanged.
A fixed 258-sample channel marks overflow as a failure. Initialization and each
successful complete Read are distinguished and matched to exact payload lengths;
no network-arrival or client-write timestamp is substituted. No production hook,
clock freezing/advancement, resource expansion or timeout increase is added.

All PG holds have a 1.5s phase guard, below the fixture's 2s lock_timeout, plus an
independent 1s rollback cleanup. Exact store backend PIDs and the holding owner PID
establish actual lock blocking; the first post-HELLO barrier is checked before the
pump can tick. The historical proposed 17-message/2.1s schedule is not used. Real
execution remains NOT_RUN pending separate exact-source authorization and review.
