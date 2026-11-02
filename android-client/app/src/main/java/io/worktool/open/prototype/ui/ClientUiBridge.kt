package io.worktool.open.prototype.ui

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
    fun resumeAndConnect()
    fun pauseAndDisconnect()
    /** The implementation must recheck command/digest/scope and foreground state before approving. */
    fun decideExport(commandId: String, scopeDigest: String, referenceId: String?, approved: Boolean)
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
    val confirmation: ExportConfirmationUi? = null
)

/** Exact local fixture names. No filesystem paths, chooser, URI input, or file permission. */
object SyntheticUiReferences {
    val allowed: List<String> = listOf("synthetic_note_001", "synthetic_note_002")
}
