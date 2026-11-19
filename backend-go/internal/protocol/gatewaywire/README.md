# Gateway wire P0: pure, bounded protocol adapter

Implements the five functions and typed structures in the reviewed gateway
interface (`interface.json`, SHA-256
`2a52e4d2d4c128f60fa5baa59b041ab57b81b4ea6e830c10b4f0b7e3b663baa2`).
It adds no dependencies, sockets, database work, clock reads, random IDs or retry.
Successful validation/encoding is **not** authorization, delivery or execution.

## Accepted boundary

- `DecodeHello`: exact 1.0 hello; distinct schema-permitted declarations from
  `message.send_text` / `file.export` (including an empty list). Declarations
  confer no authority; server dispatch remains text-only. An explicit 1.1 or
  mixed recognized version offer returns `ErrUnsupportedVersion`, never a 1.0
  negotiation. A `drivers` key is never accepted on 1.0. Unsupported-offer error
  classification does not assert that its future-version body is valid.
- `EncodeHelloAck`: exactly the existing ten fields, with fixed synthetic mode,
  1.0, heartbeat 15 seconds and max frame 65536. No extra capabilities field.
- `DecodeDeviceFrame`: accepted, started, execution_result, transport_ack,
  heartbeat and device_event only. Every envelope, body and nested data object
  has exact fields. One typed union member is returned. Parent `protocol.Decode`
  supplies duplicate-key, invalid UTF-8, surrogate, depth and trailing-data checks.
- `EncodeServerFrame`: exactly one matching union member. Only text commands,
  transport_ack, result_ack, stop_request and config_notification. Text target
  must be synthetic, local confirmation is false, and payload digest must match.
  The caller assigns event ID, sequence and whole-second UTC time.
- `CommandDigest`: SHA-256 of the six-field command projection; ignores command
  ID, attempt ID and supplied digest so it can run before assigning those fields.
  The encoder independently validates these IDs and the supplied digest.

## Canonical bytes and limits

The serializer is restricted to the projected ASCII-key objects, safe integers,
booleans, valid Unicode strings and string arrays. It is **not full generic JCS**.
There is no float64 intermediary, HTML escaping, Unicode normalization or
unnecessary U+2028/U+2029 escaping. Integral JSON number forms normalize exactly.

`FrameDigest` covers the entire normalized inbound envelope. `ResultDigest`
covers only the immutable execution-result body; it is empty for other kinds.
New session/event/sequence/time changes the former, not the latter. Callers must
keep these purposes separate and must not mutate validated values before use.
Result code allows 1–128 Unicode scalar values, including escaped NUL. Canonical
bytes retain NUL as textual `\u0000`; do not re-encode decoded NUL into PostgreSQL
JSONB. Text instead permits 1–4096 **UTF-16 code units**.

Raw ingress: 65536 bytes; canonical device envelope: 8192 bytes; result body:
4096 bytes; accepted/started body: 512 bytes. Outbound text may legitimately
exceed 8192 escaped JSON bytes, but is bounded by 65536. Strings/IDs, evidence
counts, status-effect combinations, safe integers and calendar times are checked.
Freshness, current identity/session/lease/scope, sequence continuity, bounded
traffic, durable dedupe, journal reconciliation, ACK-after-commit and immutable
receipt storage remain gateway/store responsibilities.

## Verification

Tests include a fixed offline independently calculated restricted-canonical
Unicode/NUL golden vector, the existing ASCII client digest vector, direction and
exact-shape negatives, all status-effect combinations, numeric/time boundaries,
UTF-16 versus scalar length, raw/decoded byte caps and envelope/result identity.
Fuzz seeds assert fixed errors and canonical decode idempotence. Tests use only
synthetic values and fixed clocks. A pure-package pass does not establish HTTP,
WebSocket, PostgreSQL or Android interoperability.

Run from `backend-go` with the already-installed official Go toolchain and
`GOTOOLCHAIN=local GOWORK=off GOENV=off GOPROXY=off GOSUMDB=off`:

```text
go test -count=1 -cover ./internal/protocol/gatewaywire
go test -count=1 -race ./internal/protocol/gatewaywire
go vet ./internal/protocol/gatewaywire
go build ./internal/protocol/gatewaywire
go test -run='^$' -fuzz=FuzzPureDecoders -fuzztime=3s -parallel=2 ./internal/protocol/gatewaywire
```
