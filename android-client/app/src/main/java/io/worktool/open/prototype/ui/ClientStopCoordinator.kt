package io.worktool.open.prototype.ui

/** Fixed, content-free stop result. It never asserts that durable journal recovery succeeded. */
data class ClientStopResult(val succeeded: Boolean) {
    fun snapshot(previous: ClientUiSnapshot): ClientUiSnapshot = previous.copy(
        busy = false,
        paused = true,
        wsState = if (succeeded) UiWsState.DISCONNECTED else UiWsState.ERROR,
        confirmation = null,
        statusCode = if (succeeded) "LOCAL_PAUSED" else "MANUAL_REVIEW_REQUIRED",
        errorCode = if (succeeded) previous.errorCode else FAILURE_CODE
    )

    companion object { const val FAILURE_CODE = "STOP_CLEANUP_FAILED" }
}

/** Serial-executor stop path shared by production and pure tests; no disk/network implementation. */
class ClientStopCoordinator {
    private var stopping = false
    private var failed = false

    /** Engine.closed reports a recovery error rather than throwing. Preserve that failure too. */
    fun reportFailure(): Boolean {
        if (!stopping) return false
        failed = true
        return true
    }

    fun stop(
        preserveConsent: Boolean,
        pause: () -> Unit,
        disconnect: () -> Unit,
        clearConsent: () -> Unit,
        closed: () -> Unit,
        finishSnapshot: (ClientStopResult) -> Unit
    ): ClientStopResult {
        check(!stopping) { "STOP_ALREADY_IN_PROGRESS" }
        stopping = true
        failed = false
        var consentAttempted = false
        fun attempt(work: () -> Unit) { try { work() } catch (_: Exception) { failed = true } }
        fun clearOnce() {
            if (!consentAttempted) { consentAttempted = true; attempt(clearConsent) }
        }
        try {
            // Independent attempts: a journal exception must not skip I/O cancellation or recovery.
            attempt(pause)
            attempt(disconnect)
            if (!preserveConsent || failed) clearOnce()
            attempt(closed)
            if (failed) clearOnce() // Recovery may fail after a successful preserved-consent pause.
            var result = ClientStopResult(!failed)
            try { finishSnapshot(result) }
            catch (_: Exception) {
                failed = true
                clearOnce()
                result = ClientStopResult(false)
                // Bounded best effort only; a dead UI dispatcher cannot be made reliable here.
                attempt { finishSnapshot(result) }
            }
            return ClientStopResult(!failed)
        } finally { stopping = false }
    }
}
