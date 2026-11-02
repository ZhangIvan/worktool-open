# Android combined candidate interfaces

**IMPLEMENTED_SOURCE / NOT_BUILT / NOT_DEVICE_TESTED.** Integration keeps Android Kotlin plus local SQLite. It does not implement Go WS1.1 or change backend authorization.

## Variant and connection profile

Source-set `VariantFeatures.ownFixtureAccessibility` is true in debug/release, false in lab. It is a build-time ceiling, not permission. Lab never registers the service/FixtureActivity, constructs a driver, advertises the new capability or opens accessibility settings.

`ConnectionMode.SYNTHETIC_1_0` is the default. `ConnectionProfile` creates a hello with only1.0 and the old two capabilities, no drivers. Explicit `OWN_FIXTURE_1_1` requires the feature, captures the exact runtime own target, advertises only1.1 and the independent driver, and requires a1.1 ACK. There is no automatic fallback or in-session upgrade. The current approved secure applicationId is `io.worktool.open.prototype`; any future suffix needs a separate exact target/schema/server-scope review, not a guessed Activity class suffix.

`ClientUiBridge.resumeAndConnect(endpoint:String?=null, mode:ConnectionMode=SYNTHETIC_1_0)` preserves explicit lab origin consent. `inspectFixture()` is observation only. `decideFixture(commandId,attemptId,payloadDigest,approved)` approves only the currently displayed matching task; no UI method manufactures a ticket. Snapshots contain mode/version, bounded status and ID tails, never credentials.

`OkHttpDeviceLink.connect(identity,listener)` remains synthetic1.0; the explicit profile overload is used only by AppGraph after a complete successful stop. The profile is captured by each transport generation, not mutable connection-global configuration. Hello ACK identity/version/shape and every later frame version must match. Server declaration acceptance is not proof of local service enabled; device cap and exact admin scope must be implemented and checked by a future authoritative gateway.

## Threading and cancellation

AppGraph's serial executor owns journal/engine state; UI callbacks use main. `FixtureServiceRegistry` accesses nodes/actions only on main; SQLite receipt operations use its dedicated worker. The shared synchronous `FixtureSessionPermit` is invalidated at stop/mode-switch/lifecycle entry before work is queued. Each operation holds a non-revivable tombstone. `Driver.cancel` marks it synchronously before main.post; post return is not a completed cancellation barrier.

Stop then independently attempts pause, disconnect, consent cleanup, engine.closed and final snapshot. Any failure preserves a fixed sanitized error, busy=false/paused=true, clears both prompt queues/probe and prevents reconnect. A new authorized1.1 session gets a new permit, never revives the old one. Main.onStop and Fixture.onPause invalidate and disconnect; rotation/return do not auto-resume.

Final node-action checks include the captured permit. Invalidation prevents queued work that has not passed that final gate; it cannot retract a submitted system action. Late completion can leave durable evidence/unknown but cannot restore permission, start a new connection or replace an existing terminal.

## Persistence and results

The existing `SqliteJournal` schema/tenant-device separation is unchanged. Before engine.ready on1.0, `ConnectionProfile.canRecover` examines all pending commands and pending results with their persisted command bodies. Any fixture with no result or an unacknowledged result blocks1.0 with `PENDING_FIXTURE_REQUIRES_1_1`. Read/parse errors also reject. No journal clearing, wrong-version result resend, ACK or hidden100-row limit. Only an explicit1.1 connection with current server authorization can process retained evidence; a revoked scope leaves it retained/blocked rather than self-granting access.

`FixtureReceiptStore` persists one-use command/ticket consumption and atomically commits own-fixture effect+receipt. It is a separate database from the command journal, not a distributed atomic transaction. The real button callback is MANUAL_ONLY / COMMITTED / REJECTED: manual actions never synthesize remote receipts; committed output renders the exact receipt text; rejected actions never fall back to manual success. Driver success also requires the corresponding output postcondition. Missing/ambiguous evidence is unknown or the specifically proven partial state. Recovering running work does not execute it again; only durable unacknowledged results are eligible to be resent in a compatible session.

## Validation

Retain original core19, pairing5, stop10, budget11 and existing lab isolation tests. Added fixture routing/permit/recovery/variant tests use fakes, not a device. Actual counts and outcomes await official CI. Static source/manifest-fixture gates are not compiled-APK or device evidence. No new Node, Go, protocol schema, proxy or network listener is introduced by this Android source lane.
