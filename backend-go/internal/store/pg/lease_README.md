# Gateway lease / DB claim candidate

Based on reviewed LF contract MD `8d0fd6b82f3f1e8dadcf9e1590a61dbfb3fd2c30bc0217542cb639058bd97ae1` / JSON `25228768e2f34b3137026db37e6a2b8677bf82c10f1e85450e08458bef68891b`. SOURCE CANDIDATE; real PG acceptance requires the independently reviewed isolated CI gate. Local tests with no explicit PG environment **SKIP**, not PG PASS.

## API and trusted boundary

`NewLeaseStore(runner, localGate)` uses the existing bounded transaction runner. `RuntimeAuthorityFromTrusted` and `GatewayAuthorityFromAuthenticated` are internal bridge constructors, **not authentication functions**. Their fields are private, String is redacted, and no JSON decoding, bearer authentication or HTTP endpoint is implemented. Trusted adapters must derive instance/boot/current recovery epoch independently; the mandatory local gate must enforce the external current recovery epoch and recovery-closed state, without network work inside the transaction.

- AcquireDeviceLease(ctx, gateway, AcquireLeaseRequest{ConnectionID, ExpectedFence})
- RenewDeviceLease(ctx, gateway, LeaseHandle)
- ClaimReadyCommand(ctx, gateway, LeaseHandle, ClaimCommandRequest{CommandID, ExpectedCommandVersion})
- CloseExactSession(ctx, trustedGatewayRuntime, LeaseHandle)
- RecoverExpiredLease(ctx, trustedWorkerRuntime, LeaseHandle)

Only successful, definite commit returns a handle or intent. Commit error returns COMMIT_OUTCOME_UNKNOWN, no send permission and no automatic retry. ClaimOutcome carries either Intent or Reason; replay of an already claimed command never returns another send intent. No attempt is supplied in a client request: it is generated once at claim and retirement reads/CAS-compares the locked original attempt.

## Authorization / atomicity

READ COMMITTED; lock order: tenant gate → exact lease → current runtime → app (Claim only) → device → sorted sessions → binding → command → graph/event/audit/outbox. Whole-tenant serialization is correctness-first; throughput **NOT_MEASURED**. Revoke commits authority changes before any separately ordered lease cleanup. Unsupported user actors, file.export and fixture drivers are explicitly not routed through app authorization.

Final PG clock decision rechecks session/lease/app expiry after locks. Runtime state/role/boot/epoch, device state/epoch, session fingerprint and revocation are current DB facts. App203 claim checks active/current auth_epoch/scopes/binding/policy/capability, canonical payload and six-field command digest, developer message request hash, frozen subscription digest and held reservation graph. Existing token-rotation writers must atomically advance auth_epoch; this module does not implement them.

Connection identity is present in authoritative lease, session snapshot and command snapshot. LeaseUntil is not part of equality CAS; the current locked deadline is read. Fence and command version use int64, refusing exhaustion; they are not added to 1.0 wire or encoded as unsafe JS numbers. Acquire nonce retry only returns a still-live exact previous acquisition. Unknown blocks subsequent claims but does not block control-lease retirement/takeover.

Claim updates queued→delivered, attempt/version, exact dispatch snapshot, event/audit and command_ready pending→claimed atomically. Delivered/claimed mean durable **DB intent**, not sent. Close/recover preserve all result and quota evidence; delivered/accepted/running→execution_unknown once, existing unknown/terminal do not repeat command events. Independent lease audit is allowed. Inflight plus persistent result/terminal history is an integrity error. Unknown is never replayed or automatically cleared.

## Append-only migration 0007

Old 0001–0006 SQL remain byte-identical. New columns are lease connection_id/state, session gateway_connection, command dispatch_connection. Extended snapshot checks, one nonrevoked session and one unresolved command per device, lease identity/fence DML guard. No new tables. Existing lease rows or nonempty dispatch/session snapshots fail closed before schema mutation; no fabricated nonce. Existing duplicate nonrevoked sessions/unresolved commands fail unique creation. This is **not** a reviewed data migration for live production history.

Store role has no TRUNCATE/DDL/trigger-disable grant in the existing harness; normal DELETE of a lease is forbidden. PITR can still roll back the database fence: external recovery gate / old-runtime isolation remain separate requirements.

Two original wrong-session negative fixture SQLs add only synthetic connection/state fields to avoid new completeness checks masking the original foreign-key assertion. Assertions additionally require the exact original FK constraint. No ATC expectation or original migration was changed.

## Test map / evidence boundary

Top-level `TestPGLeaseLF01` … `TestPGLeaseLF08` use the existing explicit isolated owner/store DSN gate, random schema and synthetic data. No default localhost probing or local server startup. CI must require all eight PASS without skips, plus the fourteen existing PG cases.

| Test | Main assertions |
|---|---|
| LF01 | Two pool connections barrier-race ownership; nonce retry no second audit/deadline change |
| LF02 | Session rotation: old close cannot alter new attempt; old inflight becomes unknown and blocks next claim |
| LF03 | Recovery/new fence, stale renewal/close, delete/regression/same-fence identity guards, valid renew |
| LF04 | Two-connection same-command claim, one attempt/event/outbox; discarded response cannot cause replay |
| LF05 | Revoke/epoch/scopes/binding/policy/cancel before claim; lock waits cross PG token/session/lease expiry; prior committed intent retained |
| LF06 | Tenant/device/session/owner/connection/recovery/fence/command/version mismatch, unsupported actors/capabilities, runtime role |
| LF07 | Late audit failures roll back claim and retirement graphs; observed lease-lock wait ends with bounded context cleanup |
| LF08 | Three inflight states, existing unknown/unknown result/terminal, contradictory result, quota preservation; recovery vs next claim barrier |

Additional pure/static tests check trusted-type/identity helpers and migration inventory; they do not parse PostgreSQL SQL. Prior runner fake commit-unknown tests remain in scope as unit evidence, **not real PG commit-fault injection**. Migration production-data reconciliation, PITR, network send/WS session binding, Android effects, unknown-result reconciliation, callback delivery, credential issuance/rotation and load capacity are NOT_IMPLEMENTED/NOT_TESTED here. Android Kotlin+SQLite journal remains unchanged.

Database fencing cannot retract a frame/effect already in flight. A future transport adapter must check exact current session/physical connection/attempt/version immediately before sending, and still needs device-side session binding/journal and a separately tested race boundary. No exactly-once or complete end-to-end isolation claim.
