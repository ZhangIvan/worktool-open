# Lab client permit/budget candidate

Base: `c0c887b1dc8cd894414dde0d81377aaac45fa85b`, independent branch `worktool/lab-budget-candidate`. Scope is client source only. **IMPLEMENTED_NOT_BUILT / tests NOT_RUN / NOT_DEVICE_TESTED**. No proxy, listener, credentials, deployment, SDK download, installation, permission change or device connection was performed.

## Narrow merge boundary

`ClientBudget` in main has only epoch validity, short admission, inbound-frame bytes, distinct-command admission, callback slots and invalidation observers. It contains no endpoint/HTTP allowance. `EndpointPolicy.budget` defaults to `UnrestrictedClientBudget`; ordinary debug/release continue using their HTTPS-only factories. Only `src/lab/LabClientBudget` owns the numeric limits, monotonic clock and expiry timer. No `BuildConfig LAB` branches are introduced.

For a future separately reviewed Accessibility merge, inject the same budget and epoch into that engine's gates and bounded serial callbacks. This candidate does not modify the Accessibility worktree, add a 1.1 capability, register a service or authorize that merge. Named engine arguments avoid changing the final wall-clock lambda parameter; original 19 core test bodies are untouched.

## Lease and cancellation

- An explicit foreground UI confirmation starts an origin-bound permit **before** queueing enrollment/resume work. A delayed serial task cannot start or renew the timer. Pairing identity storage is not consent.
- The default clock is `SystemClock.elapsedRealtime`; an independent scheduled timer expires at 300000 ms even with no received frames. Every admission also checks the monotonic deadline. Duplicate confirmation on an active permit does not extend the deadline; receive, hello, policy notification, retry and duplicate command never grant a lease.
- Cancellation/expiry invalidates its epoch and origin, invalidates queued connection/pairing intents, and disallows old callback/command/effect admission. Cleanup runs off the UI/expiry caller and cancels HTTP/WS only if they still carry that epoch. A newly explicitly confirmed epoch is not accidentally closed by stale cleanup.
- Serial cleanup pauses the engine, clears approvals, and durably recovers unfinished running intents as unknown. No ACK or cancellation receipt is fabricated. A completed effect cannot be undone. A short `runIfValid` admission linearizes an effect or identity acceptance; it does not hold a permit lock across disk/network waits, and an already-admitted operation may finish after expiry. This is not an instantaneous rollback claim.
- A later explicitly confirmed session can retransmit durable results, but never silently replay a command. There is no automatic reconnect or lease renewal.

## Work limits

| Limit per permit | Enforcement |
|---|---|
| 32 distinct command IDs | Before `journal.accept` stores the body. Duplicates use the same ID slot, still consume frame budget, and retain existing persistent execution dedupe. The 33rd ID invalidates the permit. |
| 256 inbound protocol frames | Before JSON parsing and serial enqueue, including hello ACK and duplicates. A duplicate hello cannot reset counters. |
| 1 MiB aggregate decoded UTF-8 text | Measured on each received text message, not inferred from Java string length; the first byte overflow invalidates the permit. Existing per-message 64 KiB check remains. |
| 32 pending/running inbound callback slots | Acquired before serial executor submission, released exactly once in `finally`; cancellation or a new epoch cannot cause an old release to expand the new queue. Overflow closes/pauses instead of silently ACKing a dropped command. |

The callback bound covers ready, wire-frame, close and pairing-response callbacks. UI action submissions are separately governed by existing UI/intent gates; this is not a claim that every executor or OkHttp internal queue has a universal memory bound.

## Residual blockers deliberately retained

OkHttp supplies an already assembled text message to `onMessage`; its 64 KiB/aggregate checks do **not** bound allocation before assembly, socket buffering or malicious fragmented traffic. Cleartext LAN interception/ARP-MITM remains possible; a MITM can bypass a legitimate ingress and send oversized frames. No full-chain memory, authentication or confidentiality guarantee is made.

The separately approved design's ingress peer/identity/path limits, HTTP/chunked/fragmentation/header deadlines, total TCP/HTTP/WS concurrency and shutdown/temporary-credential cleanup remain **NOT_IMPLEMENTED / NOT_RUN / deployment blockers**. This change does not implement a proxy or authorize one to be started.

## Tests and evidence

Eleven new fake-clock/queue test cases are authored in `src/testLab/.../LabClientBudgetTest.kt`: 299999/300000 idle expiry; duplicate confirmation; 32/33 command boundary and duplicates; frame 257; aggregate bytes; callback 33 and finally release; old-slot/new-epoch isolation; cancelled queued pair/resume/delayed response; expiry before callback execution; duplicate notification/command cannot renew; timer scheduling failure fails closed. Existing endpoint tests use injected fake clock/timer; normal debug/release tests assert their budget remains unrestricted while HTTP remains forbidden.

All Kotlin/JVM/device tests and APK builds remain **NOT_RUN** locally. Only the offline source isolation gate and whitespace check were executed. `android-client/tools/verify_variant_isolation.py` additionally checks command admission ordering, frame-before-JSON ordering, callback finally release, and excludes lab budget classes from ordinary source/artifacts. Artifact isolation requires actual separately built APKs and an existing official SDK analyzer; it was not run here.
