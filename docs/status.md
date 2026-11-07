# Implementation and evidence status

Evidence cut-off: 2026-11-02 UTC. This is a partial open-source implementation, not a complete WorkTool-compatible service. A passing build or synthetic test is not proof of real WeCom automation.

## Delivered code boundaries

| Module | Implemented scope | Not delivered / not verified |
|---|---|---|
| Internal control plane | Tenant/RBAC, device pairing, WS, durable command/result/ACK state, approvals, revocation, audit; two synthetic effects | Production deployment and real messaging execution |
| Native Android client | Kotlin pairing, HTTPS/WSS, Keystore credentials, SQLite ledger, dispatch, pause/reconnect, local consent and result ACK; actual native Views | Device execution and lifecycle testing; no enabled accessibility host or WeCom driver |
| Developer API v1 | Separate app credentials/scopes/device bindings, expiry/rotation epochs, type 203 single-target **synthetic** submission, batch/message/child IDs, app-scoped results and callback outbox | Full legacy API compatibility; production callback transport/key custody |
| Callbacks | Signed exact-body HMAC events, bounded retries/deadletter, local synthetic receiver tests | Default network is disabled. Only explicitly configured loopback test receiver is supported; production HTTPS provider returns unavailable |
| Accessibility fixture adapter | Narrow native action API source for own fixture Views | No service registration or system authorization in the first APK; no device action proof |

New developer API identifiers are scoped by **source + API version + message family + direction + type**, not a global number. `/api/developer/v1/messages` accepts only its documented command type 203. Unsupported types are rejected; it does not interpret legacy event payloads. In particular, numeric 101/102 have different meanings in the public event documentation and historical client constants. Do not join those catalogs on the bare number.

## Versioned validation evidence

| Exact source / artifact | Evidence | Limit |
|---|---|---|
| First published native source `12cb1a384cdb093bf1edda1c35da5f84ec64cad9` | [GitHub CI](https://github.com/ZhangIvan/worktool-open/actions/runs/41397454945) built the app and instrumentation APK, ran JVM-test and lint tasks; static APK review passed | The JVM task ran, but no retained JUnit XML was used to assert an exact test-count result. Instrumentation APK compilation is not instrumentation execution |
| First debug APK SHA256 `5f2b04950816e94989efcec0dcf64cf7c12573bfea9451784d3e238729d20439` | Package `io.worktool.open.prototype`, version `0.1.0-native-alpha`, min API 26 / target 36, INTERNET only, v2 signature verified; expected native classes packaged | Device NOT_RUN. Merged APK includes a non-exported AndroidX Startup provider for OkHttp initialization; no service/receiver. This APK predates the pause-state UI fix |
| Developer API reviewed local source `34e66ffcbd00bac4caba7042ddb2363de9bd383f` | Implementer 41 tests, typecheck/build and 5 HTTP checks passed; independent suite 59 tests, typecheck/build and 5 HTTP checks passed | All backend/receiver effects were synthetic and local; these numbers do not silently transfer to later source revisions |
| API conflict-audit fix `2aa48749e0c04ceb8e185eebeeda4770fac1db67` | Implementer reran 28 relevant backend/API tests and typecheck; independent final targeted 3 tests and typecheck passed | Adds redacted conflict audit outside the rolled-back result transaction. It is not a new full 59-test run at this revision |
| Native UI fix `ea10a5565b75814b5181c98edac6bb3b7a63e7d6` | Narrow source review passed: displayed pause state now reads the engine's authoritative gate; a revocation regression assertion was added | New assertion was authored but NOT_RUN at local review time; requires CI for the published revision and a new APK hash. Do not attribute it to the first APK |

The native client only changes its own synthetic inbox / fixture hashes. It does not send messages to real WeCom contacts. Server revocation is asynchronous; already-applied effects cannot be undone. Browser-rendered UI verification remains BLOCKED/NOT_RUN in the original authoring environment; HTTP checks do not replace it.

## Open, traceable implementation gaps

These IDs are backlog references, **not claims that GitHub issues or features already exist**.

| ID | Priority | Gap / acceptance needed |
|---|---|---|
| WTO-001 | P1 | Configuration GET/PUT routes marked M1 in the design inventory are not implemented; implement authenticated/versioned routes and tests or revise their planned milestone label |
| WTO-002 | P1 | Download-grants/download routes are missing; require bound authorization, one-time/replay rules and data-scope tests before delivery |
| WTO-003 | P2 | Release/update routes explicitly return capability unavailable; signed update verification and rollback policy remain future work |
| WTO-004 | P0 for legacy adapter | Formal namespaced legacy event/command mapping and collision rejection tests; never map 101/102 or another bare numeric constant to an operation |
| WTO-005 | P1 by capability | The 67-item reference inventory contains 62 schema-pending entries, two synthetic wire capabilities and three control subsets. It is not 67 frozen or implemented commands |
| WTO-006 | P1 | Actual AccessibilityService host, explicit permission onboarding, lifecycle, event filtering and authorized fixture-driver routing; separate manifest/permission review and user-approved device activation |
| WTO-007 | P1 | Authorized phone-to-self-owned-backend connectivity and real-device queue/ACK/revocation tests; no test ingress or device permission is automatically opened by this repository |
| WTO-008 | P1 before production callbacks | Persistent production key provider, reviewed HTTPS destination policy, retention and operational monitoring; current callback provider remains synthetic-only |
| WTO-009 | Evidence maintenance | Keep the 99 historical PRD NOT_RUN cases unchanged and append assertion-level current evidence. The 247 cross-reference records are not 247 distinct features |

A debug-only LAN candidate, if introduced, is a separate reviewed experiment, not a relaxation of production TLS defaults. No current status authorizes installation, enabling accessibility, logging into WeCom, sending to a real conversation, publishing secrets or deploying a public service.
