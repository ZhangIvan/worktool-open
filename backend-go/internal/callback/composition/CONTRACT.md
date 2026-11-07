# Inert callback composition planner, schema 1

This package is an offline input contract, not callback activation. `Parse`
accepts a bounded JSON byte slice and an optional expected content identity. It
returns a private copied plan or an empty disabled plan with a fixed error code.
It never reads a file or environment variable, calls a provider, uses a clock,
resolves DNS, opens a connection, constructs a Runner/Sender/Worker, starts a
goroutine, or installs a CLI/HTTP/environment switch. Current startup remains
isolated-synthetic/F0. Existing SQL, callback runtime behavior, workflows and
resource limits are unchanged. There is no enable or READY result.

## Exact manifest schema

All fields below are required; unknown, missing, differently cased, duplicate
and escape-equivalent duplicate fields are rejected recursively. Null is not a
default. One valid UTF-8 JSON document must fit in 262144 bytes. Existing
`protocol.Decode` enforces depth <=64 and Unicode scalar escape validity.
Integers use canonical unsigned decimal JSON tokens fitting signed int64: no
negative/signed zero, decimal, exponent or string conversion.

```
{
  "version": 1,
  "destinations": [{
    "tenant": "tenant_synthetic",
    "app": "app_synthetic",
    "destination": "destination_synthetic",
    "policy_version": 1,
    "key_version": 1,
    "url": "https://hooks.example.com/callback",
    "cidrs": ["8.8.8.0/24"],
    "signature_profile": "worktool-callback-hmac-sha256-v1",
    "key_ref": "synthetic-key-reference",
    "provider_revision": {"kind": "exact", "id": "revision_001"}
  }],
  "budgets": {
    "runner": {"acquire_ms": 2000, "begin_ms": 2000,
      "transaction_ms": 5000, "commit_ms": 2000, "cleanup_ms": 1000},
    "worker": {"poll_ms": 1000, "recovery_ms": 1000, "run_ms": 1000,
      "shutdown_ms": 10000, "join_headroom_ms": 1000},
    "pool": {"max_connections": 10, "system_reserve": 1,
      "operational_reserve": 1,
      "roles": [{"role": "worker", "replicas": 1, "max_conns": 2}]}
  }
}
```

The example is synthetic declaration data only. It does not approve the named
network or endpoint or represent actual routing, database allocation or keys.
Never put secret bytes in a manifest. `key_ref` and provider revision are opaque
identifiers, not passwords, tokens, key material, key digests or credentials.

## Destination validation and identity

There must be 1–32 distinct `(tenant,destination)` pairs, matching CallbackStore's
stricter limit. A second app/version for the same pair is rejected. Tenant, app
and destination are 1–128 ASCII letters/digits/underscore/hyphen; numeric versions
are positive int64. App is included in the identity even though the existing
callback Binding type has no App field. No assertion authenticates that app.

Existing pure `security.NewPolicy` remains the URL and public-network validator.
URL must already equal the existing canonical HTTPS:443 representation. The
planner does not silently rewrite it. CIDRs are 1–32 entries; validate before
canonicalizing with `netip.Prefix.String`, sort, and reject duplicate canonical
networks. Host bits are never masked to turn invalid input into valid input.
Sharing a URL requires identical normalized CIDR sets. Overlapping but distinct
networks are not merged; equivalent routing coverage need not have one identity.
The existing special-address deny rules still apply to every future pinned DNS
answer; parsing a CIDR does not establish approval, complete public routing or
egress enforcement. No DNS query or transport request occurs.

`key_ref` follows the existing 1–256 printable non-space ASCII reference bound.
Provider revision requires kind `exact` plus a 1–128 identifier token. Known
mutable aliases `latest`, `current`, `active`, `default`, `alias`, `fallback`,
`AWSCURRENT`, `AWSPREVIOUS`, `AWSPENDING` are rejected case-insensitively as whole
tokens or hyphen/underscore-separated tokens. This intentionally narrow local
grammar does not support every provider identifier format. It is not evidence
that any opaque token is actually immutable. No missing revision falls back to
key_ref, no provider is resolved, and no alias is treated as an exact version.

The only accepted signature profile names the existing wire behavior: 32-byte
HMAC-SHA256 key over `eventID + "." + decimalTimestampMS + "." + exactBody`,
lowercase hex signature, existing event/timestamp/signature headers. No header,
signed bytes, verification window, key selection or retry behavior changes.
Unknown schema/profile versions fail closed. A private encoder test changes a
hypothetical profile to prove it participates in the hash; that does not make the
hypothetical profile accepted or implemented.

