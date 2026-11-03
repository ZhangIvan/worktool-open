package io.worktool.open.prototype.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Authored JVM tests, NOT_RUN: no local Android SDK/Gradle toolchain was downloaded or executed. */
class NativeCommandEngineTest {
    private val now=1893456000000L
    private val binding=Binding("tenant_demo","device_demo","session_demo",1,now)
    private class MemoryJournal: Journal {
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
        override fun acknowledge(commandId:String){rows[commandId]?.let{if(it.result!=null)rows[commandId]=it.copy(acked=true)}}
        override fun recoverInterrupted(){rows.values.toList().filter{it.stage=="running"&&it.result==null}.forEach{r->finish(r.id,JSONObject().put("commandId",r.id).put("attemptId",r.attemptId).put("status","execution_unknown").put("effectState","unknown").put("code","PROCESS_INTERRUPTED").put("evidenceRefs",JSONArray().put("journal_recovery")),null)}}
        override fun recent()=rows.values.toList().takeLast(100)
        override fun effectRows()=effects.map{it.value}
        override fun close()=Unit
    }
    private class Link:DeviceLink {
        var online=true;var failStarted=false;var eventIds=emptyMap<String,String>();val sent=mutableListOf<Pair<String,JSONObject>>()
        override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit)=error("NOT_USED")
        override fun connect(identity:Identity,listener:LinkListener)=Unit
        override fun send(kind:String,body:JSONObject):String?{if(kind=="started"&&failStarted){online=false;return null};sent.add(kind to JSONObject(body.toString()));return eventIds[kind] ?: "sent_event_${sent.size}"}
        override fun connected()=online
        override fun disconnect(){online=false}
    }
    private class Events:EngineEvents{val confirmations=mutableListOf<String>();val errors=mutableListOf<String>();override fun changed()=Unit;override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String){confirmations.add(commandId)};override fun protocolError(code:String){errors.add(code)}}
    private data class Harness(val engine:NativeCommandEngine,val journal:MemoryJournal,val link:Link,val events:Events)
    private fun harness(resume:Boolean=true):Harness {val j=MemoryJournal();val l=Link();val e=Events();val engine=NativeCommandEngine(j,l,e){now};engine.ready(binding);if(resume)engine.resume();return Harness(engine,j,l,e)}
    private fun command(id:String="command_demo",export:Boolean=false):JSONObject {
        val p=if(export)JSONObject().put("exportId","export_demo").put("approvalId","approval_demo").put("scopeDigest",WireValidation.scopeDigest("device_demo",listOf("synthetic_note_001"))) else JSONObject().put("targetId","target_demo").put("text","Synthetic text only")
        val b=JSONObject().put("commandId",id).put("attemptId","attempt_demo").put("capability",if(export)"file.export" else "message.send_text").put("payload",p).put("policyVersion",1).put("requiresLocalConfirmation",export).put("expiresAt","2030-01-01T00:02:00Z")
        return b.put("payloadDigest",WireValidation.payloadDigest(b,"device_demo"))
    }
    private fun frame(body:JSONObject,seq:Long=1,event:String="event_demo",kind:String="command")=JSONObject().put("version","1.0").put("kind",kind).put("eventId",event).put("tenantId",binding.tenantId).put("deviceId",binding.deviceId).put("sessionId",binding.sessionId).put("seq",seq).put("sentAt","2030-01-01T00:00:00Z").put("mode","synthetic").put("body",body)

    @Test fun textEffectDurableAndDuplicateDoesNotRepeat(){val h=harness();val f=frame(command());h.engine.frame(f);assertEquals(1,h.journal.effects.size);assertEquals("succeeded",h.journal.command("command_demo")!!.stage);h.engine.frame(JSONObject(f.toString()));assertEquals(1,h.journal.effects.size);assertTrue(h.link.sent.any{it.first=="accepted"});assertTrue(h.link.sent.any{it.first=="started"})}
    @Test fun defaultPausedRejectsWithoutEffect(){val h=harness(false);h.engine.frame(frame(command()));assertEquals("rejected",h.journal.command("command_demo")!!.stage);assertTrue(h.journal.effects.isEmpty())}
    @Test fun exportRequiresExplicitMatchingLocalScope(){val h=harness();h.engine.frame(frame(command(export=true)));assertTrue(h.journal.effects.isEmpty());assertEquals(listOf("command_demo"),h.events.confirmations);h.engine.confirmExport("command_demo",true,listOf("synthetic_note_001"));assertEquals("synthetic_export",h.journal.effects.single().kind);assertEquals(64,h.journal.effects.single().value.length)}
    @Test fun wrongReferenceOrArbitraryPathRejected(){val h=harness();h.engine.frame(frame(command(export=true)));h.engine.confirmExport("command_demo",true,listOf("/data/user/0/private.db"));assertEquals("rejected",h.journal.command("command_demo")!!.stage);assertTrue(h.journal.effects.isEmpty())}
    @Test fun stopBeforeConfirmationCancelsWithoutEffect(){val h=harness();h.engine.frame(frame(command(export=true)));h.engine.frame(frame(JSONObject().put("commandId","command_demo").put("attemptId","attempt_demo").put("reason","operator_cancel").put("policyVersion",1),2,"event_stop","stop_request"));h.engine.confirmExport("command_demo",true,listOf("synthetic_note_001"));assertEquals("cancelled",h.journal.command("command_demo")!!.stage);assertTrue(h.journal.effects.isEmpty())}
    @Test fun wrongIdentityAndExtraFieldsFailClosed(){val h=harness();val f=frame(command()).put("tenantId","tenant_other");h.engine.frame(f);assertTrue(h.events.errors.isNotEmpty());assertTrue(h.journal.effects.isEmpty());val x=harness();x.engine.frame(frame(command()).put("extra",true));assertFalse(x.link.online)}
    @Test fun conflictAndReverseDirectionFailClosed(){val h=harness();h.engine.frame(frame(command()));val changed=frame(command("command_other"));h.engine.frame(changed);assertFalse(h.link.online);assertEquals(1,h.journal.effects.size);val x=harness();x.engine.frame(frame(command(),kind="device_event"));assertFalse(x.link.online)}
    @Test fun expiredDoesNotExecute(){val h=harness();val b=command().put("expiresAt","2029-12-31T23:59:00Z");b.put("payloadDigest",WireValidation.payloadDigest(b,"device_demo"));h.engine.frame(frame(b));assertEquals("rejected",h.journal.command("command_demo")!!.stage);assertTrue(h.journal.effects.isEmpty())}
    @Test fun interruptedRunningRecoversUnknownWithoutReplay(){val h=harness();val b=command();h.journal.accept(b);h.journal.markRunning("command_demo");h.engine.closed("OFFLINE");h.engine.ready(binding.copy(sessionId="session_next"));assertEquals("execution_unknown",h.journal.command("command_demo")!!.stage);assertTrue(h.journal.effects.isEmpty());assertTrue(h.link.sent.any{it.first=="execution_result"&&it.second.getString("status")=="execution_unknown"})}
    @Test fun onlySentDurableResultCanBeAcknowledged(){val h=harness();h.engine.frame(frame(command()));val resultEvent="sent_event_${h.link.sent.indexOfFirst { it.first=="execution_result" }+1}";h.engine.frame(frame(JSONObject().put("commandId","command_demo").put("ackEventId",resultEvent),2,"event_ack","result_ack"));assertTrue(h.journal.command("command_demo")!!.acked);val x=harness();x.engine.frame(frame(JSONObject().put("commandId","command_demo").put("ackEventId","event_result"),kind="result_ack"));assertFalse(x.link.online)}
    @Test fun jcsOrderIndependentButScopeArrayOrderBound(){assertEquals(WireValidation.canonicalDigest(JSONObject().put("b",2).put("a",1)),WireValidation.canonicalDigest(JSONObject().put("a",1).put("b",2)));assertNotEquals(WireValidation.scopeDigest("device_demo",listOf("synthetic_note_001","synthetic_note_002")),WireValidation.scopeDigest("device_demo",listOf("synthetic_note_002","synthetic_note_001")))}
    @Test fun wrongAckEventIdCannotClearDurableResult(){val h=harness();h.engine.frame(frame(command()));h.engine.frame(frame(JSONObject().put("commandId","command_demo").put("ackEventId","event_wrong"),2,"event_ack","result_ack"));assertFalse(h.journal.command("command_demo")!!.acked);assertFalse(h.link.online)}
    @Test fun staleSentAtRejectsBeforeAcceptance(){val h=harness();h.engine.frame(frame(command()).put("sentAt","2029-12-31T23:58:00Z"));assertTrue(h.journal.rows.isEmpty());assertFalse(h.link.online)}

    @Test fun validCommandWithTenSecondsRemainingMayExecute(){val h=harness();val b=command().put("expiresAt","2030-01-01T00:00:10Z");b.put("payloadDigest",WireValidation.payloadDigest(b,"device_demo"));h.engine.frame(frame(b));assertEquals("succeeded",h.journal.command("command_demo")!!.stage);assertEquals(1,h.journal.effects.size)}
    @Test fun duplicateAckReceiptCannotClearAnotherPendingResult(){val h=harness();h.engine.frame(frame(command()));val resultEvent="sent_event_${h.link.sent.indexOfFirst{it.first=="execution_result"}+1}";val ack=frame(JSONObject().put("commandId","command_demo").put("ackEventId",resultEvent),2,"event_ack","result_ack");h.engine.frame(ack);h.engine.frame(frame(command("command_second"),3,"event_second"));h.engine.frame(JSONObject(ack.toString()));assertTrue(h.journal.command("command_demo")!!.acked);assertFalse(h.journal.command("command_second")!!.acked);assertEquals(1,h.journal.pendingResults().size);assertEquals(2,h.journal.effects.size)}

    @Test fun reconnectRejectsAll101AcceptedRowsBeyondRecentUiLimit(){val h=harness();repeat(101){h.journal.accept(command("command_$it"))};assertEquals(100,h.journal.recent().size);assertEquals(101,h.journal.pendingCommands().size);h.engine.closed("OFFLINE");h.engine.ready(binding.copy(sessionId="session_next"));assertTrue(h.journal.pendingCommands().isEmpty());assertEquals(101,h.journal.rows.values.count{it.stage=="rejected"});assertTrue(h.journal.effects.isEmpty())}
    @Test fun pauseRejectsAll101AcceptedRowsBeyondRecentUiLimit(){val h=harness();repeat(101){h.journal.accept(command("command_$it"))};assertEquals(100,h.journal.recent().size);h.engine.pause();assertTrue(h.journal.pendingCommands().isEmpty());assertEquals(101,h.journal.rows.values.count{it.stage=="rejected"});assertTrue(h.journal.effects.isEmpty())}

    @Test fun failedStartedSendDisconnectsBeforeAnyEffectAndPersistsFailure(){val h=harness();h.link.failStarted=true;h.engine.frame(frame(command()));val row=h.journal.command("command_demo")!!;assertEquals("failed",row.stage);assertEquals("not_applied",JSONObject(row.result!!).getString("effectState"));assertEquals("START_NOTIFICATION_NOT_SENT",JSONObject(row.result!!).getString("code"));assertTrue(h.journal.effects.isEmpty());assertFalse(h.link.online);assertEquals(1,h.journal.pendingResults().size);assertFalse(h.link.sent.any{it.first=="execution_result"})}

    @Test fun revokedPolicyPublishesPausedGateAndCannotResumeLocally() {
        val h=harness()
        assertFalse(h.engine.isPaused())
        h.engine.frame(frame(JSONObject().put("policyVersion",2).put("revoked",true),1,"event_revoke","config_notification"))
        assertTrue(h.engine.isPaused())
        h.engine.resume()
        assertTrue(h.engine.isPaused())
        h.engine.frame(frame(command(),2,"event_after_revoke"))
        assertTrue(h.journal.effects.isEmpty())
        assertTrue(h.engine.isPaused())
    }
    private fun sharedExchange():JSONObject = javaClass.getResourceAsStream("/android-go-1.0.json")!!
        .bufferedReader(Charsets.UTF_8).use { JSONObject(it.readText()) }

    @Test fun sharedGoExchangeExecutesOnceAndAcknowledgesExactResult() {
        val fixture=sharedExchange(); val frames=fixture.getJSONArray("frames")
        assertEquals(WireValidation.canonicalDigest(fixture.getJSONObject("hello")),
            WireValidation.canonicalDigest(io.worktool.open.prototype.policy.ConnectionProfile.SYNTHETIC.hello()))
        val command=frames.getJSONObject(0).getJSONObject("frame")
        val hello=fixture.getJSONObject("helloAck")
        val clock=java.time.Instant.parse(fixture.getString("clock")).toEpochMilli()
        val j=MemoryJournal();val l=Link();val e=Events()
        l.eventIds=mapOf("accepted" to "evt_accepted", "started" to "evt_started", "execution_result" to "evt_result")
        val engine=NativeCommandEngine(j,l,e){clock}
        engine.ready(Binding(hello.getString("tenantId"),hello.getString("deviceId"),hello.getString("sessionId"),hello.getLong("policyVersion"),clock))
        engine.resume()
        engine.frame(command)
        // Re-delivery of the same event must not repeat the local effect.
        engine.frame(JSONObject(command.toString()))
        assertEquals(1,j.effects.size)
        assertEquals("synthetic_inbox",j.effects.single().kind)
        assertEquals(command.getJSONObject("body").getJSONObject("payload").getString("text"),j.effects.single().value)
        for (i in 1 until frames.length()) {
            val entry=frames.getJSONObject(i);val f=entry.getJSONObject("frame")
            if(entry.getString("direction")=="device_to_server") {
                val actual=l.sent.first { it.first==f.getString("kind") }.second
                assertEquals(WireValidation.canonicalDigest(f.getJSONObject("body")),WireValidation.canonicalDigest(actual))
            } else engine.frame(f)
        }
        assertTrue(j.command("cmd_text")!!.acked)
        assertTrue(j.pendingResults().isEmpty())
        assertTrue(e.errors.isEmpty())
    }

    @Test fun sharedUnackedResultSurvivesEngineReplacementWithoutEffectReplay() {
        val fixture=sharedExchange();val command=fixture.getJSONArray("frames").getJSONObject(0).getJSONObject("frame")
        val clock=java.time.Instant.parse(fixture.getString("clock")).toEpochMilli()
        val b=Binding("tenant_fixture","device_fixture","session_fixture",7,clock)
        val j=MemoryJournal();val l=Link();val e=Events()
        val first=NativeCommandEngine(j,l,e){clock};first.ready(b);first.resume();first.frame(command)
        assertFalse(j.command("cmd_text")!!.acked)
        val nextLink=Link();nextLink.eventIds=mapOf("execution_result" to "evt_recovered_result")
        val next=NativeCommandEngine(j,nextLink,e){clock};next.ready(b.copy(sessionId="session_recovered"))
        assertEquals(1,j.effects.size)
        assertEquals(1,nextLink.sent.count{it.first=="execution_result"})
        val ack=JSONObject(fixture.getJSONArray("frames").getJSONObject(6).getJSONObject("frame").toString())
            .put("sessionId","session_recovered").put("seq",1).put("eventId","evt_recovered_ack")
        ack.getJSONObject("body").put("ackEventId","evt_recovered_result")
        next.frame(ack)
        assertTrue(j.command("cmd_text")!!.acked)
        assertTrue(e.errors.isEmpty())
        // MemoryJournal validates engine recovery, not SQLite process-death durability.
    }

}
