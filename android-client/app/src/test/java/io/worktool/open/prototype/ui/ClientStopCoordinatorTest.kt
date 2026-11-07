package io.worktool.open.prototype.ui

import io.worktool.open.prototype.core.*
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Production coordinator + real engine, with injected storage/I/O faults. Authored; CI NOT_RUN. */
class ClientStopCoordinatorTest {
    private val now = 1893456000000L
    private class FaultJournal : Journal {
        val rows = linkedMapOf<String, StoredCommand>()
        val effects = mutableListOf<LocalEffect>()
        var failPending = false; var failFinish = false; var failRecovery = false
        var recoveryCalls = 0
        fun seed(stage: String = "accepted") {
            val body = JSONObject().put("commandId", "command_demo").put("attemptId", "attempt_demo")
            rows["command_demo"] = StoredCommand("command_demo", "digest_demo", "attempt_demo", stage, body.toString(), null, false, false)
        }
        override fun command(id: String) = rows[id]
        override fun accept(body: JSONObject) = error("NOT_USED")
        override fun markRunning(id: String) = error("NOT_USED")
        override fun finish(id: String, result: JSONObject, effect: LocalEffect?) {
            if (failFinish) error("synthetic raw storage detail must never reach UI")
            val old = rows.getValue(id)
            rows[id] = old.copy(stage = result.getString("status"), result = result.toString())
            if (effect != null) effects.add(effect)
        }
        override fun requestStop(id: String, attemptId: String) = Unit
        override fun event(sessionId: String, eventId: String, digest: String) = EventDecision.NEW
        override fun pendingCommands(): List<StoredCommand> {
            if (failPending) error("synthetic raw query failure")
            return rows.values.filter { it.result == null }
        }
        override fun pendingResults() = rows.values.filter { it.result != null && !it.acked }.map { JSONObject(it.result!!) }
        override fun acknowledge(commandId: String) = Unit
        override fun recoverInterrupted() {
            recoveryCalls++
            if (failRecovery) error("synthetic raw recovery failure")
            rows.values.toList().filter { it.stage == "running" && it.result == null }.forEach {
                finish(it.id, JSONObject().put("commandId", it.id).put("attemptId", it.attemptId)
                    .put("status", "execution_unknown").put("effectState", "unknown")
                    .put("code", "PROCESS_INTERRUPTED").put("evidenceRefs", JSONArray()), null)
            }
        }
        override fun recent() = rows.values.toList()
        override fun effectRows() = emptyList<String>()
        override fun close() = Unit
    }
    private class FaultLink : DeviceLink {
        var online = true; var failDisconnect = false
        var disconnectCalls = 0; var connectCalls = 0
        override fun enroll(endpoint: String, enrollmentId: String, enrollmentToken: String, callback: (Result<Identity>) -> Unit) = error("NOT_USED")
        override fun connect(identity: Identity, listener: LinkListener) { connectCalls++ }
        override fun send(kind: String, body: JSONObject) = if (online) "event_sent" else null
        override fun connected() = online
        override fun disconnect() { disconnectCalls++; if (failDisconnect) error("synthetic raw socket failure"); online = false }
    }
    private inner class Harness {
        val coordinator = ClientStopCoordinator()
        val journal = FaultJournal()
        val link = FaultLink()
        val errors = mutableListOf<String>()
        val events = object : EngineEvents {
            override fun changed() = Unit
            override fun confirmationRequired(commandId: String, exportId: String, scopeDigest: String) = Unit
            override fun protocolError(code: String) { errors.add(code); coordinator.reportFailure() }
        }
        val engine = NativeCommandEngine(journal, link, events) { now }
        var snapshot = ClientUiSnapshot(initialized = true, paired = true, busy = true, paused = false,
            wsState = UiWsState.CONNECTED, confirmation = ExportConfirmationUi("command_demo", "export_demo", "digest_demo", emptyList()))
        val prompts = mutableListOf("command_demo")
        var pairingInFlight = true
        var consentCalls = 0; var closedCalls = 0; var finalSnapshotCalls = 0
        var failConsent = false; var failFirstPublish = false
        init { engine.ready(Binding("tenant_demo", "device_demo", "session_demo", 1, now)); engine.resume(); journal.recoveryCalls = 0 }
        fun stop(preserve: Boolean = false): ClientStopResult = coordinator.stop(
            preserveConsent = preserve,
            pause = { engine.pause() },
            disconnect = { link.disconnect() },
            clearConsent = { consentCalls++; if (failConsent) error("synthetic raw consent failure") },
            closed = { closedCalls++; engine.closed("LOCAL_PAUSE") },
            finishSnapshot = { result ->
                pairingInFlight = false; prompts.clear(); snapshot = result.snapshot(snapshot); finalSnapshotCalls++
                if (failFirstPublish && finalSnapshotCalls == 1) error("synthetic raw dispatcher failure")
            }
        )
        fun attemptResume(): Boolean {
            // Same production admission boundary: a failed stop returns before any new connect.
            if (!stop(preserve = true).succeeded) return false
            link.connect(Identity("https://example.invalid", "device_demo", "key_demo", "synthetic_secret"), engine)
            return true
        }
        fun assertFailed(result: ClientStopResult) {
            assertFalse(result.succeeded); assertTrue(engine.isPaused()); assertTrue(journal.effects.isEmpty())
            assertTrue(link.disconnectCalls > 0); assertEquals(1, closedCalls); assertTrue(journal.recoveryCalls > 0)
            assertFalse(snapshot.busy); assertTrue(snapshot.paused); assertNull(snapshot.confirmation)
            assertTrue(prompts.isEmpty()); assertFalse(pairingInFlight)
            assertEquals(UiWsState.ERROR, snapshot.wsState); assertEquals(ClientStopResult.FAILURE_CODE, snapshot.errorCode)
            assertEquals("MANUAL_REVIEW_REQUIRED", snapshot.statusCode)
            assertFalse(snapshot.toString().contains("synthetic raw"))
        }
    }

