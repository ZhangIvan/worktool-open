# G-P0 protocol boundary primitives

Stdlib only. `Decode` enforces the caller's positive byte cap before parsing,
UTF-8 and Unicode scalar escape validity, recursive duplicate-key rejection
(including escape-equivalent names), one JSON document, depth <= 64, and retains
`json.Number` without float64 conversion. Errors never echo input.

`ExactObject` requires a schema-specific explicit key set. It rejects missing,
unknown and case-mismatched keys; it does NOT recursively infer a schema.
`ValidateEnvelope` applies the common fields from contracts/wire.schema.json
(present in this baseline): exact outer keys, version 1.0, known kinds,
synthetic mode, anchored ASCII IDs, safe positive seq, second-resolution UTC
calendar timestamp, object body. The body remains UNVALIDATED. Callers must
validate kind/version-specific nested required keys, types, enums, constraints,
capability coupling, directions and authenticated identity BEFORE any action.

SafeInteger parses exact rational numbers (schema integers 1.0/1e0 accepted),
never float64. Defensive token length <=128 and exponent magnitude <=128 are
additional ingress restrictions, not a claim of unrestricted schema equivalence.
Timestamp parsing rejects leap seconds. Limits/depth are ingress hardening.

Not implemented: full JSON Schema, JCS/RFC8785 canonicalization or digest
verification, handshake/session state, replay/seq monotonic tracking, TTL/clock
skew, authorization, API handlers, storage, network listeners. Returning an
object is not permission to execute it. No service/PG is started by these tests.

Fixtures derive solely from the existing synthetic contracts/example-command.json;
negative variants are generated copies (no runtime credentials or APK material).
Verified with parent-approved local Go 1.27.3: `go test -count=1 -p 1 -v ./internal/protocol` PASS (5 top-level tests; run evidence is archived separately, not included as source). GOTOOLCHAIN=local, GOWORK=off, GOMAXPROCS=2 and workspace-local GOPATH/GOCACHE/GOMODCACHE. No system Go, auto toolchain download, server or database used. `git diff --check` passed.
