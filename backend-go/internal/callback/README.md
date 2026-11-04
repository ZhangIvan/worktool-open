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

## Destination identity phase A

The local candidate now freezes exact URL/key_ref in append-only destination
versions. Admission, materialization, Claim and Prepare use the same frozen
version graph. Rotation deliberately cancels old-version work; it does not drain
it onto the new target. Revocation is irreversible. See the
[phase A contract](../store/pg/callback_destination_versions_ADR.md).

**Migration prerequisite:** any existing callback history or frozen subscription
snapshot makes migration 0009 refuse the upgrade. It does not reconstruct old
URLs from mutable current configuration or delete old data. Destination-only
config may be frozen for future work. This is not an automatic upgrade path for
an existing backlog.

The immutable scope is URL/key_ref, not CIDRs, signature protocol or key bytes.
Their complete configuration identity and production writer remain separate gates.
No default sender or network activity is enabled by this change.

## Validation boundary

Targeted pure/race tests cover projection, transport request policy, copied
configuration, deadline mapping and existing sender/completion logic. They do
not execute PostgreSQL SQL or prove network behavior.

Baseline `d1e334c75aabc8eb313d97441637975e1dd0e4a0` passed all 43 real
PostgreSQL groups, including CB01–CB05, at 2026-11-04 03:28 UTC in
[run 41767151007](https://github.com/ZhangIvan/worktool-open/actions/runs/41767151007).
That isolated run used 2 CPU / 1 GiB and three confirmed cleanup checks. It is
baseline evidence, **not** execution evidence for phase A migration 0009 or its
new negative cases, which remain **NOT_RUN**. The five baseline callback groups are:
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

Production remains blocked on independent review and actual integration of new changes,
complete configuration-content identity and writer composition, production secret and UTC
health providers, egress enforcement, restart/retention operations and measured
capacity. A bounded fixed policy set is not an unrestricted scheduler. The old
38-group result remains evidence for the old baseline, not this candidate.
