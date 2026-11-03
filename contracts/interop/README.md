# Android / Go 1.0 shared exchange

This is a shared **synthetic** contract fixture, not a device trace. It reuses the
previously prepared 1.0 reference values and now feeds production Go wire
encoding/decoding and the actual Kotlin NativeCommandEngine JVM tests.

The Kotlin path writes an application-local synthetic inbox effect. It does not
send a WeCom message or click FixtureActivity. The real own-package UI adapter is
an explicit 1.1 capability; Go currently rejects 1.1 rather than downgrading it.
No production protocol, permission, endpoint, or executor is changed here.

## Checks

- Go `TestSharedAndroid10Exchange`: native hello, Go hello_ack, exact command
  digest and JSON, accepted/started/result parsing, transport and result ACKs.
- Kotlin `sharedGoExchangeExecutesOnceAndAcknowledgesExactResult`: same hello and
  Unicode command, actual engine local effect, duplicate delivery suppression,
  expected result body, exact result ACK clearing the pending record.
- Kotlin `sharedUnackedResultSurvivesEngineReplacementWithoutEffectReplay`:
  reuse journal after replacing the engine, resend the immutable result in a new
  session and accept only its matching ACK, with no repeated effect.

The fixture lists selected exchange frames, not every transport ACK the engine
may produce. Each direction has its own sequence space. Kotlin tests substitute
only generated outbound event IDs to compare the fixed exchange.

Run the focused Go test from `backend-go`:

    go test ./internal/protocol/gatewaywire -run '^TestSharedAndroid10Exchange$'

Use the existing licensed Android CI toolchain for JVM tests; the shared resource
is loaded from this directory by Gradle test sourceSets for all three variants.
The tests use MemoryJournal/fake DeviceLink. They do **not** prove SQLite disk
recovery, OkHttp networking, pairing, TLS, physical-device behavior, or the whole
callback chain. JVM execution and device validation must be reported separately.

## Integration boundary still open

The preceding 43-group server/PG/loopback callback validation remains separate
historical evidence. It used a synthetic protocol peer, not Kotlin on Android.
A later explicitly approved test must replace that peer with the actual Android
client over a reviewed private test path, verify SQLite restart/durable ACKs and
receiver signatures, and retain pause/revocation/no-replay assertions. No device
installation, new listener, service permission, or callback URL is authorized by
this fixture. Own-fixture UI 1.1 still requires its separate server contract and
implementation; it is not enabled by these 1.0 tests.