Identity is `cbplan-v1:sha256:<lowercase hex>` over the UTF-8 domain separator
`worktool-callback-plan-v1` plus NUL plus Go JSON encoding of the fixed schema
struct. Destination entries sort by tenant then destination, CIDR strings sort
lexically, and role allocations sort by role. All manifest fields participate,
including app, versions, signature profile, provider revision and budgets. This
is a versioned encoding contract, not RFC 8785/JCS. Whitespace, object-field order,
destination/CIDR/role ordering and equivalent accepted IPv6 spelling do not
change identity. Same numeric versions with changed content have a different
identity. Expected-identity matching proves local content equality only, not
approval, durable historical immutability, current authorization or ownership.

Plan fields are private; accessors return copies, including nested CIDRs and
role allocations. The caller must not concurrently mutate the input byte slice
while Parse reads it. After Parse returns, input and returned snapshot mutation
do not change the plan. Default formatted strings are redacted; snapshots remain
configuration content and must not be used as log/metric labels. Reflection or
explicit snapshot serialization is not a confidentiality boundary. Errors are
only `CALLBACK_PLAN_INVALID` or `CALLBACK_PLAN_IDENTITY_MISMATCH`, never an echoed
validator/provider/SQL error or supplied manifest content.

## Resource declarations, not resource provisioning

The planner reuses `pg.Timeouts.Validate` and `pg.PoolBudget.Validate` as pure
methods. It constructs no pool/Runner. Every Runner millisecond duration must be
1–30000 before conversion to time.Duration, with Begin/Commit <= Transaction.
Worker bounds mirror enabled NewWorker: poll 100–60000 ms, recovery/run
10–10000 ms, shutdown 10–60000 ms. Tests compare the existing inert constructors
using panic-on-call adapters; no worker is started. Disabled constructor bypass
is not used as validation. Parent cancellation can legitimately truncate Runner
budgets, so this package does not invent a summed operation-timeout constraint.

A further planner consistency restriction requires positive declared join
headroom and shutdown >=2000 + 6×CleanupMS + headroom. Thus Cleanup=1000 requires
more than 8000 ms, while Cleanup=30000 cannot fit the unchanged 60000 ms Worker
ceiling. Arithmetic is bounded before conversion/addition. This is a conservative
cooperative remaining-join allowance for independent settlement, optional
observation, and rollback/discard cleanup, not a hard deadline or measured SLA.
Noncooperative adapters can hang indefinitely. Future owners still must retain
dependencies until an actual join and must not resume on an uncertainty halt.

The declared deployment-wide pool budget has 1–3 unique existing role kinds
api/gateway/worker and must include worker. Existing overflow-safe arithmetic
checks reserves, replicas, connection caps and total allocation. The worker role
label does not grant DB privileges; the claimed max_connections, reserves and
replica counts are unverified inputs, not observations or permission to scale.
A shared physical pool is listed once, per the existing budget contract. A plan
does not enlarge any actual CI/database/deployment resource allowance.

## Permanently unresolved in this slice

Every plan, even zero-value and matching-identity plans, returns the same nine
bounded unresolved prerequisites: durable configuration binding; exact-version
secret provider; sticky generation authority; UTC health; approved DNS/egress;
worker DB role; durable halt/reconciliation; joint lifecycle/recovery ownership;
deployment/receiver evidence. No input can clear them or grant a send permit.

Next dependent engineering work is a separately reviewed durable configuration
identity/writer and trusted exact-version provider contract, plus halt/restart
authority. Migration 0009 freezes only URL/key_ref, not all content declared
here. This planner does not fill historical evidence gaps, backfill identity,
make policy removal safe, settle prior leases, clear a halt or provide PITR/
cross-generation recovery. No worker activation should follow parser success.
Receiver key selection, durable dedupe, safe rotation and operator/deployment
acceptance remain independent gates. No retention deletion/quota release occurs.

## Verification boundary

Pure tests cover strict decoding, identity binding, ordering, copy isolation,
default-off/error redaction, scalar/cardinality/resource boundaries, inert
constructor parity with panic adapters and concurrent parsing/readers. A source
guard rejects new imports, init/goroutine work and unreviewed helper calls.
These checks are not a sandbox proof against arbitrary future source changes;
review actual call sites and unchanged startup imports before publication.
Race, vet and build evidence is frozen separately with its exact baseline and
source hashes. No new real PostgreSQL, CI, DNS, callback network, production
service, browser, device or deployment execution is asserted by these tests.
