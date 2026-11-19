package io.worktool.open.prototype.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Pure Kotlin/JVM engine tests with in-memory journal/link and a deterministic clock. No Android runtime, sockets or devices. */
class BoundedResultReplayTest {
    private val now=1893456000000L
    private val binding=Binding("tenant_demo","device_demo","session_demo",1,now)
    private class MemoryJournal: Journal {
        var failAck=false;var failSelect=false;var selections=0;var beforeSelect:()->Unit={}
        val rows=linkedMapOf<String,StoredCommand>(); val effects=mutableListOf<LocalEffect>()
        private val events=mutableMapOf<String,String>(); private val stops=mutableSetOf<String>()
        override fun command(id:String)=rows[id]
        override fun accept(body:JSONObject):Boolean {
            val id=body.getString("commandId"); if(rows.containsKey(id))return false
            rows[id]=StoredCommand(id,body.getString("payloadDigest"),body.getString("attemptId"),"accepted",body.toString(),null,false,id in stops);return true
        }
        override fun markRunning(id:String){val r=rows.getValue(id);check(r.stage=="accepted"&&!r.stopped);rows[id]=r.copy(stage="running")}
        override fun finish(id:String,result:JSONObject,effect:LocalEffect?){val r=rows.getValue(id);check(r.result==null);rows[id]=r.copy(stage=result.getString("status"),result=result.toString());if(effect!=null)effects.add(effect)}
        override fun requestStop(id:String,attemptId:String){stops.add(id);rows[id]?.let{if(it.attemptId==attemptId)rows[id]=it.copy(stopped=true)}}
        override fun event(sessionId:String,eventId:String,digest:String):EventDecision {val key="$sessionId/$eventId";val old=events[key];return if(old==null){events[key]=digest;EventDecision.NEW}else if(old==digest)EventDecision.DUPLICATE else EventDecision.CONFLICT}
        override fun pendingCommands()=rows.values.filter{it.result==null}
        override fun pendingResults()=rows.values.filter{!it.acked&&it.result!=null}.map{JSONObject(it.result!!)}
        override fun nextPendingResult():JSONObject? {selections++;check(!failSelect);beforeSelect();return pendingResults().firstOrNull()}
        override fun acknowledge(commandId:String){check(!failAck);rows[commandId]?.let{if(it.result!=null)rows[commandId]=it.copy(acked=true)}}
        override fun recoverInterrupted(){rows.values.toList().filter{it.stage=="running"&&it.result==null}.forEach{r->finish(r.id,JSONObject().put("commandId",r.id).put("attemptId",r.attemptId).put("status","execution_unknown").put("effectState","unknown").put("code","PROCESS_INTERRUPTED").put("evidenceRefs",JSONArray().put("journal_recovery")),null)}}
        override fun recent()=rows.values.toList().takeLast(100)
        override fun effectRows()=effects.map{it.value}
        override fun close()=Unit
    }
    private class Link:DeviceLink {
        var elapsed=0L
        var online=true;var failStarted=false;var failResult=false;var resultSendDelay=0L;var outgoingBudget:io.worktool.open.prototype.transport.OutboundSessionBudget?=null;var eventIds=emptyMap<String,String>();val sent=mutableListOf<Pair<String,JSONObject>>()
        override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit)=error("NOT_USED")
        override fun connect(identity:Identity,listener:LinkListener)=Unit
        override fun send(kind:String,body:JSONObject):String?{if(outgoingBudget?.take(elapsed,body.toString().toByteArray(Charsets.UTF_8).size)==false){online=false;return null};if((kind=="started"&&failStarted)||(kind=="execution_result"&&failResult)){online=false;return null};if(kind=="execution_result")elapsed+=resultSendDelay;sent.add(kind to JSONObject(body.toString()));return eventIds[kind] ?: "sent_event_${sent.size}"}
        override fun connected()=online
        override fun disconnect(){online=false}
    }
    private class Events:EngineEvents{val confirmations=mutableListOf<String>();val errors=mutableListOf<String>();override fun changed()=Unit;override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String){confirmations.add(commandId)};override fun protocolError(code:String){errors.add(code)}}
    private data class Harness(val engine:NativeCommandEngine,val journal:MemoryJournal,val link:Link,val events:Events)
    private fun harness(resume:Boolean=true,budget:io.worktool.open.prototype.policy.ClientBudget=io.worktool.open.prototype.policy.UnrestrictedClientBudget):Harness {val j=MemoryJournal();val l=Link();val e=Events();val engine=NativeCommandEngine(j,l,e,workBudget=budget,elapsedClock={l.elapsed},clock={now});engine.ready(binding);if(resume)engine.resume();return Harness(engine,j,l,e)}
    private fun command(id:String="command_demo",export:Boolean=false):JSONObject {
        val p=if(export)JSONObject().put("exportId","export_demo").put("approvalId","approval_demo").put("scopeDigest",WireValidation.scopeDigest("device_demo",listOf("synthetic_note_001"))) else JSONObject().put("targetId","target_demo").put("text","Synthetic text only")
        val b=JSONObject().put("commandId",id).put("attemptId","attempt_demo").put("capability",if(export)"file.export" else "message.send_text").put("payload",p).put("policyVersion",1).put("requiresLocalConfirmation",export).put("expiresAt","2030-01-01T00:02:00Z")
        return b.put("payloadDigest",WireValidation.payloadDigest(b,"device_demo"))
    }
    private fun frame(body:JSONObject,seq:Long=1,event:String="event_demo",kind:String="command")=JSONObject().put("version","1.0").put("kind",kind).put("eventId",event).put("tenantId",binding.tenantId).put("deviceId",binding.deviceId).put("sessionId",binding.sessionId).put("seq",seq).put("sentAt","2030-01-01T00:00:00Z").put("mode","synthetic").put("body",body)

    private fun stage(h:Harness,count:Int) { repeat(count) { h.journal.accept(command("command_$it")) }; h.engine.ready(binding) }
    private fun resultFrames(h:Harness)=h.link.sent.filter { it.first=="execution_result" }
    private fun ack(h:Harness,id:String,event:String,seq:Long=1) {
        h.engine.frame(frame(JSONObject().put("commandId",id).put("ackEventId",event),seq,"ack_event_$seq","result_ack"))
    }
    private fun tick(h:Harness,at:Long){h.link.elapsed=at;h.engine.replayTick()}

    @Test fun pausedReadyStages101ButOnlyOneOutstanding() {
        val h=harness(false);stage(h,101)
        assertTrue(h.engine.isPaused());assertEquals(1,resultFrames(h).size)
        assertEquals(101,h.journal.pendingResults().size);assertTrue(h.journal.pendingCommands().isEmpty());assertTrue(h.journal.effects.isEmpty())
        val lookups=h.journal.selections
        repeat(50){tick(h,(it+1)*250L)}
        assertEquals(lookups,h.journal.selections);assertEquals(1,resultFrames(h).size)
    }
    @Test fun exactAckDurablyReleasesOnlyAfterPacing() {
        val h=harness();stage(h,2);ack(h,"command_0","sent_event_1")
        assertTrue(h.journal.command("command_0")!!.acked);assertEquals(1,resultFrames(h).size)
        tick(h,499);assertEquals(1,resultFrames(h).size)
        tick(h,500);assertEquals(2,resultFrames(h).size);assertFalse(h.journal.command("command_1")!!.acked)
    }
    @Test fun lateAckForAHasNoCreditForB() {
        val h=harness();stage(h,3);ack(h,"command_0","sent_event_1");tick(h,500)
        ack(h,"command_0","sent_event_1",2);tick(h,1000)
        assertEquals(2,resultFrames(h).size);assertFalse(h.journal.command("command_1")!!.acked)
        assertTrue(h.link.online)
    }
    @Test fun foreignAckFailsClosedWithoutClearingEvidence() {
        val h=harness();stage(h,2);ack(h,"command_0","sent_event_foreign")
        assertFalse(h.link.online);assertEquals(2,h.journal.pendingResults().size);assertFalse(h.engine.needsReplayTick())
    }
    @Test fun wrongCommandAckCannotFreeCredit() {
        val h=harness();stage(h,2);ack(h,"command_1","sent_event_1")
        assertFalse(h.link.online);assertEquals(2,h.journal.pendingResults().size)
    }
    @Test fun ackLossTimesOutAt15SecondsWithNoRetry() {
        val h=harness();stage(h,2);tick(h,14999);assertTrue(h.link.online)
        tick(h,15000);assertFalse(h.link.online);assertEquals(1,resultFrames(h).size)
        assertEquals(2,h.journal.pendingResults().size);assertTrue(h.events.errors.contains("RESULT_DELIVERY_MANUAL_RESUME_REQUIRED"))
    }
    @Test fun journalAckFailureKeepsBothRowsAndStopsPump() {
        val h=harness();stage(h,2);h.journal.failAck=true;ack(h,"command_0","sent_event_1")
        assertFalse(h.link.online);assertEquals(2,h.journal.pendingResults().size)
        tick(h,500);assertEquals(1,resultFrames(h).size)
    }
    @Test fun pendingLookupFailureClosesWithoutDroppingTail() {
        val h=harness();stage(h,2);ack(h,"command_0","sent_event_1");h.journal.failSelect=true;tick(h,500)
        assertFalse(h.link.online);assertEquals(1,h.journal.pendingResults().size);assertEquals(1,resultFrames(h).size)
    }
    @Test fun failedSocketEnqueueRetainsResultAndNoAckCorrelation() {
        val h=harness();h.link.failResult=true;stage(h,2)
        assertFalse(h.link.online);assertEquals(2,h.journal.pendingResults().size);assertTrue(resultFrames(h).isEmpty());assertFalse(h.engine.needsReplayTick())
    }
    @Test fun explicitPauseStopsBeforePendingRejections() {
        val h=harness();h.journal.accept(command());h.engine.pause();tick(h,500)
        assertTrue(resultFrames(h).isEmpty());assertEquals(1,h.journal.pendingResults().size);assertFalse(h.engine.needsReplayTick())
    }
    @Test fun closedTickDoesNotSendAndFreshSessionReplaysOnlyEvidence() {
        val h=harness();stage(h,2);h.engine.closed("OFFLINE");tick(h,1000)
        assertEquals(1,resultFrames(h).size);h.engine.ready(binding.copy(sessionId="session_next"))
        assertEquals(2,resultFrames(h).size);assertEquals(2,h.journal.pendingResults().size);assertTrue(h.journal.effects.isEmpty())
    }
    @Test fun oldSessionAckRejectedAfterReady() {
        val h=harness();stage(h,2);h.engine.closed("OFFLINE");h.engine.ready(binding.copy(sessionId="session_next"))
        ack(h,"command_0","sent_event_1")
        assertFalse(h.link.online);assertEquals(2,h.journal.pendingResults().size)
    }
    @Test fun newCompletionWaitsBehindOutstandingRecovery() {
        val h=harness();stage(h,1);h.engine.resume();h.engine.frame(frame(command("command_new")))
        assertEquals(1,h.journal.effects.size);assertEquals(1,resultFrames(h).size)
        ack(h,"command_0","sent_event_1",2);tick(h,500)
        assertEquals(2,resultFrames(h).size);assertEquals("command_new",resultFrames(h).last().second.getString("commandId"))
        assertEquals(1,h.journal.effects.size)
    }
    @Test fun duplicateCommandCannotBypassWindowOrReplayEffect() {
        val h=harness();h.engine.frame(frame(command()));h.engine.frame(frame(command(),2,"event_duplicate"))
        assertEquals(1,h.journal.effects.size);assertEquals(1,resultFrames(h).size)
        val event="sent_event_${h.link.sent.indexOfFirst {it.first=="execution_result"}+1}"
        ack(h,"command_demo",event,3);h.engine.frame(frame(command(),4,"event_after_ack"));tick(h,500)
        assertEquals(1,resultFrames(h).size);assertEquals(1,h.journal.effects.size)
    }
    @Test fun idleTicksDoNotRepeatedlyQueryJournal() {
        val h=harness();val reads=h.journal.selections
        repeat(100){tick(h,it*250L)}
        assertEquals(reads,h.journal.selections);assertFalse(h.engine.needsReplayTick())
    }
    @Test fun largeBacklogRetainsTailAtSessionCapAndManualReadyContinues() {
        val h=harness()
        h.link.outgoingBudget=io.worktool.open.prototype.transport.OutboundSessionBudget(0)
        assertTrue(h.link.outgoingBudget!!.take(0,128)) // synthetic HELLO accounting
        stage(h,257)
        repeat(239) { index ->
            ack(h,"command_$index","sent_event_${index+1}",index+1L)
            tick(h,(index+1)*500L)
        }
        assertFalse(h.link.online);assertEquals(239,resultFrames(h).size)
        assertEquals(18,h.journal.pendingResults().size);assertTrue(h.journal.effects.isEmpty())
        h.engine.closed("OUTBOUND_LIMIT_MANUAL_RESUME_REQUIRED")
        h.link.online=true;h.link.outgoingBudget=io.worktool.open.prototype.transport.OutboundSessionBudget(h.link.elapsed)
        assertTrue(h.link.outgoingBudget!!.take(h.link.elapsed,128))
        h.engine.ready(binding.copy(sessionId="session_next"))
        assertEquals("command_239",resultFrames(h).last().second.getString("commandId"))
        assertEquals(18,h.journal.pendingResults().size);assertTrue(h.journal.effects.isEmpty())
        repeat(18) { index ->
            h.engine.frame(frame(JSONObject().put("commandId","command_${239+index}").put("ackEventId","sent_event_${240+index}"),index+1L,"tail_ack_$index","result_ack").put("sessionId","session_next"))
            tick(h,120000+index*500L)
        }
        assertTrue(h.journal.pendingResults().isEmpty());assertEquals(257,resultFrames(h).size)
        assertEquals(257,h.journal.rows.values.count { it.acked });assertTrue(h.journal.effects.isEmpty())
    }

    @Test fun interruptedRunningBecomesUnknownWithoutEffectReplay() {
        val h=harness();h.journal.accept(command());h.journal.markRunning("command_demo")
        h.engine.ready(binding)
        assertEquals("execution_unknown",h.journal.command("command_demo")!!.stage)
        assertEquals("unknown",resultFrames(h).single().second.getString("effectState"))
        assertTrue(h.journal.effects.isEmpty());assertFalse(h.journal.command("command_demo")!!.acked)
    }
    @Test fun revocationKeepsEvidencePumpButCannotResumeEffects() {
        val h=harness();stage(h,1);h.engine.resume();h.journal.accept(command("command_later"))
        h.engine.frame(frame(JSONObject().put("policyVersion",2).put("revoked",true),1,"event_revoke","config_notification"))
        h.engine.resume();assertTrue(h.engine.isPaused());assertEquals(1,resultFrames(h).size)
        ack(h,"command_0","sent_event_1",2);tick(h,500)
        assertEquals(2,resultFrames(h).size);assertEquals("POLICY_REVOKED",resultFrames(h).last().second.getString("code"))
        assertTrue(h.journal.effects.isEmpty())
    }
    @Test fun invalidatedPermitStopsDelayedReplayWithoutSendingOrScanning() {
        var valid=true
        val permit=object:io.worktool.open.prototype.policy.ClientBudget by io.worktool.open.prototype.policy.UnrestrictedClientBudget {
            override fun valid(epoch:Long)=valid
        }
        val h=harness(budget=permit);stage(h,2);ack(h,"command_0","sent_event_1")
        val reads=h.journal.selections;valid=false;tick(h,500)
        assertEquals(1,resultFrames(h).size);assertEquals(reads,h.journal.selections)
        assertFalse(h.engine.needsReplayTick());assertEquals(1,h.journal.pendingResults().size)
    }

    @Test fun slowJournalLookupCannotShortenPostEnqueuePacing() {
        val h=harness();h.journal.beforeSelect={h.link.elapsed+=1000};stage(h,2)
        h.journal.beforeSelect={};ack(h,"command_0","sent_event_1")
        assertEquals(1,resultFrames(h).size);tick(h,1499);assertEquals(1,resultFrames(h).size)
        tick(h,1500);assertEquals(2,resultFrames(h).size)
    }
    @Test fun slowSocketEnqueueStartsPacingAndAckDeadlineAfterReturn() {
        val h=harness();h.link.resultSendDelay=1000;stage(h,2);h.link.resultSendDelay=0
        ack(h,"command_0","sent_event_1");assertEquals(1,resultFrames(h).size)
        tick(h,1499);assertEquals(1,resultFrames(h).size);tick(h,1500);assertEquals(2,resultFrames(h).size)
        val lost=harness();lost.link.resultSendDelay=1000;stage(lost,1)
        tick(lost,15999);assertTrue(lost.link.online);tick(lost,16000);assertFalse(lost.link.online)
    }

}
