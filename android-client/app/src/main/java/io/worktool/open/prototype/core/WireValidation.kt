package io.worktool.open.prototype.core

import org.erdtman.jcs.JsonCanonicalizer
import org.json.JSONArray
import org.json.JSONObject
import java.security.MessageDigest
import java.time.Instant

/** IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED. Exact v1 whitelist; no remote schema loading. */
internal object WireValidation {
    private val identifier = Regex("[a-z][a-z0-9_-]{2,63}")
    private val digest = Regex("[a-f0-9]{64}")
    private val timestamp = Regex("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z")
    const val MAX_SAFE = 9007199254740991L
    fun exact(o: JSONObject, fields: Set<String>) {
        require(o.keys().asSequence().toSet() == fields) { "SCHEMA_INVALID" }
    }
    fun text(o: JSONObject, key: String): String = (o.get(key) as? String) ?: error("SCHEMA_INVALID")
    fun id(o: JSONObject, key: String): String = text(o,key).also { require(identifier.matches(it)) { "SCHEMA_INVALID" } }
    fun digest(o: JSONObject, key: String): String = text(o,key).also { require(digest.matches(it)) { "SCHEMA_INVALID" } }
    fun bool(o: JSONObject, key: String): Boolean = (o.get(key) as? Boolean) ?: error("SCHEMA_INVALID")
    fun integer(o: JSONObject, key: String, minimum: Long = 1): Long {
        val v = o.get(key)
        require(v is Number && v.toDouble().isFinite()) { "SCHEMA_INVALID" }
        val number = v.toDouble()
        require(number >= minimum && number <= MAX_SAFE && number == kotlin.math.floor(number)) { "SCHEMA_INVALID" }
        return v.toLong()
    }
    fun time(o: JSONObject, key: String): Long {
        val value=text(o,key); require(timestamp.matches(value)) { "SCHEMA_INVALID" }
        return Instant.parse(value).toEpochMilli()
    }
    fun sha256(value: String): String = MessageDigest.getInstance("SHA-256")
        .digest(value.toByteArray(Charsets.UTF_8)).joinToString("") { "%02x".format(it.toInt() and 255) }
    fun canonicalDigest(o: JSONObject): String = sha256(JsonCanonicalizer(o.toString()).encodedString)
    fun payloadDigest(b: JSONObject, deviceId: String): String = canonicalDigest(JSONObject()
        .put("capability", b.getString("capability")).put("payload",b.getJSONObject("payload"))
        .put("expiresAt",b.getString("expiresAt")).put("policyVersion",b.getLong("policyVersion"))
        .put("requiresLocalConfirmation",b.getBoolean("requiresLocalConfirmation")).put("deviceId",deviceId))
    fun frame(o: JSONObject): String {
        require(o.toString().toByteArray(Charsets.UTF_8).size <= 65536) { "FRAME_TOO_LARGE" }
        exact(o,setOf("version","kind","eventId","tenantId","deviceId","sessionId","seq","sentAt","mode","body"))
        require(text(o,"version") in setOf("1.0","1.1") && text(o,"mode")=="synthetic") { "UNSUPPORTED_MODE_OR_VERSION" }
        listOf("eventId","tenantId","deviceId","sessionId").forEach { id(o,it) }
        integer(o,"seq"); time(o,"sentAt")
        val kind=text(o,"kind"); val b=o.getJSONObject("body")
        when(kind) {
            "command" -> command(b, text(o,"version"))
            "stop_request" -> {
                exact(b,setOf("commandId","attemptId","reason","policyVersion"))
                id(b,"commandId"); id(b,"attemptId"); integer(b,"policyVersion")
                require(text(b,"reason") in setOf("operator_cancel","policy_revoked")) { "SCHEMA_INVALID" }
            }
            "result_ack" -> { exact(b,setOf("ackEventId","commandId")); id(b,"ackEventId"); id(b,"commandId") }
            "transport_ack" -> { exact(b,setOf("ackEventId")); id(b,"ackEventId") }
            "config_notification" -> { exact(b,setOf("policyVersion","revoked")); integer(b,"policyVersion"); bool(b,"revoked") }
            else -> error("WRONG_DIRECTION_OR_KIND")
        }
        return kind
    }
    fun command(b: JSONObject, version: String = "1.0") {
        exact(b,setOf("commandId","attemptId","capability","payloadDigest","expiresAt","policyVersion","requiresLocalConfirmation","payload"))
        id(b,"commandId"); id(b,"attemptId"); digest(b,"payloadDigest"); time(b,"expiresAt")
        integer(b,"policyVersion"); val confirmation=bool(b,"requiresLocalConfirmation"); val p=b.getJSONObject("payload")
        when(text(b,"capability")) {
            "message.send_text" -> {
                exact(p,setOf("targetId","text")); id(p,"targetId")
                val text=text(p,"text"); require(text.codePointCount(0,text.length) in 1..4096) { "SCHEMA_INVALID" }
            }
            "fixture.ui.submit_text" -> {
                require(version=="1.1" && confirmation) { "FIXTURE_REQUIRES_1_1_AND_CONFIRMATION" }
                exact(p,setOf("targetPackage","targetActivity","text"))
                val packageName=text(p,"targetPackage"); val activity=text(p,"targetActivity")
                require(packageName=="io.worktool.open.prototype" && activity=="io.worktool.open.prototype.ui.FixtureActivity") { "SCHEMA_INVALID" }
                val value=text(p,"text");require(validUnicode(value) && value.codePointCount(0,value.length) in 1..500) { "SCHEMA_INVALID" }
            }
            "file.export" -> {
                exact(p,setOf("exportId","approvalId","scopeDigest"))
                id(p,"exportId"); id(p,"approvalId"); digest(p,"scopeDigest")
                require(confirmation) { "LOCAL_CONFIRMATION_REQUIRED" }
            }
            else -> error("CAPABILITY_UNAVAILABLE")
        }
    }
    private fun validUnicode(value:String):Boolean {
        var i=0
        while(i<value.length){val c=value[i];if(Character.isHighSurrogate(c)){if(i+1>=value.length||!Character.isLowSurrogate(value[i+1]))return false;i+=2}else{if(Character.isLowSurrogate(c))return false;i++}}
        return true
    }
    fun scopeDigest(deviceId: String, references: List<String>): String = canonicalDigest(JSONObject()
        .put("deviceId",deviceId).put("referenceIds",JSONArray(references)))
}
