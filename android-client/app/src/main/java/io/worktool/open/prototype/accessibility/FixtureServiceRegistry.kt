package io.worktool.open.prototype.accessibility

import android.accessibilityservice.AccessibilityService
import android.app.Activity
import android.content.ComponentName
import android.content.Context
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import io.worktool.open.prototype.R
import io.worktool.open.prototype.policy.VariantFeatures
import io.worktool.open.prototype.core.*
import io.worktool.open.prototype.ui.FixtureActivity
import java.lang.ref.WeakReference
import java.util.concurrent.Executors

/** Real, main-thread-only own-window driver. No external app selectors, gestures or screen capture.
 * Candidate: IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED. Network engine callbacks must marshal to serial.
 */
object FixtureServiceRegistry {
    private val main=Handler(Looper.getMainLooper())
    private val storage=Executors.newSingleThreadExecutor { Thread(it,"fixture-receipts") }
    private var context:Context?=null
    private var store:FixtureReceiptStore?=null
    private var service:AccessibilityService?=null
    private var foreground=WeakReference<Activity>(null)
    private var serviceEpoch=0L
    private var foregroundEpoch=0L
    private var enabled=false
    private var eventWindow=-1
    private var remoteGuardEpoch=-1L
    @Volatile private var snapshot=FixtureDriverStatus(false,false,false,0,0,"SERVICE_UNAVAILABLE")
    private class Operation(val ticket:FixtureTicket,val text:String,val permit:FixtureOperationPermit,val done:(FixtureDriverResult)->Unit) {
        var inputRequested=false;var inputChanged=false;var inputVerified=false;var beforeTextDigest:String?=null;var clickRequested=false;var buttonClaimed=false;@Volatile var ended=false
    }
    private var active:Operation?=null
    private val sessionPermit=java.util.concurrent.atomic.AtomicReference<FixtureSessionPermit?>()
    fun target(context:Context)=FixtureTarget(context.packageName,ComponentName(context,FixtureActivity::class.java).className)
    private fun mainOnly(){check(Looper.myLooper()==Looper.getMainLooper())}
    private fun initialize(c:Context){check(VariantFeatures.ownFixtureAccessibility);if(context==null){context=c.applicationContext;storage.execute { store=FixtureReceiptStore(c.applicationContext);store!!.markInterruptedUnknown() }}}
    private fun publish(reason:String){snapshot=FixtureDriverStatus(service!=null,enabled,foreground.get()!=null,serviceEpoch,foregroundEpoch,reason)}
    fun status():FixtureDriverStatus=snapshot
    fun foregroundEntered(activity:Activity){mainOnly();if(!VariantFeatures.ownFixtureAccessibility)return;initialize(activity);invalidate("FOREGROUND_CHANGED");foregroundEpoch++;enabled=false;eventWindow=-1;remoteGuardEpoch=-1;foreground=WeakReference<Activity>(if(activity.componentName==ComponentName(activity,FixtureActivity::class.java))activity else null);publish("MANUAL_ENABLE_REQUIRED")}
    fun foregroundLeft(activity:Activity){mainOnly();if(foreground.get()===activity){invalidate("FOREGROUND_LEFT");foregroundEpoch++;foreground.clear();enabled=false;eventWindow=-1;publish("FOREGROUND_REQUIRED")}}
    fun setLocalEnabled(value:Boolean){mainOnly();if(!VariantFeatures.ownFixtureAccessibility)return;if(!value)invalidate("LOCAL_PERMISSION_REVOKED");enabled=value&&service!=null&&foreground.get()!=null;publish(if(enabled)"LOCAL_ENABLED" else "LOCAL_DISABLED")}
    fun disableService(){mainOnly();setLocalEnabled(false);service?.disableSelf()}
    internal fun connected(host:AccessibilityService){mainOnly();if(!VariantFeatures.ownFixtureAccessibility){host.disableSelf();return};initialize(host);invalidate("SERVICE_RECONNECTED");service=host;serviceEpoch++;enabled=false;eventWindow=-1;publish("MANUAL_ENABLE_REQUIRED")}
    internal fun disconnected(host:AccessibilityService,reason:String){mainOnly();if(service===host){invalidate(reason);service=null;serviceEpoch++;enabled=false;eventWindow=-1;publish(reason)}}
    internal fun interrupted(host:AccessibilityService){mainOnly();if(service===host){invalidate("SERVICE_INTERRUPTED");serviceEpoch++;enabled=false;eventWindow=-1;publish("SERVICE_INTERRUPTED")}}
    internal fun event(host:AccessibilityService,event:AccessibilityEvent?){
        mainOnly();if(service!==host||event==null)return
        val own=context?.packageName ?: return
        if(event.packageName?.toString()!=own){eventWindow=-1;invalidateOperation("OTHER_WINDOW");return}
        if(event.eventType==AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED){
            val expected=context?.let{target(it).targetActivity}
            eventWindow=if(event.className?.toString()==expected)event.windowId else -1
            if(eventWindow<0)invalidateOperation("WINDOW_CHANGED")
        }
    }
    fun driver(permit:FixtureSessionPermit):FixtureDriver {
        check(VariantFeatures.ownFixtureAccessibility) { "VARIANT_DRIVER_DENIED" }
        sessionPermit.getAndSet(permit)?.takeIf { it !== permit }?.invalidate()
        return Driver(permit)
    }
    /** Observation does not install an execution session or create/consume a ticket. */
    fun inspect(target:FixtureTarget,deadlineElapsedMs:Long,done:(FixtureInspection)->Unit) {
        if(!VariantFeatures.ownFixtureAccessibility){done(FixtureInspection(false,"VARIANT_DRIVER_DENIED",target));return}
        Driver(FixtureSessionPermit()).inspect(target,deadlineElapsedMs,done)
    }
    private fun valid(t:FixtureTicket):Boolean {
        val a=foreground.get() ?: return false
        return FixtureTicketGate.permits(t,FixtureDriverStatus(service!=null,enabled,true,serviceEpoch,foregroundEpoch,"CHECK"),target(a),eventWindow,SystemClock.elapsedRealtime())&&
            a.componentName==ComponentName(a,FixtureActivity::class.java)&&!a.isFinishing&&!a.isDestroyed
    }
    @Suppress("DEPRECATION")
    private fun <T> nodes(target:FixtureTarget,window:Int?=null,action:(AccessibilityNodeInfo,AccessibilityNodeInfo,AccessibilityNodeInfo,Int)->T):T {
        mainOnly();val a=foreground.get() ?: error("FOREGROUND_REQUIRED")
        check(enabled && target==FixtureServiceRegistry.target(a) && a.componentName==ComponentName(a,FixtureActivity::class.java)) { "SCOPE_OR_LOCAL_PERMISSION" }
        val root=service?.rootInActiveWindow ?: error("ROOT_UNAVAILABLE")
        try {
            // This must precede findAccessibilityNodeInfosByViewId; never traverse a foreign root.
            check(root.packageName?.toString()==a.packageName) { "ROOT_PACKAGE_MISMATCH" }
            val ownWindowNode=a.window.decorView.createAccessibilityNodeInfo()
            val ownWindow=try { ownWindowNode.windowId } finally { ownWindowNode.recycle() }
            check(root.windowId==eventWindow&&root.windowId==ownWindow&&(window==null||window==root.windowId)) { "ROOT_SCOPE_MISMATCH" }
            val ids=listOf(R.id.ui_fixture_input,R.id.ui_fixture_send,R.id.ui_fixture_output).map{a.resources.getResourceName(it)}
            val all=ids.map{root.findAccessibilityNodeInfosByViewId(it)}
            try {
                check(all.all{it.size==1}) { "AMBIGUOUS_OR_MISSING_NODE" }
                val n=all.map{it.single()}
                n.forEachIndexed{index,node->check(node.packageName?.toString()==a.packageName&&node.windowId==root.windowId&&node.viewIdResourceName==ids[index]&&node.isVisibleToUser&&node.isEnabled){"NODE_SCOPE_MISMATCH"}}
                check(n[0].isEditable&&n[1].isClickable){"UNSUPPORTED_NODE"}
                return action(n[0],n[1],n[2],root.windowId)
            } finally {all.flatten().forEach{it.recycle()}}
        } finally {root.recycle()}
    }
    private class Driver(private val sessionPermit:FixtureSessionPermit):FixtureDriver {
        override fun status()=snapshot
        override fun inspect(target:FixtureTarget,deadlineElapsedMs:Long,done:(FixtureInspection)->Unit){
            // Dialog dismissal may temporarily own the root. Re-observe only; never reuse its nodes.
            val boundedDeadline=minOf(deadlineElapsedMs,SystemClock.elapsedRealtime()+5000)
            fun attempt(){
                val result=try{check(sessionPermit.isValid()&&SystemClock.elapsedRealtime()<boundedDeadline);nodes(target){_,_,_,window->FixtureInspection(true,"SUPPORTED",target,window,serviceEpoch,foregroundEpoch)}}catch(_:Exception){FixtureInspection(false,"TARGET_UNAVAILABLE",target)}
                if(result.supported||SystemClock.elapsedRealtime()>=boundedDeadline||!enabled||!sessionPermit.isValid()||foreground.get()==null)done(result)
                else main.postDelayed({attempt()},50)
            }
            main.post {attempt()}
        }
        override fun execute(ticket:FixtureTicket,text:String,done:(FixtureDriverResult)->Unit){
            val operationPermit=sessionPermit.operation(ticket.commandId)
            main.post {
            if(active!=null||!operationPermit.isValid()||!valid(ticket)||FixtureReceiptStore.hash(text)!=ticket.textDigest){done(FixtureDriverResult("failed","not_applied","TICKET_INVALID",emptyList()));return@post}
            val op=Operation(ticket,text,operationPermit,done);active=op;remoteGuardEpoch=foregroundEpoch
            storage.execute {
                val consumed=runCatching{store!!.consume(ticket)}
                main.post {
                    if(op.ended)return@post
                    if(consumed.getOrNull()!=true){end(op,"execution_unknown","unknown","TICKET_ALREADY_CONSUMED_OR_STORAGE_FAILED");return@post}
                    if(!op.permit.isValid()||!valid(ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return@post}
                    scheduleTimeout(op); setInput(op)
                }
            }
        }}
        override fun cancel(commandId:String,reasonCode:String){
            sessionPermit.cancel(commandId) // synchronous tombstone before main.post; enqueue is not completion
            main.post {active?.takeIf{it.ticket.commandId==commandId&&it.permit===sessionPermit.operation(commandId)}?.let{op->
            if(reasonCode in setOf("STOP_REQUESTED","OPERATOR_CANCEL")) settleKnown(op,"CANCELLED")
            else end(op,"execution_unknown","unknown","LIFECYCLE_OR_PERMISSION_INVALIDATED")
        }}}
    }
    private fun scheduleTimeout(op:Operation){main.postDelayed({if(!op.ended)settleKnown(op,"DRIVER_TIMEOUT")},(op.ticket.deadlineElapsedMs-SystemClock.elapsedRealtime()).coerceAtLeast(1))}
    private fun persistPhase(op:Operation,phase:String,next:()->Unit){storage.execute {
        val result=runCatching{store!!.phase(op.ticket.ticketId,phase)}
        main.post {if(!op.ended){if(result.isFailure)end(op,"execution_unknown","unknown","STEP_STORAGE_FAILED")else next()}}
    }}
    private fun setInput(op:Operation){
        if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return}
        persistPhase(op,"input_requested") {
            if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return@persistPhase}
            try {
                op.inputRequested=true
                nodes(op.ticket.target,op.ticket.windowId){input,_,_,_->check(op.permit.isValid()&&valid(op.ticket));op.beforeTextDigest=FixtureReceiptStore.hash(input.text?.toString().orEmpty());input.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,Bundle().apply{putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,op.text)})}
                main.post {verifyInput(op)}
            } catch(_:Exception){end(op,"execution_unknown","unknown","INPUT_ACTION_UNCERTAIN")}
        }
    }
    private fun verifyInput(op:Operation){
        if(op.ended)return
        if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return}
        try {
            val matches=nodes(op.ticket.target,op.ticket.windowId){input,_,_,_->input.text?.toString()==op.text}
            if(!matches){main.postDelayed({verifyInput(op)},50);return}
            op.inputVerified=true;op.inputChanged=op.beforeTextDigest!=op.ticket.textDigest
            persistPhase(op,if(op.inputChanged)"input_changed" else "input_verified"){click(op)}
        } catch(_:Exception){end(op,"execution_unknown","unknown","INPUT_POSTCONDITION_UNCERTAIN")}
    }
    private fun click(op:Operation){
        if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return}
        persistPhase(op,"click_requested") {
            if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","TICKET_INVALIDATED");return@persistPhase}
            try {
                nodes(op.ticket.target,op.ticket.windowId){input,button,_,_->
                    check(input.text?.toString()==op.text){"INPUT_CHANGED"}
                    check(op.permit.isValid()&&valid(op.ticket)){"PERMIT_INVALIDATED"}
                    op.clickRequested=true
                    button.performAction(AccessibilityNodeInfo.ACTION_CLICK) // Boolean is never execution success.
                }
                main.postDelayed({pollReceipt(op)},50)
            } catch(_:Exception){settleKnown(op,"CLICK_OR_INPUT_CHANGED")}
        }
    }
    private fun pollReceipt(op:Operation){
        if(op.ended)return
        if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","WINDOW_OR_PERMISSION_LOST");return}
        storage.execute {
            val read=runCatching{store!!.receipt(op.ticket.ticketId)}
            main.post {
                if(op.ended)return@post
                if(read.isFailure){end(op,"execution_unknown","unknown","RECEIPT_READ_FAILED");return@post}
                val r=read.getOrNull()
                if(r==null){main.postDelayed({pollReceipt(op)},50);return@post}
                if(!op.permit.isValid()||!valid(op.ticket)){end(op,"execution_unknown","unknown","RECEIPT_WINDOW_LOST");return@post}
                val verified=runCatching {r.commandId==op.ticket.commandId&&r.attemptId==op.ticket.attemptId&&r.payloadDigest==op.ticket.payloadDigest&&r.textDigest==op.ticket.textDigest&&nodes(op.ticket.target,op.ticket.windowId){_,_,output,_->output.text?.toString()==r.renderText}}.getOrDefault(false)
                if(verified)end(op,"succeeded","applied","FIXTURE_RECEIPT_AND_POSTCONDITION",listOf(r.receiptId))
                else main.postDelayed({pollReceipt(op)},50)
            }
        }
    }
    /** Real Fixture button callback: worker transaction writes effect+receipt before UI render. */
    fun buttonClick(activity:Activity,text:String,done:(FixtureButtonResult)->Unit){
        mainOnly();val op=active
        if(op==null){done(FixtureButtonResult(if(remoteGuardEpoch==foregroundEpoch)FixtureButtonOutcome.REJECTED else FixtureButtonOutcome.MANUAL_ONLY,reasonCode="NO_ACTIVE_REMOTE_TICKET"));return}
        if(!op.permit.isValid()||!valid(op.ticket)||foreground.get()!==activity||!op.clickRequested||op.buttonClaimed||FixtureReceiptStore.hash(text)!=op.ticket.textDigest){done(FixtureButtonResult(FixtureButtonOutcome.REJECTED,reasonCode="REMOTE_TICKET_INVALID"));return}
        op.buttonClaimed=true
        storage.execute {
            val committed=runCatching{check(!op.ended && op.permit.isValid() && SystemClock.elapsedRealtime()<op.ticket.deadlineElapsedMs);store!!.commit(op.ticket,text)}
            main.post {
                val receipt=committed.getOrNull()
                if(receipt==null||op.ended||!op.permit.isValid()||!valid(op.ticket)||foreground.get()!==activity){done(FixtureButtonResult(FixtureButtonOutcome.REJECTED,reasonCode="RECEIPT_OR_LEASE_INVALID"));if(!op.ended)end(op,"execution_unknown","unknown","RECEIPT_COMMIT_UNCERTAIN");return@post}
                done(FixtureButtonResult(FixtureButtonOutcome.COMMITTED,receipt,"DURABLE_FIXTURE_RECEIPT"))
            }
        }
    }
    private fun settleKnown(op:Operation,code:String){val outcome=FixtureTicketGate.interrupted(op.inputRequested,op.inputChanged,op.inputVerified,op.clickRequested,code);end(op,outcome.status,outcome.effectState,outcome.code)}
    private fun invalidateOperation(reason:String){active?.let{it.permit.invalidate();end(it,"execution_unknown","unknown",reason)}}
    private fun invalidate(reason:String){sessionPermit.get()?.invalidate();invalidateOperation(reason)}
    private fun end(op:Operation,status:String,effect:String,code:String,evidence:List<String> = emptyList()){
        if(op.ended)return;op.permit.invalidate();op.ended=true;if(active===op)active=null
        val ref="driver_"+FixtureReceiptStore.hash(op.ticket.ticketId).take(40)
        storage.execute {runCatching{store!!.phase(op.ticket.ticketId,when(status){"partially_succeeded"->"partial";"cancelled"->"cancelled";"succeeded"->"verified";else->"unknown"})};main.post {op.done(FixtureDriverResult(status,effect,code,(listOf(ref)+evidence).take(16)))}}
    }
}
