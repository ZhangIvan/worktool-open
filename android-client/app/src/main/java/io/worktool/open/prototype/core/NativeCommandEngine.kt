package io.worktool.open.prototype.core

import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID
import io.worktool.open.prototype.policy.ConnectionProfile
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
    private val fixtureDriver: FixtureDriver? = null,
    private val fixtureTarget: FixtureTarget? = null,
    private val dispatchToSerial: ((() -> Unit) -> Unit)? = null,
    private val elapsedClock: () -> Long = { System.nanoTime()/1_000_000 },
    private val clock: () -> Long = System::currentTimeMillis
) : LinkListener {
    private var binding: Binding? = null
    private var incoming = 0L
    private var paused = true // explicit local resume; pairing does not consent to execution
    private var revoked = false
    private var lastClock = 0L
    private val confirmations = mutableSetOf<String>()
    private val fixtureConfirmations = mutableSetOf<String>()
    private val fixtureInFlight = mutableSetOf<String>()
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
        if(!ConnectionProfile.canRecover(binding.protocolVersion,journal)) { fail("PENDING_FIXTURE_REQUIRES_1_1"); return }
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
            require(value.getString("version")==bind.protocolVersion) { "SESSION_VERSION_MISMATCH" }
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
                    fixtureDriver?.cancel(body.getString("commandId"),if(revoked) "POLICY_REVOKED" else "STOP_REQUESTED")
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
                    if(revoked) { paused=true; cancelFixtures("POLICY_REVOKED"); rejectPending("POLICY_REVOKED") }
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
        if(body.getString("capability")=="fixture.ui.submit_text") {
            val payload=body.getJSONObject("payload")
            if(bind.protocolVersion!="1.1" || fixtureDriver==null || dispatchToSerial==null || fixtureTarget==null ||
                FixtureTarget(payload.getString("targetPackage"),payload.getString("targetActivity"))!=fixtureTarget) {
                fail("FIXTURE_DRIVER_OR_TARGET_UNAVAILABLE");return
            }
        }
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
        if(body.getString("capability")=="fixture.ui.submit_text") {
            if(bind.protocolVersion!="1.1"||fixtureDriver==null||dispatchToSerial==null){finish(body,"rejected","not_started","FIXTURE_DRIVER_UNAVAILABLE");return}
            val p=body.getJSONObject("payload")
            fixtureConfirmations.add(id)
            events.fixtureConfirmationRequired(FixtureApprovalRequest(id,body.getString("attemptId"),body.getString("payloadDigest"),FixtureTarget(p.getString("targetPackage"),p.getString("targetActivity")),p.getString("text"),WireValidation.sha256(p.getString("text"))))
            return
        }
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

    fun fixtureStatus():FixtureDriverStatus = fixtureDriver?.status() ?: FixtureDriverStatus(false,false,false,0,0,"DRIVER_UNAVAILABLE")
    @Synchronized fun confirmFixture(commandId:String,attemptId:String,payloadDigest:String,allow:Boolean) {
        try {
        val row=journal.command(commandId) ?: return
        if(row.result!=null||commandId !in fixtureConfirmations||row.attemptId!=attemptId||row.digest!=payloadDigest)return
        fixtureConfirmations.remove(commandId)
        val body=JSONObject(row.body)
        if(!allow){finish(body,"rejected","not_started","LOCAL_FIXTURE_CONFIRMATION_DENIED");return}
        val reason=guard(body)
        if(reason!=null){finish(body,"rejected","not_started",reason);return}
        val driver=fixtureDriver ?: return
        val dispatch=dispatchToSerial ?: return
        val p=body.getJSONObject("payload")
        val target=FixtureTarget(p.getString("targetPackage"),p.getString("targetActivity"))
        val deadline=elapsedClock()+minOf(5000L,WireValidation.time(body,"expiresAt")-clock())
        fixtureInFlight.add(commandId)
        driver.inspect(target,deadline) { inspected -> dispatch {
            synchronized(this) {
                try {
                val latest=journal.command(commandId)
                if(latest?.result!=null||commandId !in fixtureInFlight)return@synchronized
                val invalid=guard(body)
                if(invalid!=null||!inspected.supported||inspected.target!=target||elapsedClock()>=deadline){finish(body,"rejected","not_started",invalid?:"FIXTURE_INSPECT_FAILED");return@synchronized}
                val ticket=FixtureTicket("ticket_"+UUID.randomUUID().toString().replace("-",""),commandId,attemptId,payloadDigest,WireValidation.sha256(p.getString("text")),target,inspected.windowId,inspected.serviceEpoch,inspected.foregroundEpoch,deadline)
                journal.markRunning(commandId)
                val sent=link.send("started",JSONObject().put("commandId",commandId).put("attemptId",attemptId))
                if(sent==null||!link.connected()){finish(body,"failed","not_applied","START_NOTIFICATION_NOT_SENT");return@synchronized}
                driver.execute(ticket,p.getString("text")) { outcome -> dispatch {
                    synchronized(this) {
                        // Interrupted/recovered unknown is immutable; late receipt requires explicit evidence review.
                        if(journal.command(commandId)?.result!=null)return@synchronized
                        try { finishDriver(body,outcome) } catch (_:Exception) { fail("FIXTURE_RESULT_REJECTED") }
                    }
                } }
                } catch (_:Exception) { fail("FIXTURE_CALLBACK_REJECTED") }
            }
        } }
        } catch (_:Exception) { fail("FIXTURE_OR_JOURNAL_REJECTED") }
    }
    private fun finishDriver(body:JSONObject,outcome:FixtureDriverResult) {
        if(outcome.status=="succeeded" && (outcome.code!="FIXTURE_RECEIPT_AND_POSTCONDITION" || outcome.evidenceRefs.none{it.startsWith("receipt_")})) {
            finish(body,"execution_unknown","unknown","DRIVER_PROOF_INVALID");return
        }
        val permitted=mapOf("succeeded" to setOf("applied"),"partially_succeeded" to setOf("partially_applied"),"failed" to setOf("not_applied","not_started"),"cancelled" to setOf("not_applied","not_started"),"execution_unknown" to setOf("unknown"))
        require(outcome.effectState in (permitted[outcome.status]?:emptySet()))
        val id=body.getString("commandId")
        val refs=(listOf("journal_"+WireValidation.sha256(id).take(40))+outcome.evidenceRefs).distinct().take(16)
        require(refs.all{Regex("[a-z][a-z0-9_-]{2,63}").matches(it)})
        val result=JSONObject().put("commandId",id).put("attemptId",body.getString("attemptId")).put("status",outcome.status).put("effectState",outcome.effectState).put("code",outcome.code.take(128)).put("evidenceRefs",JSONArray(refs))
        journal.finish(id,result,null);fixtureInFlight.remove(id);sendResult(result);events.changed()
    }
    private fun cancelFixtures(reason:String) {
        var failed=false
        fixtureInFlight.toList().forEach { try {fixtureDriver?.cancel(it,reason)} catch (_:Exception) {failed=true} }
        check(!failed) { "FIXTURE_CANCEL_UNCONFIRMED" }
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
        confirmations.remove(id); fixtureConfirmations.remove(id); fixtureInFlight.remove(id)
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
        confirmations.clear();fixtureConfirmations.clear()
    }
    /** Authoritative execution gate for the UI; transport connection alone is not permission. */
    @Synchronized fun isPaused(): Boolean = paused
    @Synchronized fun pause() { paused=true; cancelFixtures("LOCAL_PAUSE"); rejectPending("LOCAL_PAUSED"); events.changed() }
    @Synchronized fun resume() { if(binding!=null && link.connected() && !revoked) paused=false; events.changed() }
    @Synchronized override fun closed(reasonCode: String) {
        val cancelFailed=runCatching {cancelFixtures("CONNECTION_CLOSED")}.isFailure
        fixtureConfirmations.clear();fixtureInFlight.clear();binding=null;paused=true;confirmations.clear();sentResults.clear()
        try {
            journal.recoverInterrupted();events.changed()
            if(cancelFailed)events.protocolError("FIXTURE_CANCEL_UNCONFIRMED")
        } catch (_:Exception) {events.protocolError("CLOSED_JOURNAL_RECOVERY_FAILED");link.disconnect()}
    }
    private fun fail(code: String) { runCatching {cancelFixtures("PROTOCOL_ERROR")};fixtureConfirmations.clear();fixtureInFlight.clear(); binding=null;paused=true;confirmations.clear();events.protocolError(code);link.disconnect() }
}
