# Callback vertical candidate (not deployed)

The default gateway remains 1.0 synthetic/F0. Only trusted explicit
`NewSyntheticCallbackGatewayStore` composition enables frozen subscriptions.
There is no HTTP switch, automatic worker, production key provider or default
external endpoint. No Android/WeCom capability is added.

The candidate connects real components in this order:
HTTP admission → existing WS synthetic executor → immutable result and callback
materialization in the ACK transaction → PostgreSQL Claim/Prepare/Complete →
exact-byte HMAC → one pinned HTTPS attempt. Same event/body survives retries.
The optional transport uses reviewed exact URL/public CIDRs, literal-IP dialing,
normal TLS hostname verification, no proxies/redirects/connection reuse, a shared
2-second deadline and status-only responses. It is not invoked by default.

PostgreSQL scheduling uses shared scope tokens/slots, monotonic delivery and slot
fences, current authorization checks and one-shot prepared markers. Failed or
uncertain commits provide no new send permission. Expired leases require an
explicit bounded RecoverExpired call; no background daemon is installed.
Complete settles an issued attempt without treating later app revocation as
proof the receiver did not already process it. Quota is internal capacity,
never monetary billing. Delivery completion does not release retention quota.

## Validation boundary

Targeted pure/race tests cover projection, transport request policy, copied
configuration, deadline mapping and existing sender/completion logic. They do
not execute PostgreSQL SQL or prove network behavior.

Five proposed gated real integration groups are authored, not run:
- CB01: admission/WS/result/ACK/outbox/receiver HMAC/public result query, duplicate
  device result, one delivered callback and exact slot release.
- CB02: competing claims, prepared marker once, completion replay, app revocation
  between Claim and Prepare with no send permit.
- CB03: explicit expired-lease DB fault injection, prepared versus unprepared
  recovery, successor fence and stale completion. Not a real timeout test.
- CB04: late result-audit fault rolls back the entire materialization; reconnect
  with the original result creates one graph without charging capacity twice.
- CB05: receiver processes a request then closes without a response; authoritative
  retry delay, stable event/body with fresh signature timestamp and one receiver
  effect across two attempts. Receiver deduplication is test-local in memory.

All real groups call the existing isolated-CI gate before any PG or socket.
Only a `_test.go` adapter maps the one synthetic fixture identity to its own
loopback receiver. This is not a production private-address exception. Keys are
random per test and cleared; no real URL or account is accessed.

Production remains blocked on independent review and actual integration results,
configuration-writer locking/immutable version rules, production secret and UTC
health providers, egress enforcement, restart/retention operations and measured
capacity. A bounded fixed policy set is not an unrestricted scheduler. The old
38-group result remains evidence for the old baseline, not this candidate.
