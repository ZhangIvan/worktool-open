package io.worktool.open.prototype.core

import io.worktool.open.prototype.policy.ConnectionMode
import io.worktool.open.prototype.policy.ConnectionProfile
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Pure journal/profile tests. NOT_RUN until official CI; not Android SQLite evidence. */
class FixtureModeRecoveryTest {
    private class Ledger:Journal {
        val rows=linkedMapOf<String,StoredCommand>();var recoveries=0;var acknowledgements=0;var recentReads=0
        override fun command(id:String)=rows[id]
        override fun pendingCommands()=rows.values.filter{it.result==null}
        override fun pendingResults()=rows.values.filter{it.result!=null&&!it.acked}.map{JSONObject(it.result!!)}
        override fun recent():List<StoredCommand>{recentReads++;return rows.values.toList().takeLast(100)}
        override fun recoverInterrupted(){recoveries++}
        override fun acknowledge(commandId:String){acknowledgements++}
        override fun accept(body:JSONObject)=error("not used")
        override fun markRunning(id:String)=error("not used")
        override fun finish(id:String,result:JSONObject,effect:LocalEffect?)=error("not used")
        override fun requestStop(id:String,attemptId:String)=error("not used")
        override fun event(sessionId:String,eventId:String,digest:String)=error("not used")
        override fun effectRows()=emptyList<String>()
        override fun close()=Unit
    }
    private fun row(id:String,fixture:Boolean=true,result:Boolean=false,acked:Boolean=false):StoredCommand {
        val body=JSONObject().put("commandId",id).put("capability",if(fixture)"fixture.ui.submit_text" else "message.send_text")
        val value=if(result)JSONObject().put("commandId",id).put("status","execution_unknown").toString() else null
        return StoredCommand(id,"digest","attempt",if(result)"execution_unknown" else "running",body.toString(),value,acked,false)
    }
    @Test fun defaultProfileIsExactV10AndHasNoDriver(){val hello=ConnectionProfile.SYNTHETIC.hello();assertEquals("1.0",hello.getJSONArray("supportedVersions").getString(0));assertEquals(1,hello.getJSONArray("supportedVersions").length());assertFalse(hello.has("drivers"));assertEquals(2,hello.getJSONArray("capabilities").length())}
    @Test fun explicitProfileNeverOffersDowngrade(){val p=ConnectionProfile(ConnectionMode.OWN_FIXTURE_1_1,FixtureTarget("io.worktool.open.prototype","io.worktool.open.prototype.ui.FixtureActivity"));val h=p.hello();assertEquals("1.1",h.getJSONArray("supportedVersions").getString(0));assertEquals(1,h.getJSONArray("supportedVersions").length());assertEquals(1,h.getJSONArray("drivers").length());assertFalse(p.mode.allowed(false))}
    @Test fun pendingBeyondRecentLimitBlocksBeforeRecoveryOrAck(){val j=Ledger();j.rows["fixture_old"]=row("fixture_old");repeat(150){val id="synthetic_$it";j.rows[id]=row(id,false,true,true)};assertFalse(ConnectionProfile.canRecover("1.0",j));assertEquals(0,j.recentReads);assertEquals(0,j.recoveries);assertEquals(0,j.acknowledgements);assertEquals(151,j.rows.size)}
    @Test fun unackedUnknownBlocksV10ButCanBeRetainedForV11(){val j=Ledger();j.rows["fixture_old"]=row("fixture_old",result=true);assertFalse(ConnectionProfile.canRecover("1.0",j));assertTrue(ConnectionProfile.canRecover("1.1",j));assertFalse(j.rows.getValue("fixture_old").acked);assertEquals(0,j.recoveries)}
    @Test fun acknowledgedTerminalDoesNotBlockAndIsNotDeleted(){val j=Ledger();j.rows["fixture_old"]=row("fixture_old",result=true,acked=true);assertTrue(ConnectionProfile.canRecover("1.0",j));assertEquals(1,j.rows.size);assertEquals(0,j.acknowledgements)}
    @Test fun malformedBodyCannotBeSkipped(){val j=Ledger();j.rows["fixture_old"]=row("fixture_old").copy(body="{}");try{ConnectionProfile.canRecover("1.0",j);fail("bad body skipped")}catch(_:org.json.JSONException){};assertEquals(1,j.rows.size)}
    @Test fun engineRejectsV10BeforeRecoverySendAndWrongVersionAck() {
        val j=Ledger();j.rows["fixture_old"]=row("fixture_old",result=true)
        var sent=0;var connected=true;val errors=mutableListOf<String>()
        val link=object:DeviceLink {
            override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit)=Unit
            override fun connect(identity:Identity,listener:LinkListener)=Unit
            override fun send(kind:String,body:JSONObject):String?{sent++;return "event_sent"}
            override fun connected()=connected
            override fun disconnect(){connected=false}
        }
        val events=object:EngineEvents {
            override fun changed()=Unit
            override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String)=Unit
            override fun protocolError(code:String){errors.add(code)}
        }
        val engine=NativeCommandEngine(j,link,events,clock={1893456000000L})
        engine.ready(Binding("tenant_demo","device_demo","session_demo",1,1893456000000L,"1.0"))
        assertTrue(errors.contains("PENDING_FIXTURE_REQUIRES_1_1"));assertTrue(engine.isPaused());assertFalse(connected)
        assertEquals(0,sent);assertEquals(0,j.recoveries);assertEquals(0,j.acknowledgements);assertFalse(j.rows.getValue("fixture_old").acked)
    }

}
