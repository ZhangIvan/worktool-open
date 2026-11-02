package io.worktool.open.prototype.core

import org.json.JSONObject

data class Identity(val endpoint: String, val deviceId: String, val keyId: String, val secret: String)
data class Binding(val tenantId: String, val deviceId: String, val sessionId: String, val policyVersion: Long, val serverTimeMs: Long)
data class StoredCommand(val id: String, val digest: String, val attemptId: String, val stage: String, val body: String, val result: String?, val acked: Boolean, val stopped: Boolean)
data class LocalEffect(val kind: String, val referenceId: String, val value: String)
enum class EventDecision { NEW, DUPLICATE, CONFLICT }

interface Journal {
    fun command(id: String): StoredCommand?
    fun accept(body: JSONObject): Boolean
    fun markRunning(id: String)
    fun finish(id: String, result: JSONObject, effect: LocalEffect? = null)
    fun requestStop(id: String, attemptId: String)
    fun event(sessionId: String, eventId: String, digest: String): EventDecision
    fun pendingResults(): List<JSONObject>
    /** All unfinished commands; must not use the UI recent-row limit. */
    fun pendingCommands(): List<StoredCommand>
    fun acknowledge(commandId: String)
    fun recoverInterrupted()
    fun recent(): List<StoredCommand>
    fun effectRows(): List<String>
    fun close()
}
interface CredentialVault {
    fun load(): Identity?
    fun save(identity: Identity)
    fun clear()
}
interface LinkListener {
    fun ready(binding: Binding)
    fun frame(value: JSONObject)
    fun closed(reasonCode: String)
}
interface DeviceLink {
    fun enroll(endpoint: String, enrollmentId: String, enrollmentToken: String, callback: (Result<Identity>) -> Unit)
    fun connect(identity: Identity, listener: LinkListener)
    fun send(kind: String, body: JSONObject): String?
    fun connected(): Boolean
    fun disconnect()
}
interface EngineEvents {
    fun changed()
    fun confirmationRequired(commandId: String, exportId: String, scopeDigest: String)
    fun protocolError(code: String)
}
