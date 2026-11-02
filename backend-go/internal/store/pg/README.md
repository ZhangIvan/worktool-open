# PostgreSQL plumbing foundation

Candidate Go source; no PostgreSQL is started or contacted. This package neither
installs schema nor executes production SQL. `PoolAdapter` wraps pgxpool acquire /
BeginTx / release; `Runner` supplies bounded acquire, begin, transaction, commit,
and independent rollback contexts. A callback must use its context, do pure DB
work, and never retain the transaction or perform network side effects.
Cancellation is not rollback. Cleanup runs on success, error and panic; failed
rollback/ambiguous commit discards the connection before release. The runner does
not retry. Any Commit error is conservatively `ErrCommitOutcomeUnknown`; query
primary durable idempotency state before deciding what to do, never blindly repeat
business effects. A successful rollback attempt after Commit failure does not
prove the prior Commit was not durable. Error causes may contain sensitive SQL:
callers must expose only classified codes, not raw driver errors to users/logs.

PoolBudget checks deployment-wide sum(replicas*MaxConns)+system+operational reserve
against MaxConnections without integer overflow. Distinct pools need allocations;
a single shared pool is counted once under its owning deployment role. This does
not configure a pool, enforce server max_connections, or coordinate dynamic
replicas. Config validation must precede deployment/admission. Timeouts are >0 and
<=30 seconds; begin/commit fit within total transaction context. Callbacks ignoring
context cannot safely be forcibly interrupted; no detached goroutine is used.

Unit tests use fake pool/connection and a fake pgx.Tx (embedded interface catches
unexpected methods). They test lifecycle, ambiguity, cancellation, panic cleanup,
budget and no-retry behavior. **They do not validate PostgreSQL SQL/FKs/locking,
actual driver wire cancellation, pool discard under network loss or concurrent PG
transactions: all such integration checks remain NOT_RUN.** SQLSTATE retry policy,
role-specific pool construction and DB-side timeout SET LOCAL remain caller /
future integration work. No production readiness claim.

Local verification (2026-11-02): official locally verified Go 1.27.3,
`go test -mod=readonly -p=1 -v ./internal/store/pg` and the same package with
`-race -count=1` both PASS (12 top-level tests plus 2 acquire/begin subtests).
GOTOOLCHAIN=local, GOWORK=off, GOMAXPROCS=2; caches/dependencies are isolated.
`test-race-local.txt` is a local run record, not required source/distribution.
These are fake-adapter tests only; real PostgreSQL validation remains NOT_RUN.
