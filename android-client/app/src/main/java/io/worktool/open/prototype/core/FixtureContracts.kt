package io.worktool.open.prototype.core

/** Candidate interfaces; IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED. No arbitrary selectors. */
data class FixtureTarget(val targetPackage:String,val targetActivity:String)
data class FixtureDriverStatus(val serviceConnected:Boolean,val locallyEnabled:Boolean,val foreground:Boolean,val serviceEpoch:Long,val foregroundEpoch:Long,val reasonCode:String)
data class FixtureInspection(val supported:Boolean,val reasonCode:String,val target:FixtureTarget,val windowId:Int=-1,val serviceEpoch:Long=0,val foregroundEpoch:Long=0)
data class FixtureTicket(val ticketId:String,val commandId:String,val attemptId:String,val payloadDigest:String,val textDigest:String,val target:FixtureTarget,val windowId:Int,val serviceEpoch:Long,val foregroundEpoch:Long,val deadlineElapsedMs:Long)
data class FixtureDriverResult(val status:String,val effectState:String,val code:String,val evidenceRefs:List<String>)
data class FixtureApprovalRequest(val commandId:String,val attemptId:String,val payloadDigest:String,val target:FixtureTarget,val text:String,val textDigest:String)
interface FixtureDriver {
    fun status():FixtureDriverStatus
    /** Pure observation; no ticket consumption, writes or platform action. */
    fun inspect(target:FixtureTarget,deadlineElapsedMs:Long,done:(FixtureInspection)->Unit)
    fun execute(ticket:FixtureTicket,text:String,done:(FixtureDriverResult)->Unit)
    fun cancel(commandId:String,reasonCode:String)
}
