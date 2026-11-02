package io.worktool.open.prototype.core

import org.json.JSONArray
import org.json.JSONObject
import io.worktool.open.prototype.policy.ClientBudget
import io.worktool.open.prototype.policy.UnrestrictedClientBudget
import io.worktool.open.prototype.policy.CommandPolicy
import io.worktool.open.prototype.policy.StandardCommandPolicy

/** Actual Android client orchestration; remote effects remain app-local synthetic fixtures.
 * IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED. All entry points serialize on this monitor.
 * DeviceLink owns serial callbacks and envelope creation. EngineEvents must marshal UI to main.
 */
class NativeCommandEngine(
    private val journal: Journal,
    private val link: DeviceLink,
    private val events: EngineEvents,
    private val commandPolicy: CommandPolicy = StandardCommandPolicy,
    private val workBudget: ClientBudget = UnrestrictedClientBudget,
    private val permitEpoch: Long = workBudget.epoch(),
    private val clock: () -> Long = System::currentTimeMillis
) : LinkListener {
    private var binding: Binding? = null
    private var incoming = 0L
    private var paused = true // explicit local resume; pairing does not consent to execution
    private var revoked = false
    private var lastClock = 0L
    private val confirmations = mutableSetOf<String>()
    private val sentResults = mutableMapOf<String, MutableSet<String>>()
    private val fixtureContent = mapOf("synthetic_demo" to "WorkTool app-owned synthetic export fixture v1",
        "synthetic_note_001" to "App-owned synthetic note 001",
        "synthetic_note_002" to "App-owned synthetic note 002")

    @Synchronized override fun ready(binding: Binding) {
        try {
        listOf(binding.tenantId,binding.deviceId,binding.sessionId).forEach {
            WireValidation.id(JSONObject().put("id",it),"id")
        }
        if (binding.policyVersion !in 1L..WireValidation.MAX_SAFE ||
            kotlin.math.abs(clock()-binding.serverTimeMs)>30000) { fail("CLOCK_OR_BINDING_INVALID"); return }
        this.binding=binding; incoming=0; revoked=false; paused=true; confirmations.clear(); sentResults.clear(); lastClock=clock()
        journal.recoverInterrupted()
        // A prior accepted task never resumes an old local confirmation after reconnect/restart.
        journal.pendingCommands().filter { it.stage=="accepted" }.forEach {
            finish(JSONObject(it.body),"rejected","not_started","RECONNECT_RECONFIRMATION_REQUIRED")
        }
        journal.pendingResults().forEach { sendResult(it) }
        events.changed()
        } catch (_: Exception) { fail("READY_OR_JOURNAL_REJECTED") }
    }

    @Synchronized override fun frame(value: JSONObject) {
        try {
            val kind=WireValidation.frame(value)
            val bind=binding ?: error("NO_SESSION")
            require(value.getString("tenantId")==bind.tenantId && value.getString("deviceId")==bind.deviceId &&
                value.getString("sessionId")==bind.sessionId) { "IDENTITY_MISMATCH" }
            require(kotlin.math.abs(WireValidation.time(value,"sentAt")-clock())<=30000) { "STALE_FRAME" }
            val previous=incoming
            // Duplicate lookup precedes seq check as mandated by v1. Journal commits event receipts.
            val decision=journal.event(bind.sessionId,value.getString("eventId"),WireValidation.canonicalDigest(value))
            if (decision==EventDecision.CONFLICT) error("EVENT_CONFLICT")
            if (decision==EventDecision.DUPLICATE) {
                if(kind!="transport_ack") ack(value)
                return
            }
            require(value.getLong("seq")==previous+1) { "SEQ_REJECTED" }
            incoming=value.getLong("seq")
            val body=value.getJSONObject("body")
            when(kind) {
                "command" -> accept(body)
                "stop_request" -> {
                    journal.requestStop(body.getString("commandId"),body.getString("attemptId"))
                    if(body.getString("reason")=="policy_revoked") revoked=true
                    ack(value)
                    journal.command(body.getString("commandId"))?.let { stored ->
                        if(stored.attemptId!=body.getString("attemptId")) error("ATTEMPT_MISMATCH")
                        if(stored.result==null && stored.stage=="accepted") finish(JSONObject(stored.body),"cancelled","not_started","STOP_REQUESTED")
                        else if(stored.result!=null) sendResult(JSONObject(stored.result))
                    }
                }
                "config_notification" -> {
                    require(body.getLong("policyVersion")>=bind.policyVersion) { "STALE_POLICY" }
                    binding=bind.copy(policyVersion=body.getLong("policyVersion"))
                    revoked=body.getBoolean("revoked")
                    if(revoked) { paused=true; rejectPending("POLICY_REVOKED") }
                    ack(value)
                }
                "result_ack" -> {
                    val commandId=body.getString("commandId")
                    require(sentResults[commandId]?.contains(body.getString("ackEventId"))==true && journal.command(commandId)?.result!=null) { "UNEXPECTED_RESULT_ACK" }
                    journal.acknowledge(commandId)
                }
                "transport_ack" -> Unit
            }
            events.changed()
        } catch (_: Exception) { fail("WIRE_OR_JOURNAL_REJECTED") }
    }

    private fun accept(body: JSONObject) {
        // Reject variant-forbidden content before persisting the command payload.
        if(!commandPolicy.permits(body)) { fail("VARIANT_FIXTURE_POLICY_DENIED"); return }
        val bind=binding ?: error("NO_SESSION")
        require(WireValidation.payloadDigest(body,bind.deviceId)==body.getString("payloadDigest")) { "DIGEST_MISMATCH" }
        val id=body.getString("commandId")
        if(!workBudget.command(permitEpoch,id)){fail("CLIENT_COMMAND_BUDGET_DENIED");return}
        val old=journal.command(id)
        if(old!=null) {
            require(old.digest==body.getString("payloadDigest") && old.attemptId==body.getString("attemptId")) { "COMMAND_CONFLICT" }
            old.result?.let { sendResult(JSONObject(it)) }
            // Still awaiting a fresh local confirmation: no second execution and no second prompt.
            return
        }
        require(journal.accept(body)) { "ACCEPT_CONFLICT" }
        link.send("accepted", JSONObject().put("commandId",id).put("attemptId",body.getString("attemptId")))
        val initialTtl=WireValidation.time(body,"expiresAt")-clock()
        // 30–300 seconds is the server creation TTL; queue/transit may leave less than 30.
        if(initialTtl<=0 || initialTtl>330000) { finish(body,"rejected","not_started","EXPIRED_OR_INVALID_TTL"); return }
        val reason=guard(body)
        if(reason!=null) { finish(body,"rejected","not_started",reason); return }
        if(body.getString("capability")=="file.export" || body.getBoolean("requiresLocalConfirmation")) {
            confirmations.add(id)
            val payload=body.getJSONObject("payload")
            events.confirmationRequired(id,payload.optString("exportId",""),payload.optString("scopeDigest",""))
            return
        }
        execute(body,emptyList())
    }

    /** The UI must supply explicit selected app-owned reference IDs; no path, URI or user file input. */
    @Synchronized fun confirmExport(commandId: String, allow: Boolean, referenceIds: List<String>) {
        try {
            val row=journal.command(commandId) ?: return
            if(row.result!=null || commandId !in confirmations) return
            confirmations.remove(commandId)
            val body=JSONObject(row.body)
            if(!allow) { finish(body,"rejected","not_started","LOCAL_CONFIRMATION_DENIED"); return }
            val reason=guard(body)
            if(reason!=null) { finish(body,"rejected","not_started",reason); return }
            if(body.getString("capability")=="file.export") {
                val bind=binding ?: error("NO_SESSION")
                if(!commandPolicy.permitsReferences(referenceIds) || referenceIds.isEmpty() || referenceIds.size>16 || referenceIds.distinct().size!=referenceIds.size ||
                    referenceIds.any { it !in fixtureContent } ||
                    WireValidation.scopeDigest(bind.deviceId,referenceIds)!=body.getJSONObject("payload").getString("scopeDigest")) {
                    finish(body,"rejected","not_started","EXPORT_SCOPE_MISMATCH"); return
                }
            } else if(referenceIds.isNotEmpty()) {
                finish(body,"rejected","not_started","UNEXPECTED_REFERENCE_IDS"); return
            }
            execute(body,referenceIds)
        } catch (_: Exception) { fail("CONFIRMATION_OR_JOURNAL_REJECTED") }
    }

    private fun guard(body: JSONObject): String? {
        if(!workBudget.valid(permitEpoch)) return "CLIENT_PERMIT_EXPIRED"
        if(!commandPolicy.permits(body)) return "VARIANT_FIXTURE_POLICY_DENIED"
        val bind=binding ?: return "OFFLINE"
        val now=clock()
        if(now<lastClock-30000) return "CLOCK_ROLLBACK"
        lastClock=maxOf(lastClock,now)
        if(!link.connected()) return "OFFLINE"
        if(paused) return "LOCAL_PAUSED"
        if(revoked || body.getLong("policyVersion")!=bind.policyVersion) return "POLICY_REVOKED"
        if(journal.command(body.getString("commandId"))?.stopped==true) return "STOP_REQUESTED"
        val remaining=WireValidation.time(body,"expiresAt")-now
        if(remaining<=0 || remaining>330000) return "EXPIRED_OR_INVALID_TTL"
        return null
    }
    private fun execute(body: JSONObject, references: List<String>) {
        val reason=guard(body)
        if(reason!=null) { finish(body,"rejected","not_started",reason); return }
        val id=body.getString("commandId")
        journal.markRunning(id)
        val startedEventId=link.send("started",JSONObject().put("commandId",id).put("attemptId",body.getString("attemptId")))
        if(startedEventId==null || !link.connected()) {
            // Local running intent is durable, but the effect has definitely not been applied.
            finish(body,"failed","not_applied","START_NOTIFICATION_NOT_SENT")
            return
        }
        // This is not an execution permit from the server: started/stop are asynchronous.
        // Only app-local SQLite effect/result commit is atomic; no claim of instant revoke.
        // Linearize admission before the existing local transaction, not a long lock around disk I/O.
        if(!workBudget.runIfValid(permitEpoch){}) {finish(body,"failed","not_applied","CLIENT_PERMIT_EXPIRED");return}
        val p=body.getJSONObject("payload")
        val effect=when(body.getString("capability")) {
            "message.send_text" -> LocalEffect("synthetic_inbox",p.getString("targetId"),p.getString("text"))
            "file.export" -> LocalEffect("synthetic_export",p.getString("exportId"),
                WireValidation.sha256(references.joinToString("\n") { fixtureContent.getValue(it) }))
            else -> error("CAPABILITY_UNAVAILABLE")
        }
        finish(body,"succeeded","applied","APP_LOCAL_SYNTHETIC_APPLIED",effect)
    }
    private fun finish(body: JSONObject,status: String,effectState: String,code: String,effect: LocalEffect?=null) {
        val id=body.getString("commandId")
        val result=JSONObject().put("commandId",id).put("attemptId",body.getString("attemptId"))
            .put("status",status).put("effectState",effectState).put("code",code)
            .put("evidenceRefs",JSONArray().put("journal_" + WireValidation.sha256(id).take(40)))
        journal.finish(id,result,effect)
        confirmations.remove(id)
        sendResult(result)
        events.changed()
    }
    private fun sendResult(result: JSONObject) {
        if(binding!=null && link.connected()) {
            val eventId=link.send("execution_result",result)
            if(eventId!=null) sentResults.getOrPut(result.getString("commandId")) { mutableSetOf() }.add(eventId)
        }
    }
    private fun ack(frame: JSONObject) { link.send("transport_ack",JSONObject().put("ackEventId",frame.getString("eventId"))) }
    private fun rejectPending(code: String) {
        journal.pendingCommands().filter { it.stage=="accepted" }.forEach { finish(JSONObject(it.body),"rejected","not_started",code) }
        confirmations.clear()
    }
    /** Authoritative execution gate for the UI; transport connection alone is not permission. */
    @Synchronized fun isPaused(): Boolean = paused
    @Synchronized fun pause() { paused=true; rejectPending("LOCAL_PAUSED"); events.changed() }
    @Synchronized fun resume() { if(binding!=null && link.connected() && !revoked) paused=false; events.changed() }
    @Synchronized override fun closed(reasonCode: String) { binding=null; paused=true; confirmations.clear(); sentResults.clear(); try {journal.recoverInterrupted();events.changed()} catch (_:Exception) {events.protocolError("CLOSED_JOURNAL_RECOVERY_FAILED");link.disconnect()} }
    private fun fail(code: String) { binding=null;paused=true;confirmations.clear();events.protocolError(code);link.disconnect() }
}
