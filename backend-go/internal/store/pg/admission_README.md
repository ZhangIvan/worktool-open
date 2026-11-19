# Bounded app-text admission candidate

Contract: ADMISSION-TRANSACTION v0.1, Markdown SHA256
`cd0d7573bb62e21c85aab733a27b507957caab50c496d60ee3df94049e04f4d1`.
Only synthetic203 -> message.send_text, one child. This is not an HTTP service,
worker, external callback sender, payment module or production-ready auth stack.
`charged_jobs/charged_bytes` count internal reserved capacity, never money.

## API and trust

`NewAdmissionStore(runner, localRecoveryGate)` requires a nonnil local-only gate.
`AppAuthorityFromAuthenticated(...)` is an explicit bridge for a **trusted auth
adapter**, NOT a token authenticator: it accepts only a fingerprint, never raw
credentials, and exposes no JSON-settable fields. A caller must not wire the
constructor to client-supplied identity JSON. Authentication/HTTP parsing and
production recovery provider are not implemented by this slice. The store
rechecks current active app, fingerprint/credential epoch, expiry, scope, device
state/epoch/capability and binding under the transaction. App creator is provenance;
no operator-role inheritance, owner/admin escalation or user-actor bypass.

`AdmitAppText(ctx, authority, AppTextRequest)` returns Accepted only after a
confirmed commit. Receipt represents original durable acceptance, not current
execution status. The same tenant/app/key with same internally computed digest
returns original stable IDs with no quota update. Changed payload conflicts.
Commit errors return only COMMIT_OUTCOME_UNKNOWN with an empty receipt; do not
blindly retry or change keys. Reconcile original key on the authoritative primary
under current authorization. There is no automatic SQLSTATE retry.

## Transaction invariants

READ COMMITTED, tenant scope FOR UPDATE (even zero subscriptions), then sorted
(C collation) destination scopes, then app/device/binding and destination/version
FOR SHARE. No auth-locked duplicate branch later locks new destinations. All
subscription INSERT/retire/config writers must acquire the same tenant gate;
locking existing rows alone would NOT prevent an empty-set phantom. Configuration
writers aren't exposed by this package and direct DML must not bypass the policy.

Final clock_timestamp decision, after locks/reads, rechecks expiry and safe time
bounds. FOR SHARE does not freeze time. Authorization holds at that decision, not
forever or necessarily at response receipt; dispatch still needs its own current
policy check (not implemented here). Mandatory local gate is checked before work
and again before decision; it must never do network I/O in the transaction.

One command + message receipt + result reservation + F callback reservations,
F<=4, all exact P0 v1.1 amounts, counters, immutable subscription snapshot,
created->authorized->queued events, command_ready outbox and redacted audit commit
together. No result/callback-delivery is fabricated before device execution.
Each result consumes1job/196608bytes; each callback1job/90112bytes in tenant and its
destination view. Overflow-safe subtraction guards multi-scope capacity; all or
nothing. Counter aggregation is a controlled-transaction invariant, not an extra
DDL trigger. Direct DB grants still permit bypass if misused: production must
restrict writers, and any SQL privilege/procedure hardening requires its own review.
Whole-tenant serialization is correctness-first; throughput NOT_MEASURED.

## Canonical subset and time

Internal fixed DTO encoder only: safe nonnegative integers, validated UTF8 strings,
ASCII object keys, bools and bounded arrays; no arbitrary JSON/float JCS framework.
203 body is internally normalized and hashed; supplied payload hashes are not
accepted. DeviceID ASCII1..128 is new hardening, not identical legacy acceptance.
UTF16 text length matches Node limit4096; keys ASCII0x21..0x7e (no space/control).
Commands.digest is separate six-field canonical content including stored-device
policy, confirm=false and UTC expiresAt. DB millisecond created/expires remain exact;
wire UTC strings floor to seconds without .000Z. Snapshot digest uses fixed typed
fields, sorted destinations and canonical bytes, not JSONB display serialization.

`testdata/admission-golden.json` contains only synthetic independent Node/canonicalize
reference inputs/bytes/hashes, no private sample data or workspace paths. Go actually
checks29 cases (19 valid +10 rejected), with additional raw invalidUTF8/surrogate-byte
negative tests. This demonstrates the defined subset, not complete JCS or an HTTP
parser. Upstream JSON duplicate keys/lone escaped surrogates still need strict
parser handling when an HTTP adapter is separately implemented.

## Real PG test sources and current results

`TestPGAdmissionATC01`…`08`: samekey race, changed payload race, final capacity,
multi-scope rollback, both subscription-phantom orders, revocation/expiry across
lock wait (fresh+duplicate), discarded application response reconciliation, and
F0/F4/F5 plus retries after admission closure. Tests use two pgx pool connections
and a third existing fixture control connection, actual pg_locks barriers, no
network side effects. They assert reservation/counter aggregates and full graph
counts. ATC07 deliberately discards application response after confirmed commit;
**not** a driver Commit-lost-response fault-proxy test.

The original strict integration gate and nonowner role harness are reused unchanged.
Missing opt-in/DSNs gives top-level SKIP for every PG group. Local preparation only:
Go compile/pure golden/static/fake tests pass; all14 PG top-level groups (old4+ATC8+payload2)
are **NOT_RUN / SKIP**, pending separately authorized isolated CI execution. No PG
was started or connected locally. Race detector verifies only tests actually run,
not skipped database races. HTTP/auth integration, dispatch revocation enforcement,
worker/PITR and production throughput remain outside this candidate.

## Reviewed storage compatibility increment

Append-only `0006_commands_payload_json.sql` implements the narrow reviewed
payload ADR; original0001..0005 remain byte-identical. It checks target table OID,
payload TEXT type, single expected column CHECK, exact old catalog name/expression,
validation/locality and absent new name under an exclusive table lock, then swaps
only payload object-syntax validation to json_typeof(payload::json). No heuristic
DROP or NOT VALID bypass. Canonical TEXT preserves escaped NUL; result/body JSONB
constraints remain unchanged and are an explicit later compatibility boundary.

`TestPGAdmissionPayloadStorage` covers NUL/full admission + idempotent retry,
byte-for-byte escaped TEXT readback, malformed JSON, JSON scalar/null rejection,
SQL NULL compatibility and rollback preservation. `TestPGAdmissionPayloadMigrationGuard`
builds original-five isolated child schemas and checks expected catalog plus
renamed/changed/missing constraint and wrong-column-type fail-closed paths, keeping
result constraint unchanged. These new real-PG cases remain NOT_RUN locally.

ATC04 additionally injects one test-schema-only audit CHECK failure at the final
write, verifies counters and the complete new-command graph rolled back, removes
the fault and retries the unaccepted key. Ledger assertions filter ad_ commands,
include audit/status and recomputed command digest. ATC06 has separate current
credential epoch/fingerprint/scopes/device epoch/state/capability/binding negatives;
no claim they passed PG before isolated CI execution.