    @Test fun pendingQueryFailureStillDisconnectsRecoversAndPublishesFailure() {
        val h = Harness(); h.journal.failPending = true
        h.assertFailed(h.stop()); assertFalse(h.link.online)
    }
    @Test fun finishFailureDoesNotInventReceiptAndStillAttemptsRemainingCleanup() {
        val h = Harness(); h.journal.seed(); h.journal.failFinish = true
        h.assertFailed(h.stop()); assertNull(h.journal.command("command_demo")!!.result)
    }
    @Test fun swallowedRecoveryErrorSignalIsAStopFailureNotSuccess() {
        val h = Harness(); h.journal.seed("running"); h.journal.failRecovery = true
        h.assertFailed(h.stop()); assertEquals(listOf("CLOSED_JOURNAL_RECOVERY_FAILED"), h.errors)
        assertNull(h.journal.command("command_demo")!!.result) // No assertion of durable recovery success.
    }
    @Test fun disconnectExceptionDoesNotSkipRecoveryOrFinalState() {
        val h = Harness(); h.link.failDisconnect = true
        h.assertFailed(h.stop()); assertTrue(h.link.online) // Cancellation was attempted, not falsely claimed complete.
    }
    @Test fun consentExceptionDoesNotSkipRecoveryOrFinalState() {
        val h = Harness(); h.failConsent = true
        h.assertFailed(h.stop()); assertEquals(1, h.consentCalls)
    }
    @Test fun failedStopBlocksResumeAndNeverClearsTheFailureForConnect() {
        val h = Harness(); h.journal.failPending = true
        assertFalse(h.attemptResume()); assertEquals(0, h.link.connectCalls)
        assertEquals(ClientStopResult.FAILURE_CODE, h.snapshot.errorCode); assertEquals(1, h.consentCalls)
    }
    @Test fun normalAndRepeatedStopRemainPausedWithoutNewEffects() {
        val h = Harness()
        assertTrue(h.stop().succeeded); assertTrue(h.stop().succeeded)
        assertEquals(2, h.closedCalls); assertEquals(2, h.journal.recoveryCalls)
        assertTrue(h.engine.isPaused()); assertFalse(h.snapshot.busy); assertNull(h.snapshot.confirmation)
        assertTrue(h.prompts.isEmpty()); assertTrue(h.journal.effects.isEmpty()); assertNull(h.snapshot.errorCode)
    }
    @Test fun successfulResumePreservesExplicitConsentButRecoveryFailureClearsIt() {
        val good = Harness(); assertTrue(good.attemptResume()); assertEquals(0, good.consentCalls); assertEquals(1, good.link.connectCalls)
        val bad = Harness(); bad.journal.failRecovery = true
        assertFalse(bad.attemptResume()); assertEquals(1, bad.consentCalls); assertEquals(0, bad.link.connectCalls)
    }
    @Test fun severalFailuresStillAttemptEveryStageWithoutFabricatingRecovery() {
        val h = Harness(); h.journal.failPending = true; h.journal.failRecovery = true; h.link.failDisconnect = true
        h.assertFailed(h.stop()); assertTrue(h.errors.contains("CLOSED_JOURNAL_RECOVERY_FAILED"))
    }
    @Test fun finalSnapshotFailureGetsOneBoundedSanitizedRetry() {
        val h = Harness(); h.failFirstPublish = true
        val result = h.stop(preserve = true)
        h.assertFailed(result); assertEquals(2, h.finalSnapshotCalls); assertEquals(1, h.consentCalls)
    }
}
