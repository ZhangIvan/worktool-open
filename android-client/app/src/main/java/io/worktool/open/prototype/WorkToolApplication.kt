package io.worktool.open.prototype

import android.app.Application
import android.os.Handler
import android.os.Looper
import io.worktool.open.prototype.core.*
import io.worktool.open.prototype.transport.*
import io.worktool.open.prototype.ui.*
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
    private val link = OkHttpDeviceLink()
    private val context = app
    private var identity: Identity? = null
    private var journal: SqliteJournal? = null
    private var engine: NativeCommandEngine? = null
    private val prompts = linkedMapOf<String, ExportConfirmationUi>()
    private var generation = 0L
    @Volatile private var active = false
    @Volatile private var wantsResume = false
    private var pairingInFlight = false
    @Volatile private var state = ClientUiSnapshot()
    init { serial.execute {
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
        if(listeners.isEmpty()) { active=false; wantsResume=false; serial.execute { stop() } }
    }
    override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String) { serial.execute {
        if(!active || state.busy || pairingInFlight || !state.initialized || identity!=null) return@execute
        pairingInFlight=true; state=state.copy(busy=true,errorCode=null,statusCode="PAIRING"); publish()
        link.enroll(endpoint,enrollmentId,enrollmentToken) { result -> serial.execute {
            try {
                val newIdentity=result.getOrThrow(); vault.save(newIdentity); identity=newIdentity
                state=state.copy(paired=true,identity=IdentityUiSummary(newIdentity.deviceId.takeLast(8),newIdentity.keyId.takeLast(8)),statusCode="PAIRED_MANUAL_CONNECT_REQUIRED",errorCode=null)
            } catch (_: Exception) { state=state.copy(errorCode="PAIRING_FAILED_OR_CREDENTIAL_SAVE_FAILED",statusCode="PAIRING_REQUIRES_REVIEW") }
            pairingInFlight=false; state=state.copy(busy=false); publish()
        } }
    } }
    override fun resumeAndConnect() { serial.execute {
        val id=identity ?: return@execute
        if(!active || state.busy) return@execute
        stop(); wantsResume=true; val current=++generation
        state=state.copy(busy=true,wsState=UiWsState.CONNECTING,statusCode="CONNECTING",errorCode=null); publish()
        link.connect(id,object:LinkListener {
            override fun ready(binding:Binding) { serial.execute {
                if(current!=generation || !active || !wantsResume) return@execute
                try {
                    check(binding.deviceId==id.deviceId)
                    journal?.close(); journal=SqliteJournal(context,binding.tenantId,binding.deviceId)
                    engine=NativeCommandEngine(journal!!,link,this@AppGraph)
                    state=state.copy(busy=false,wsState=UiWsState.CONNECTED,paused=false,statusCode="CONNECTED_SYNTHETIC_ONLY")
                    engine!!.ready(binding); engine!!.resume(); changed()
                } catch (_: Exception) { protocolError("LOCAL_SESSION_INITIALIZATION_FAILED") }
            } }
            override fun frame(value:JSONObject) { serial.execute { if(current==generation && active && wantsResume) engine?.frame(value) } }
            override fun closed(reasonCode:String) { serial.execute {
                if(current!=generation) return@execute
                wantsResume=false; prompts.clear(); engine?.closed(reasonCode)
                state=state.copy(busy=false,paused=true,wsState=UiWsState.DISCONNECTED,confirmation=null,statusCode="DISCONNECTED_MANUAL_RESUME_REQUIRED")
                publish()
            } }
        })
    } }
    override fun pauseAndDisconnect() { wantsResume=false; serial.execute { stop() } }
    private fun stop() {
        generation++; wantsResume=false; prompts.clear()
        engine?.pause(); link.disconnect(); engine?.closed("LOCAL_PAUSE")
        state=state.copy(busy=pairingInFlight,paused=true,wsState=UiWsState.DISCONNECTED,confirmation=null,statusCode="LOCAL_PAUSED"); publish()
    }
    override fun decideExport(commandId:String,scopeDigest:String,referenceId:String?,approved:Boolean) { serial.execute {
        val prompt=state.confirmation ?: return@execute
        if(!active || state.paused || !link.connected() || prompt.commandId!=commandId || prompt.scopeDigest!=scopeDigest) return@execute
        if(approved && referenceId !in prompt.allowedReferenceIds) return@execute
        prompts.remove(commandId); state=state.copy(confirmation=null)
        engine?.confirmExport(commandId,approved,if(approved && referenceId!=null) listOf(referenceId) else emptyList())
        changed()
    } }
    override fun changed() {
        try {
            val rows=journal?.recent().orEmpty()
            val pendingIds=journal?.pendingCommands().orEmpty().map { it.id }.toSet()
            prompts.keys.retainAll(pendingIds)
            state=state.copy(ledger=rows.map { LedgerUiRow(it.id,it.stage,it.result!=null,it.acked) },
                inbox=journal?.effectRows().orEmpty().mapNotNull { raw -> val j=JSONObject(raw); if(j.getString("kind")=="synthetic_inbox") SyntheticInboxUiRow(j.getString("referenceId"),j.getString("value")) else null },
                confirmation=prompts.values.firstOrNull())
        } catch (_: Exception) { state=state.copy(errorCode="LOCAL_LEDGER_READ_FAILED") }
        publish()
    }
    override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String) {
        val id=identity?.deviceId ?: return
        val references=SyntheticUiReferences.allowed.filter { WireValidation.scopeDigest(id,listOf(it))==scopeDigest }
        prompts[commandId]=ExportConfirmationUi(commandId,exportId,scopeDigest,references)
        state=state.copy(confirmation=prompts.values.firstOrNull()); publish()
    }
    override fun protocolError(code:String) {
        generation++; wantsResume=false; prompts.clear(); link.disconnect()
        state=state.copy(busy=false,paused=true,wsState=UiWsState.ERROR,confirmation=null,errorCode="PROTOCOL_OR_LOCAL_STORAGE_REJECTED",statusCode="MANUAL_REVIEW_REQUIRED"); publish()
    }
    private fun publish() { val copy=state; main.post { listeners.toList().forEach { it.onSnapshot(copy) } } }
}
