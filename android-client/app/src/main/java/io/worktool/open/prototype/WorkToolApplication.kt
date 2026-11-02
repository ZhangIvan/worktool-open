package io.worktool.open.prototype

import android.app.Application
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import io.worktool.open.prototype.accessibility.FixtureServiceRegistry
import io.worktool.open.prototype.policy.ConnectionMode
import io.worktool.open.prototype.policy.ConnectionProfile
import io.worktool.open.prototype.policy.VariantFeatures
import java.util.concurrent.atomic.AtomicReference
import io.worktool.open.prototype.core.*
import io.worktool.open.prototype.transport.*
import io.worktool.open.prototype.ui.*
import io.worktool.open.prototype.policy.VariantFactory
import org.json.JSONObject
import java.util.concurrent.Executors

class WorkToolApplication : Application(), ClientUiBridgeProvider {
    override lateinit var clientUiBridge: ClientUiBridge
        private set
    override fun onCreate() { super.onCreate(); clientUiBridge = AppGraph(this) }
}

/** Network callbacks and ledger operations share one worker; UI snapshots never contain credentials. */
internal class AppGraph(app: Application) : ClientUiBridge, EngineEvents {
    private val serial = Executors.newSingleThreadExecutor { Thread(it, "worktool-client-ledger") }
    private val main = Handler(Looper.getMainLooper())
    private val listeners = linkedSetOf<ClientUiListener>() // main thread only
    private val vault = KeystoreCredentialVault(app)
    private val endpointPolicy = VariantFactory.endpointPolicy()
    private val budget = endpointPolicy.budget
    private val cleanup = Executors.newSingleThreadExecutor { Thread(it,"client-permit-cleanup") }
    private val link = OkHttpDeviceLink(endpointPolicy)
    private val context = app
    private var identity: Identity? = null
    private var journal: SqliteJournal? = null
    private var engine: NativeCommandEngine? = null
    private val prompts = linkedMapOf<String, ExportConfirmationUi>()
    private val fixturePrompts = linkedMapOf<String, FixtureConfirmationUi>()
    private val fixturePermit = AtomicReference<FixtureSessionPermit?>()
    private fun invalidateFixturePermit() { fixturePermit.get()?.invalidate() }
    private val stopCoordinator = ClientStopCoordinator()
    private var generation = 0L
    @Volatile private var active = false
    @Volatile private var wantsResume = false
    private var pairingInFlight = false
    private val pairingGate = io.worktool.open.prototype.policy.PairingAttemptGate()
    private val connectionIntent = io.worktool.open.prototype.policy.ConnectionIntentGate()
    @Volatile private var state = ClientUiSnapshot()
    init {
        budget.onInvalidated { epoch,_ ->
            if(budget.epoch()==epoch) {
                invalidateFixturePermit()
                connectionIntent.invalidate();pairingGate.invalidate();wantsResume=false
            }
            // Always close the old permit's I/O, even if a newer explicit confirmation already exists.
            // Never wait for the ledger queue or a socket monitor on the UI/expiry caller.
            cleanup.execute {
                link.cancelPermit(epoch)
                serial.execute {if(budget.epoch()==epoch)stop()}
            }
        }
        serial.execute {
        try { identity = vault.load(); state = state.copy(initialized=true, paired=identity!=null,
            identity=identity?.let { IdentityUiSummary(it.deviceId.takeLast(8),it.keyId.takeLast(8)) }, statusCode="LOCAL_READY") }
        catch (_: Exception) { state=state.copy(initialized=true,errorCode="CREDENTIAL_UNAVAILABLE_REPAIR_REQUIRED") }
        publish()
    } }
    override fun snapshot() = state
    override fun attach(listener: ClientUiListener) {
        check(Looper.myLooper()==Looper.getMainLooper()); listeners.add(listener); listener.onSnapshot(state)
        active=true
    }
    override fun detach(listener: ClientUiListener) {
        check(Looper.myLooper()==Looper.getMainLooper()); listeners.remove(listener)
        if(listeners.isEmpty()) { invalidateFixturePermit(); active=false; connectionIntent.invalidate(); pairingGate.invalidate(); wantsResume=false; endpointPolicy.clearConsent(); serial.execute { stop() } }
    }
    private fun confirmIntent(endpoint:String):Long? {
        if(!active)return null
        return try {
            endpointPolicy.clearConsent()
            endpointPolicy.confirm(endpoint) // Explicit UI confirmation starts the lease now, before enqueue.
            budget.epoch()
        } catch (_:Exception) { null }
    }
    private fun inbound(epoch:Long,work:()->Unit) {
        val slot=budget.callback(epoch) ?: return
        try { serial.execute {try {if(budget.valid(epoch))work()} finally {slot.close()}} }
        catch (_:Exception) {slot.close();cleanup.execute {link.cancelPermit(epoch)}}
    }
    override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String) {
        val epoch=confirmIntent(endpoint) ?: return
        val intent=connectionIntent.capture()
        val pairingAttempt=pairingGate.begin()
        serial.execute {
            if(!budget.valid(epoch)||!connectionIntent.current(intent)||!pairingGate.isCurrent(pairingAttempt,active))return@execute
            if(!active||state.busy||pairingInFlight||!state.initialized||identity!=null)return@execute
            pairingInFlight=true;state=state.copy(busy=true,errorCode=null,statusCode="PAIRING");publish()
            link.enroll(endpoint,enrollmentId,enrollmentToken) { result -> inbound(epoch) {
                try {
                    val newIdentity=result.getOrThrow()
                    var accepted=false
                    budget.runIfValid(epoch){accepted=connectionIntent.current(intent)&&pairingGate.consume(pairingAttempt,active)}
                    check(accepted){"PAIRING_CANCELLED"}
                    vault.save(newIdentity);identity=newIdentity
                    state=state.copy(paired=true,identity=IdentityUiSummary(newIdentity.deviceId.takeLast(8),newIdentity.keyId.takeLast(8)),statusCode="PAIRED_MANUAL_CONNECT_REQUIRED",errorCode=null)
                } catch (_:Exception) {state=state.copy(errorCode="PAIRING_FAILED_OR_CREDENTIAL_SAVE_FAILED",statusCode="PAIRING_REQUIRES_REVIEW")}
                pairingInFlight=false;state=state.copy(busy=false);publish()
            } }
        }
    }
    override fun resumeAndConnect(endpoint: String?, mode: ConnectionMode) {
        // Revocation is synchronous at the UI boundary, not delayed behind ledger/main queues.
        invalidateFixturePermit()
        if(!mode.allowed(VariantFeatures.ownFixtureAccessibility)) {
            serial.execute { stop(); state=state.copy(errorCode="VARIANT_MODE_DENIED");publish() };return
        }
        if(endpointPolicy.requiresExplicitConnectionOrigin && endpoint==null)return
        val selected=endpoint ?: identity?.endpoint ?: return
        val epoch=confirmIntent(selected) ?: return
        val intent=connectionIntent.capture()
        serial.execute {
        if(!budget.valid(epoch)||!connectionIntent.current(intent)) return@execute
        val id=identity ?: return@execute
        if(!active || state.busy) return@execute
        if(!stop(preserveConsent=true)) return@execute
        try {
            val confirmed=endpointPolicy.authorize(selected)
            require(confirmed==endpointPolicy.parse(id.endpoint)) { "PAIRED_ORIGIN_MISMATCH" }
        } catch (_: Exception) { endpointPolicy.clearConsent(); state=state.copy(errorCode="ORIGIN_CONFIRMATION_REQUIRED"); publish(); return@execute }
        if(!budget.valid(epoch) || !connectionIntent.current(intent) || !active) { endpointPolicy.clearConsent(); return@execute }
        val target=if(mode==ConnectionMode.OWN_FIXTURE_1_1) FixtureServiceRegistry.target(context) else null
        val profile=ConnectionProfile(mode,target)
        wantsResume=true; val current=++generation
        state=state.copy(busy=true,wsState=UiWsState.CONNECTING,statusCode="CONNECTING",errorCode=null,connectionMode=mode,negotiatedVersion=null); publish()
        val sessionFixturePermit=AtomicReference<FixtureSessionPermit?>()
        link.connect(id,object:LinkListener {
            override fun ready(binding:Binding) { inbound(epoch) {
                if(current!=generation || !active || !wantsResume) return@inbound
                try {
                    check(binding.deviceId==id.deviceId && binding.protocolVersion==mode.wireVersion)
                    journal?.close(); journal=SqliteJournal(context,binding.tenantId,binding.deviceId)
                    engine=null
                    if(!ConnectionProfile.canRecover(binding.protocolVersion,journal!!)) { protocolError("PENDING_FIXTURE_REQUIRES_1_1");return@inbound }
                    val permit=if(mode==ConnectionMode.OWN_FIXTURE_1_1) FixtureSessionPermit().also { fixturePermit.set(it);sessionFixturePermit.set(it) } else null
                    val driver=if(permit!=null) FixtureServiceRegistry.driver(permit) else null
                    engine=NativeCommandEngine(journal!!,link,this@AppGraph,commandPolicy=VariantFactory.commandPolicy(),workBudget=budget,permitEpoch=epoch,
                        fixtureDriver=driver,fixtureTarget=target,dispatchToSerial=if(permit==null)null else { work -> inbound(epoch){if(current==generation)work()} },elapsedClock=SystemClock::elapsedRealtime)
                    state=state.copy(busy=false,wsState=UiWsState.CONNECTED,paused=true,negotiatedVersion=binding.protocolVersion,statusCode=if(mode==ConnectionMode.OWN_FIXTURE_1_1)"CONNECTED_OWN_FIXTURE_CANDIDATE" else "CONNECTED_SYNTHETIC_ONLY")
                    engine!!.ready(binding); engine!!.resume(); changed()
                } catch (_: Exception) { protocolError("LOCAL_SESSION_INITIALIZATION_FAILED") }
            } }
            override fun frame(value:JSONObject) { inbound(epoch) { if(current==generation && active && wantsResume) engine?.frame(value) } }
            override fun closed(reasonCode:String) { sessionFixturePermit.get()?.invalidate(); inbound(epoch) {
                if(current!=generation) return@inbound
                wantsResume=false; endpointPolicy.clearConsent(); prompts.clear();fixturePrompts.clear(); engine?.closed(reasonCode)
                state=state.copy(busy=false,paused=true,wsState=UiWsState.DISCONNECTED,confirmation=null,fixtureConfirmation=null,fixtureProbe=null,negotiatedVersion=null,errorCode=if(mode==ConnectionMode.OWN_FIXTURE_1_1)"FIXTURE_SESSION_UNAVAILABLE_OR_UNAUTHORIZED" else state.errorCode,statusCode="DISCONNECTED_MANUAL_RESUME_REQUIRED")
                publish()
            } }
        },profile)
    } }
    override fun pauseAndDisconnect() { invalidateFixturePermit(); connectionIntent.invalidate(); pairingGate.invalidate(); wantsResume=false; endpointPolicy.clearConsent(); serial.execute { stop() } }
    private fun stop(preserveConsent:Boolean=false):Boolean {
        invalidateFixturePermit()
        generation++; pairingGate.invalidate(); wantsResume=false; prompts.clear();fixturePrompts.clear()
        return stopCoordinator.stop(
            preserveConsent=preserveConsent,
            pause={engine?.pause()},
            disconnect={link.disconnect()},
            clearConsent={endpointPolicy.clearConsent()},
            closed={engine?.closed("LOCAL_PAUSE")},
            finishSnapshot={result ->
                pairingInFlight=false; wantsResume=false; prompts.clear();fixturePrompts.clear()
                if(!result.succeeded) connectionIntent.invalidate()
                state=result.snapshot(state).copy(fixtureConfirmation=null,fixtureProbe=null,negotiatedVersion=null)
                publish()
            }
        ).succeeded
    }
    override fun decideExport(commandId:String,scopeDigest:String,referenceId:String?,approved:Boolean) { serial.execute {
        val prompt=state.confirmation ?: return@execute
        if(!active || state.paused || !link.connected() || prompt.commandId!=commandId || prompt.scopeDigest!=scopeDigest) return@execute
        if(approved && referenceId !in prompt.allowedReferenceIds) return@execute
        prompts.remove(commandId); state=state.copy(confirmation=null)
        engine?.confirmExport(commandId,approved,if(approved && referenceId!=null) listOf(referenceId) else emptyList())
        changed()
    } }
    override fun inspectFixture() { serial.execute {
        if(!active || !VariantFeatures.ownFixtureAccessibility) return@execute
        val current=generation
        FixtureServiceRegistry.inspect(FixtureServiceRegistry.target(context),SystemClock.elapsedRealtime()+5000) { inspected -> serial.execute {
            if(!active || current!=generation) return@execute
            state=state.copy(fixtureProbe=FixtureProbeUi(inspected.supported,inspected.reasonCode,
                inspected.target.targetPackage,inspected.target.targetActivity,inspected.windowId.takeIf { it>=0 },SystemClock.elapsedRealtime()))
            publish()
        } }
    } }
    override fun decideFixture(commandId:String,attemptId:String,payloadDigest:String,approved:Boolean) { serial.execute {
        val prompt=state.fixtureConfirmation ?: return@execute
        if(!VariantFeatures.ownFixtureAccessibility || state.connectionMode!=ConnectionMode.OWN_FIXTURE_1_1 || fixturePermit.get()?.isValid()!=true || !active || state.paused || !link.connected() || prompt.commandId!=commandId ||
            prompt.attemptId!=attemptId || prompt.payloadDigest!=payloadDigest) return@execute
        fixturePrompts.remove(commandId);state=state.copy(fixtureConfirmation=null)
        engine?.confirmFixture(commandId,attemptId,payloadDigest,approved)
        changed()
    } }
    override fun fixtureConfirmationRequired(request:FixtureApprovalRequest) {
        fixturePrompts[request.commandId]=FixtureConfirmationUi(request.commandId,request.attemptId,request.payloadDigest,
            request.target.targetPackage,request.target.targetActivity,request.text)
        state=state.copy(fixtureConfirmation=fixturePrompts.values.firstOrNull());publish()
    }
    override fun changed() {
        try {
            val rows=journal?.recent().orEmpty()
            val pendingIds=journal?.pendingCommands().orEmpty().map { it.id }.toSet()
            prompts.keys.retainAll(pendingIds)
            fixturePrompts.keys.retainAll(pendingIds)
            state=state.copy(paused=engine?.isPaused() ?: true,ledger=rows.map { LedgerUiRow(it.id,it.stage,it.result!=null,it.acked) },
                inbox=journal?.effectRows().orEmpty().mapNotNull { raw -> val j=JSONObject(raw); if(j.getString("kind")=="synthetic_inbox") SyntheticInboxUiRow(j.getString("referenceId"),j.getString("value")) else null },
                confirmation=prompts.values.firstOrNull(),fixtureConfirmation=fixturePrompts.values.firstOrNull())
        } catch (_: Exception) { stopCoordinator.reportFailure(); state=state.copy(errorCode="LOCAL_LEDGER_READ_FAILED") }
        publish()
    }
    override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String) {
        val id=identity?.deviceId ?: return
        val references=SyntheticUiReferences.allowed.filter { WireValidation.scopeDigest(id,listOf(it))==scopeDigest }
        prompts[commandId]=ExportConfirmationUi(commandId,exportId,scopeDigest,references)
        state=state.copy(confirmation=prompts.values.firstOrNull(),fixtureConfirmation=fixturePrompts.values.firstOrNull()); publish()
    }
    override fun protocolError(code:String) {
        invalidateFixturePermit()
        if(stopCoordinator.reportFailure()) return // stop owns all remaining cleanup and its fixed failure snapshot
        generation++; pairingGate.invalidate(); wantsResume=false; prompts.clear();fixturePrompts.clear(); link.disconnect(); endpointPolicy.clearConsent()
        state=state.copy(busy=false,paused=true,wsState=UiWsState.ERROR,confirmation=null,fixtureConfirmation=null,fixtureProbe=null,negotiatedVersion=null,errorCode=if(code=="PENDING_FIXTURE_REQUIRES_1_1")code else "PROTOCOL_OR_LOCAL_STORAGE_REJECTED",statusCode="MANUAL_REVIEW_REQUIRED"); publish()
    }
    private fun publish() { val copy=state; main.post { listeners.toList().forEach { it.onSnapshot(copy) } } }
}
