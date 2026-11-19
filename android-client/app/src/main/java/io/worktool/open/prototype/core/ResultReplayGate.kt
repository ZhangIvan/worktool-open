package io.worktool.open.prototype.core

/** Serial-worker state only. Caller supplies a monotonic clock and at most one delayed tick.
 * No timer, journal, execution authority or automatic reconnect lives here.
 */
class ResultReplayGate {
    companion object { const val INTERVAL_MS=500L; const val ACK_TIMEOUT_MS=15000L }
    var active=false; private set
    private var commandId:String?=null
    private var eventId:String?=null
    private var sentAt:Long?=null
    private var requested=false
    fun start() { active=true; commandId=null; eventId=null; sentAt=null; requested=true }
    fun stop() { active=false; requested=false }
    fun request() { if(active)requested=true }
    fun needsTick()=active && (requested || commandId!=null)
    fun timedOut(now:Long)=active && commandId!=null && sentAt?.let { now-it>=ACK_TIMEOUT_MS || now<it }==true
    fun canSend(now:Long)=active && requested && commandId==null && sentAt?.let { now>=it && now-it>=INTERVAL_MS }!=false
    fun empty() { requested=false }
    fun sent(command:String,event:String,now:Long) { check(active && commandId==null);commandId=command;eventId=event;sentAt=now;requested=false }
    // Only call after exact session/command/event validation and durable journal ACK.
    fun acknowledged(command:String,event:String) { if(commandId==command && eventId==event) {commandId=null;eventId=null;requested=true} }
}
