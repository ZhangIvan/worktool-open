# Go backend foundations (not a running replacement server)

Direction: Go modular monolith + **server PostgreSQL**. Android remains Kotlin + **local SQLite** for its journal, deduplication and recovery. This directory does not open listeners, create credentials or connect to a database.

Implemented here:
- `internal/protocol`: strict JSON ingress and common **wire 1.0 outer envelope** checks. Kind-specific body schema, authentication, canonical JCS and business routing are **not implemented** here. A valid outer envelope is not an authorized command.
- `internal/store/pg`: pgx pool adapter, explicit transaction cleanup/ambiguous commit handling, and deployment-wide pool-budget validation. Unit tests use controlled fakes; no real PostgreSQL constraints, concurrency, SQL migration, backup or recovery has run.

The existing Node backend is retained as a frozen behavioral reference, not deleted. These foundations do not expose the 203 API or claim full backend feature parity. The separate Accessibility 1.1 candidate is not integrated here. Callback admission reservation and production write/egress remain gated by design and runtime verification.

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

Source is MIT under the root license. Dependency/runtime license notices are preserved in `third-party-licenses/`. Module checksum verification is integrity evidence, **not** a vulnerability audit or production readiness claim. Dependency vulnerability scanning, real PG integration, capacity tests and device tests are separate gates.
