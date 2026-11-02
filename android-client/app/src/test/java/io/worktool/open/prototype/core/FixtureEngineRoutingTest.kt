package io.worktool.open.prototype.core

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** NOT_RUN: JVM orchestration tests with a fake driver, never evidence of Accessibility execution. */
class FixtureEngineRoutingTest {
    private val wall=1893456000000L
    private class Ledger:Journal {
        val rows=linkedMapOf<String,StoredCommand>();val effects=mutableListOf<LocalEffect>();val ev=mutableMapOf<String,String>()
        override fun command(id:String)=rows[id]
        override fun accept(b:JSONObject):Boolean{val id=b.getString("commandId");if(rows.containsKey(id))return false;rows[id]=StoredCommand(id,b.getString("payloadDigest"),b.getString("attemptId"),"accepted",b.toString(),null,false,false);return true}
        override fun markRunning(id:String){rows[id]=rows.getValue(id).copy(stage="running")}
        override fun finish(id:String,result:JSONObject,effect:LocalEffect?){val r=rows.getValue(id);check(r.result==null);rows[id]=r.copy(stage=result.getString("status"),result=result.toString());if(effect!=null)effects.add(effect)}
        override fun requestStop(id:String,attemptId:String){rows[id]?.let{rows[id]=it.copy(stopped=true)}}
        override fun event(sessionId:String,eventId:String,digest:String):EventDecision{val k="$sessionId/$eventId";val old=ev[k];return if(old==null){ev[k]=digest;EventDecision.NEW}else if(old==digest)EventDecision.DUPLICATE else EventDecision.CONFLICT}
        override fun pendingResults()=rows.values.mapNotNull{if(!it.acked)it.result?.let(::JSONObject) else null}
        override fun pendingCommands()=rows.values.filter{it.result==null}
        override fun acknowledge(commandId:String){rows[commandId]?.let{rows[commandId]=it.copy(acked=true)}}
        override fun recoverInterrupted(){rows.values.toList().filter{it.stage=="running"&&it.result==null}.forEach{r->finish(r.id,JSONObject().put("commandId",r.id).put("attemptId",r.attemptId).put("status","execution_unknown").put("effectState","unknown").put("code","PROCESS_INTERRUPTED").put("evidenceRefs",org.json.JSONArray().put("journal_recovery")),null)}}
        override fun recent()=rows.values.toList().takeLast(100)
        override fun effectRows()=emptyList<String>()
        override fun close()=Unit
    }
    private class Link:DeviceLink{var connected=true;val messages=mutableListOf<String>();override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit)=Unit;override fun connect(identity:Identity,listener:LinkListener)=Unit;override fun send(kind:String,body:JSONObject):String?{messages.add(kind);return "event_${messages.size}"};override fun connected()=connected;override fun disconnect(){connected=false}}
    private class Events:EngineEvents{val approvals=mutableListOf<FixtureApprovalRequest>();val errors=mutableListOf<String>();override fun changed()=Unit;override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String)=Unit;override fun protocolError(code:String){errors.add(code)};override fun fixtureConfirmationRequired(request:FixtureApprovalRequest){approvals.add(request)}}
    private class Driver:FixtureDriver{
        var throwCancellation=false;var deferCancellation=false;var inspected=0;var executed=0;var last:FixtureTicket?=null;var complete:((FixtureDriverResult)->Unit)?=null
        override fun status()=FixtureDriverStatus(true,true,true,3,4,"READY")
        override fun inspect(target:FixtureTarget,deadlineElapsedMs:Long,done:(FixtureInspection)->Unit){inspected++;done(FixtureInspection(true,"SUPPORTED",target,7,3,4))}
        override fun execute(ticket:FixtureTicket,text:String,done:(FixtureDriverResult)->Unit){executed++;last=ticket;complete=done}
        override fun cancel(commandId:String,reasonCode:String){if(throwCancellation)error("private cancellation detail");if(deferCancellation)return;complete?.invoke(FixtureDriverResult("execution_unknown","unknown","CANCELLED_AFTER_BEGIN",listOf("driver_test")));complete=null}
    }
    private fun body():JSONObject{val b=JSONObject().put("commandId","command_fixture").put("attemptId","attempt_fixture").put("capability","fixture.ui.submit_text").put("expiresAt","2030-01-01T00:02:00Z").put("policyVersion",1).put("requiresLocalConfirmation",true).put("payload",JSONObject().put("targetPackage","io.worktool.open.prototype").put("targetActivity","io.worktool.open.prototype.ui.FixtureActivity").put("text","Synthetic fixture"));return b.put("payloadDigest",WireValidation.payloadDigest(b,"device_demo"))}
    private fun frame(b:JSONObject,version:String="1.1",seq:Long=1)=JSONObject().put("version",version).put("kind","command").put("eventId","event_command_$seq").put("tenantId","tenant_demo").put("deviceId","device_demo").put("sessionId","session_demo").put("seq",seq).put("sentAt","2030-01-01T00:00:00Z").put("mode","synthetic").put("body",b)
    private data class H(val engine:NativeCommandEngine,val ledger:Ledger,val driver:Driver,val events:Events)
    private fun setup(version:String="1.1"):H{val j=Ledger();val l=Link();val d=Driver();val e=Events();val engine=NativeCommandEngine(j,l,e,fixtureDriver=d,fixtureTarget=FixtureTarget("io.worktool.open.prototype","io.worktool.open.prototype.ui.FixtureActivity"),dispatchToSerial={it()},elapsedClock={1000},clock={wall});engine.ready(Binding("tenant_demo","device_demo","session_demo",1,wall,version));engine.resume();return H(engine,j,d,e)}
    @Test fun v10NeverRoutesNewCapability(){val h=setup("1.0");h.engine.frame(frame(body(),"1.0"));assertEquals(0,h.driver.executed);assertTrue(h.ledger.rows.isEmpty());assertTrue(h.events.errors.isNotEmpty())}
    @Test fun inspectAndSubmissionWaitForExactTaskApprovalAndProof(){val h=setup();val b=body();h.engine.frame(frame(b));assertEquals(1,h.events.approvals.size);assertEquals(0,h.driver.inspected);h.engine.confirmFixture("command_fixture","attempt_wrong",b.getString("payloadDigest"),true);assertEquals(0,h.driver.executed);h.engine.confirmFixture("command_fixture","attempt_fixture",b.getString("payloadDigest"),true);assertEquals(1,h.driver.inspected);assertEquals(1,h.driver.executed);assertEquals("running",h.ledger.command("command_fixture")!!.stage);assertNull(h.ledger.command("command_fixture")!!.result);val t=h.driver.last!!;assertEquals(b.getString("payloadDigest"),t.payloadDigest);assertEquals(3L,t.serviceEpoch);assertEquals(4L,t.foregroundEpoch);assertEquals(6000L,t.deadlineElapsedMs);h.driver.complete!!(FixtureDriverResult("succeeded","applied","FIXTURE_RECEIPT_AND_POSTCONDITION",listOf("receipt_test","driver_test")));assertEquals("succeeded",h.ledger.command("command_fixture")!!.stage);assertTrue(h.ledger.effects.isEmpty())}
    @Test fun actionAcceptedWithoutReceiptCannotBecomeSuccess(){val h=setup();val b=body();h.engine.frame(frame(b));h.engine.confirmFixture("command_fixture","attempt_fixture",b.getString("payloadDigest"),true);h.driver.complete!!(FixtureDriverResult("succeeded","applied","ACTION_RETURNED_TRUE",emptyList()));assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage)}
    @Test fun pauseAndDuplicateNeverReplayUiAction(){val h=setup();val b=body();h.engine.frame(frame(b));h.engine.confirmFixture("command_fixture","attempt_fixture",b.getString("payloadDigest"),true);h.engine.pause();assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage);h.engine.frame(frame(b,seq=2));assertEquals(1,h.driver.executed)}
    @Test fun deferredDriverClosedPersistsUnknownAndLateReceiptCannotOverwriteOrReplay(){val h=setup();h.driver.deferCancellation=true;val b=body();h.engine.frame(frame(b));h.engine.confirmFixture("command_fixture","attempt_fixture",b.getString("payloadDigest"),true);assertEquals("running",h.ledger.command("command_fixture")!!.stage);h.engine.closed("CONNECTION_CLOSED");assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage);h.driver.complete!!(FixtureDriverResult("succeeded","applied","FIXTURE_RECEIPT_AND_POSTCONDITION",listOf("receipt_late")));assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage);h.engine.ready(Binding("tenant_demo","device_demo","session_demo",1,wall,"1.1"));h.engine.resume();h.engine.frame(frame(b));assertEquals(1,h.driver.executed);assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage)}

    @Test fun cancellationFailureStillRecoversJournalAndReportsFixedFailure(){
        val h=setup();val b=body();h.engine.frame(frame(b));h.engine.confirmFixture("command_fixture","attempt_fixture",b.getString("payloadDigest"),true)
        h.driver.throwCancellation=true;h.engine.closed("LOCAL_PAUSE")
        assertEquals("execution_unknown",h.ledger.command("command_fixture")!!.stage)
        assertTrue(h.events.errors.contains("FIXTURE_CANCEL_UNCONFIRMED"));assertTrue(h.engine.isPaused())
        assertFalse(h.events.errors.any{it.contains("private")})
    }

}
