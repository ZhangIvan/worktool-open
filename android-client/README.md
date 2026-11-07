# WorkTool Open native Android candidate

New MIT source, not a repackaged APK. **This combined source is NOT_BUILT / NOT_DEVICE_TESTED until its own official CI and device evidence exists.** Earlier native/Node test results do not validate this combination. No WeCom driver, account, conversation target, install or service enablement is included.

## Three variants and two explicit connection modes

| Variant | Default connection | Optional explicit connection | Components |
|---|---|---|---|
| debug / release | HTTPS/WSS, wire1.0, two synthetic capabilities | Fixture page's explicit own-fixture1.1 button; no fallback | One BIND_ACCESSIBILITY_SERVICE-protected service; non-exported own FixtureActivity |
| lab | Explicit single-origin cleartext lab consent, wire1.0 only | None | No service or FixtureActivity manifest entry; no settings/driver UI entry |

`message.send_text`/external203 remains an **app-local synthetic inbox**. `file.export` hashes an explicitly approved bundled note, not files. The independent `fixture.ui.submit_text` candidate uses actual Accessibility ACTION_SET_TEXT/ACTION_CLICK on this app's fixed fixture widgets only. Service class presence, a true performAction return, or an HTTP accepted response is not successful UI execution. The67 reference entries are not67 implemented commands.

Secure variants default to `SYNTHETIC_1_0`; only a user action on Fixture selects `OWN_FIXTURE_1_1`. The selected mode is in memory, captured per connection, never inferred from service state or a server frame. Switching first invalidates the old execution permit and completes stop; failure blocks connect. An unsupported1.1 server causes a visible failure, never a203 send or automatic1.0 downgrade. UI selection cannot modify paired credentials, grant device capabilities or replace an admin-approved exact driver scope.

The current Go foundation does **not** provide the required WS/session/1.1 gateway. Default1.0 retains compatibility with an independently supported1.0 backend; it does not make Go operational. No old Node server, new endpoint, tunnel or proxy is bundled as a replacement.

## Actual modules and safety gates

- `core/NativeCommandEngine`: exact negotiated version, command/event deduplication, local approval, durable result-event ACK binding, no effect replay. Existing synthetic command policy and lab budget run before command acceptance.
- `policy/ConnectionProfile`: exact1.0 or1.1 hello; full journal preflight before ready/recovery. If1.0 finds any fixture command without result or with an unacknowledged result, including unknown, it closes with `PENDING_FIXTURE_REQUIRES_1_1`. No recent100 shortcut, deletion or wrong-version ACK. Acknowledged immutable terminals are retained without replay.
- `transport/OkHttpDeviceLink`: shared endpoint policy for pairing/session/WS, platform trust and hostname checks for HTTPS/WSS, redirects disabled, generation-gated callbacks; no trust-all/default host.
- `transport/KeystoreCredentialVault` and `SqliteJournal`: unchanged protected credentials and tenant/device SQLite. Android SQLite remains formal persistent storage; server PostgreSQL does not replace it.
- `ui/MainActivity`: default synthetic pairing/manual connection, bounded secret-free snapshots, local export approval. Main.onStop still pauses/disconnects. The Fixture page requires its own explicit connection and local enable; settings merely opens the OS page by user click, never grants permission.
- `accessibility/`: real protected host, exact runtime package filter, two window events and view IDs, `isAccessibilityTool=false`. Root package is checked before traversing fixed unique nodes. No arbitrary selectors, scripts, coordinates, gestures, screenshots or other-app targets.
- `FixtureSessionPermit`: synchronous atomic session/operation invalidation before queued cancellation. Each final main-thread SET_TEXT/CLICK gate rechecks the captured permit, window, target, epochs and deadline. Actions already past the final gate/system-submitted may still complete; stop is not rollback or proof of not_applied.
- `FixtureReceiptStore`: ticket consumption and steps persist; effect+receipt share one local transaction. This is **not** a transaction across the existing command journal. Success additionally requires matching on-screen output. Interrupted/ambiguous work stays unknown; a late receipt does not overwrite an immutable terminal or restore permission.
- `ClientStopCoordinator`: existing independent cleanup attempts and fixed failure state retained. New fixture prompts/probe are cleared; failed cleanup cannot silently resume.

Only INTERNET appears under uses-permission. Secure variants nevertheless add a real BIND-protected service and thus change the permission surface relative to the earlier no-service APK. Lab may contain shared class bytes, but its manifest, feature, UI, constructor and wire gates disable the path.

## Build evidence and deployment limits

### Reconnect result staging candidate (JVM checks NOT_RUN locally)

During `NativeCommandEngine.ready`, previously accepted commands are first
durably rejected with the existing reconfirmation-required result. This recovery
step defers sending until the final pending-result traversal, so each newly
created result is attempted once rather than once during `finish` and again
during replay. Older unacknowledged results are still replayed; acknowledged
rows stay excluded. Normal command completion still attempts immediate delivery.
Result rows remain unacknowledged until the existing exact result-event ACK
check succeeds, and no local effect is replayed or new execution consent granted.

Three new JVM cases cover a single recovered accepted command, mixed old/new
pending results with an acknowledged control, and 101 recovered rows beyond the
UI's recent-row limit. They use an in-memory journal and link, not Android SQLite
or actual sockets. They are authored but not executed locally because the local
environment lacks Kotlin/Gradle and their cached test dependencies. Prior CI is
not execution evidence for this candidate.

This narrow correction removes duplicate replay within one `ready` call. It
does not add outbound pacing, an ACK window or a bounded backlog pump. Large
reconnect backlogs can still exceed the Go gateway's ingress rate/queue bounds;
that remains a separate compatibility and integration gate. No process-death,
real-device, production callback or deployment acceptance is implied.

Pinned existing official inputs: AGP8.13.2, Kotlin2.2.21, Gradle8.13, JDK17, compile/target36, min26, Build Tools35.0.0. On an already licensed/provisioned official environment:

```sh
gradle --no-daemon -p android-client :app:testDebugUnitTest :app:testReleaseUnitTest :app:testLabUnitTest :app:lintDebug :app:lintRelease :app:lintLab :app:assembleDebug :app:assembleRelease :app:assembleLab :app:assembleDebugAndroidTest -Pandroid.builder.sdkDownload=false
```

The existing official CI does not accept new SDK licenses, install an SDK, run an emulator, enable a service or execute instrumentation. Three-variant static artifact checks must decode actual service metadata/resource values, manifest protection, permissions, package and lab DEX isolation. Release remains unsigned, not production/install readiness. Test counts/failures/skips must come from that exact commit's reports, not source counts. Disposable debug signing keys can differ; never automatically uninstall/erase an existing installation.

LAB retains source-enforced300-second monotonic consent,32 unique commands,256 frames/1MiB decoded ingress and32 queued callbacks. These are not a transport allocation cap or protection against cleartext interception. Deployment remains blocked without separately authorized identity-scoped ingress/proxy path/frame/concurrency/session controls and verification. See `LAB_CANDIDATE.md` and `../docs/lab-budget-client.md`.

No source review or CI build is device acceptance. Actual foreground/window events, permission/lifecycle changes, SQLite/Keystore behavior, controls and process-death/result-ACK recovery require later explicit device authorization. Platform distribution and safety boundaries are in `../docs/platform-safety.md`. Third-party dependencies retain their own upstream licenses; no sample APK, decompiled source or private evidence is included.
