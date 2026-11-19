# Go foundation CI boundary

This workflow only checks `backend-go` source foundations: formatting, vet, package build, unit tests and race instrumentation. The transaction tests use controlled fakes. **It does not start PostgreSQL, HTTP/WS servers, callback receivers, load tests, Android devices, or production migrations.** A green result is not Go backend parity, PG integration or capacity evidence.

The workflow uses official `actions/checkout` and `actions/setup-go`, fixed commit SHAs, Go 1.27.3, normal module checksums, read-only repository permission, a 10-minute cap and cancellation of superseded runs. Module/build directories live in the ephemeral runner's temporary directory; persistent action cache is disabled. No repository secrets, deployment step, token publishing, self-hosted runner or user endpoints are used. Setup Go's optional token input is explicitly empty.

Dependencies are declared in `backend-go/go.mod` and `go.sum`. Read-only module flags and a git diff check prevent silent lockfile mutation. This workflow is separate from historical Node and Android evidence and does not alter their completed result identities.
