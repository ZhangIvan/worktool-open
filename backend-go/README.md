# Go backend candidate and opt-in standalone source

Direction: Go modular monolith + **server PostgreSQL**. Android remains Kotlin + **local SQLite** for its journal, deduplication and recovery. Importing packages does not open listeners or create credentials. The new Linux `cmd/worktool-server` entry connects and listens only with explicit `--serve`, validated private isolated-synthetic configuration, a pre-existing database/runtime, and an externally maintained recovery gate. See [STARTUP.md](STARTUP.md); this is not a complete replacement or production deployment.

Implemented here:
- `internal/protocol` and `gatewaywire`: strict JSON and wire 1.0 parsing/encoding, bounded kind-specific synthetic text contracts and canonical digests. A valid frame alone is not authorization.
- `internal/store/pg`: bounded transaction/ambiguous-commit handling, admission, leases, sessions, results and callback candidates; controlled fakes and separately gated real-PG integration tests.
- `internal/httpapi` and `gateway`: device pairing/session and application type-203 synthetic-text routes, actual bounded WebSocket/PG composition, result queries and recovery evidence. They are not a public browser-management plane.
- `internal/consoleapi`, browser store and `startup`: opt-in login/session/logout and tenant-scoped **read-only devices**, with explicit configuration and shutdown boundaries. This new slice still needs its own build/integration evidence; previous CI does not validate new uncommitted code.

The existing Node backend remains the default runnable prototype and behavioral reference. Its original console entry is preserved; `console:go` selects only the new read-only Go preview. Full browser API parity, independent credential provisioning/migration and production rollout are not complete. The separate Accessibility 1.1 candidate is not integrated here. Production callbacks, secrets, recovery and egress remain separately gated.

## Reproduce the bounded checks
Use a verified official **Go 1.27.3** toolchain (do not assume a command named `go` is the intended compiler):
```sh
cd backend-go
GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2 go mod verify
GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2 go test -count=1 -p 1 ./...
GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2 go test -race -count=1 -p 1 ./...
GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=2 go vet ./...
```
Use workspace-local `GOPATH`, `GOMODCACHE` and `GOCACHE` in isolated environments. Dependencies are pinned in `go.mod`/`go.sum`; download preparation uses the normal Go module proxy/checksum database, never a project-provided unknown executable. The race check needs a supported toolchain/C compiler; a blocked command must remain NOT_RUN, not silently counted as passed.

Source is MIT under the root license. Dependency/runtime license notices are preserved in `third-party-licenses/`. Module checksum verification is integrity evidence, **not** a vulnerability audit or production readiness claim. The historical isolated 43-group PG run is scoped to its recorded commit; capacity, production recovery and device tests remain separate gates.
