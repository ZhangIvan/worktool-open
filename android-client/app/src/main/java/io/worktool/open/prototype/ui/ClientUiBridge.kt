package io.worktool.open.prototype.ui

import io.worktool.open.prototype.policy.ConnectionMode

/** Implement on the Application. Activities never receive CredentialVault or raw credentials. */
interface ClientUiBridgeProvider {
    val clientUiBridge: ClientUiBridge
}

/** All methods are UI entry points; implementation schedules network/journal work on its serial executor. */
interface ClientUiBridge {
    /** Already-published immutable in-memory copy; must not perform disk or network I/O here. */
    fun snapshot(): ClientUiSnapshot
    /** Publish snapshots on the Android main thread. Attaching must never start a connection. */
    fun attach(listener: ClientUiListener)
    fun detach(listener: ClientUiListener)
    /** Runtime-only user input. Never log or retain the enrollment token in a UI snapshot. */
    fun enroll(endpoint: String, enrollmentId: String, enrollmentToken: String)
    /** Explicit user action only. Never replay an interrupted effect just because the Activity resumes. */
    fun resumeAndConnect(endpoint: String? = null, mode: ConnectionMode = ConnectionMode.SYNTHETIC_1_0)
    fun pauseAndDisconnect()
    /** The implementation must recheck command/digest/scope and foreground state before approving. */
    fun decideExport(commandId: String, scopeDigest: String, referenceId: String?, approved: Boolean)
    /** Read-only probe of the current own-app Fixture window. Never grants or executes an action. */
    fun inspectFixture()
    /** Local per-command consent only; implementation creates/consumes the protected driver ticket. */
    fun decideFixture(commandId: String, attemptId: String, payloadDigest: String, approved: Boolean)
}

fun interface ClientUiListener {
    fun onSnapshot(snapshot: ClientUiSnapshot)
}

enum class UiWsState { DISCONNECTED, CONNECTING, CONNECTED, ERROR }

data class IdentityUiSummary(val deviceIdSuffix: String, val keyIdSuffix: String)
data class LedgerUiRow(
    val commandId: String,
    val stage: String,
    val hasDurableResult: Boolean,
    val resultAcked: Boolean
)
data class SyntheticInboxUiRow(val referenceId: String, val text: String)
data class ExportConfirmationUi(
    val commandId: String,
    val exportId: String,
    val scopeDigest: String,
    /** Intersect effective command scope with the two fixture references. Empty means approval disabled. */
    val allowedReferenceIds: List<String>
)
data class FixtureProbeUi(
    val ready: Boolean,
    val reasonCode: String,
    val packageName: String? = null,
    val activityClass: String? = null,
    val windowId: Int? = null,
    val checkedAtElapsedMs: Long = 0L
)
data class FixtureConfirmationUi(
    val commandId: String,
    val attemptId: String,
    val payloadDigest: String,
    val targetPackage: String,
    val targetActivity: String,
    /** Exact bounded synthetic text, not a truncated preview or secret-bearing diagnostic frame. */
    val text: String
)
data class ClientUiSnapshot(
    val initialized: Boolean = false,
    val paired: Boolean = false,
    val busy: Boolean = false,
    val identity: IdentityUiSummary? = null,
    val wsState: UiWsState = UiWsState.DISCONNECTED,
    val paused: Boolean = true,
    val statusCode: String = "NOT_ATTACHED",
    /** Bounded, content-free machine code only. Never an exception message, URL, frame, or token. */
    val errorCode: String? = null,
    val ledger: List<LedgerUiRow> = emptyList(),
    val inbox: List<SyntheticInboxUiRow> = emptyList(),
    val confirmation: ExportConfirmationUi? = null,
    val connectionMode: ConnectionMode = ConnectionMode.SYNTHETIC_1_0,
    val negotiatedVersion: String? = null,
    val fixtureProbe: FixtureProbeUi? = null,
    val fixtureConfirmation: FixtureConfirmationUi? = null
)

/** Exact local fixture names. No filesystem paths, chooser, URI input, or file permission. */
object SyntheticUiReferences {
    val allowed: List<String> = listOf("synthetic_note_001", "synthetic_note_002")
}
