# WorkTool Open native Android client

New MIT source, not a repackaged APK. **Native implementation pending compilation and device verification.** No build or real-device success is claimed until CI artifacts and separate device records exist.

## Actual modules
- `core/NativeCommandEngine`: authenticated v1 frame validation, persistent command dispatch, pause/reconnect, event/command deduplication, local approval, result-event ACK binding. Interrupted running work becomes `execution_unknown`, never automatic replay.
- `transport/OkHttpDeviceLink`: foreground HTTPS pairing/session and WSS protocol; platform trust/hostname checks, no redirects/trust-all.
- `transport/KeystoreCredentialVault`: device secret encrypted with Android Keystore AES-GCM, no backups/logs.
- `transport/SqliteJournal`: tenant/device-separated no-backup SQLite; local synthetic effect and result commit atomically, durable ACK and stop tombstones.
- `ui/MainActivity`: explicit pairing and manual connection, status, durable result/ACK, local export consent. Backgrounding pauses/disconnects; no foreground/background service.
- `ui/FixtureActivity` and `accessibility/FixtureAccessibilityAdapter`: actual app-owned fixture Views and narrow ACTION_SET_TEXT/ACTION_CLICK source. **Accessibility service is not registered or enabled.** Adapter actions are not currently remotely exposed and have not been device-tested.

`message.send_text` writes an **app-local synthetic inbox**, not WeCom. `file.export` produces only a hash of an explicitly approved bundled synthetic note, not real files. The remaining capabilities are **planned_unavailable**; the 67 reference entries are not 67 implemented commands. Public Apifox compatibility is a separate backend module/version.

## Build and trust boundary
Pinned AGP 8.13.2 / Kotlin 2.2.21 / Gradle 8.13 / JDK 17 / compile+target API 36 / min API 26 / Build Tools 35.0.0. Official Google Maven + Maven Central only. Android API 31 is in the supported API range, not proof of successful device execution.

On an environment already provisioned with the licensed official SDK and Gradle 8.13:

```sh
gradle --no-daemon -p android-client :app:testDebugUnitTest :app:lintDebug :app:assembleDebug :app:assembleDebugAndroidTest -Pandroid.builder.sdkDownload=false
```

`.github/workflows/android.yml` uses official SHA-pinned actions and an ordinary GitHub-hosted runner. It checks installed SDK/license files first, disables automatic SDK installation, and does **not** run `sdkmanager --licenses`, an emulator, or connected-device tests. Missing prerequisites fail explicitly. It uploads only the debug APK and static identity/permission/checksum evidence; no keystore, credentials, logs, or user data. Runner/dependency network downloads are build-time only. This job uses ordinary repository Actions limits; it adds no paid service, deploy, secrets or self-hosted runner. GitHub billing/availability remains repository-controlled.

Each clean CI runner generates a disposable debug signing key. A later debug artifact may not update an earlier install with a different key. Do not automatically uninstall or erase data; stable development signing and authorized migration are separate decisions.

## Connection prerequisites
Package `io.worktool.open.prototype`; permission INTERNET only; backups and cleartext disabled. No default endpoint. Pair only with an explicitly chosen self-owned **HTTPS origin** and a one-use enrollment token. Credentials never enter URLs or snapshots. No production endpoint is supplied.

The current Node prototype binds loopback: **a phone cannot reach it merely by typing localhost**. A reviewed, authorized TLS ingress or equivalent isolated test transport is still required. No implicit tunnel, insecure TLS, private-host hardcode or cloud service is added. There is no debug cleartext exception in this slice.

Pairing creates a new device identity; it does not enable execution. Connect/Resume is explicit, foreground-only, and local pause disconnects. Server revocation/stop is asynchronous; effects already committed cannot be undone. This is not a per-effect server execution-permit protocol. Android process death can lose unacknowledged network state, but durable results are resent after re-authentication rather than re-running effects. Local storage integrity failure stops execution for review.

## Validation status
JVM and Android instrumentation test **source exists**, but neither compilation nor Android execution has run in the initial cloud workspace (no Gradle/SDK). CI compile/JVM/lint, APK static inspection, actual installation, foreground reconnect, process-death recovery, and accessibility fixture action tests are separate stages. Do not convert a build PASS into a real WeCom/Android E2E PASS.

Official dependency sources: https://developer.android.com/build/releases/agp-8-13-0-release-notes ; https://github.com/JetBrains/kotlin ; https://github.com/square/okhttp ; https://github.com/erdtman/java-json-canonicalization ; https://developer.android.com/jetpack/androidx/releases/test . New code inherits the repository MIT license; Kotlin/OkHttp/JCS/AndroidX/JUnit/JSON-java retain their own upstream licenses. No original sample, decompiled source, PRIVATE evidence, lamda or vendor service is embedded.
