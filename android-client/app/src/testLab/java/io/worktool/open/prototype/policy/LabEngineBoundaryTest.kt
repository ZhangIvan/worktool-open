package io.worktool.open.prototype.policy

import io.worktool.open.prototype.core.*
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

/** Synthetic in-memory callback test; does not execute Android or contact any endpoint. */
class LabEngineBoundaryTest {
    @Test fun invalidTextNeverReachesCommandLedgerOrEffect() {
        var accepted=0;var effects=0;var online=true
        val journal=object:Journal {
            override fun command(id:String):StoredCommand?=null
            override fun accept(body:JSONObject):Boolean { accepted++;return true }
            override fun markRunning(id:String)=Unit
            override fun finish(id:String,result:JSONObject,effect:LocalEffect?) { if(effect!=null)effects++ }
            override fun requestStop(id:String,attemptId:String)=Unit
            override fun event(sessionId:String,eventId:String,digest:String)=EventDecision.NEW
            override fun pendingResults():List<JSONObject> = emptyList()
            override fun pendingCommands():List<StoredCommand> = emptyList()
            override fun acknowledge(commandId:String)=Unit
            override fun recoverInterrupted()=Unit
            override fun recent():List<StoredCommand> = emptyList()
            override fun effectRows():List<String> = emptyList()
            override fun close()=Unit
        }
        val link=object:DeviceLink {
            override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit)=Unit
            override fun connect(identity:Identity,listener:LinkListener)=Unit
            override fun send(kind:String,body:JSONObject):String?="evt_sent"
            override fun connected()=online
            override fun disconnect(){online=false}
        }
        val events=object:EngineEvents {
            override fun changed()=Unit
            override fun confirmationRequired(commandId:String,exportId:String,scopeDigest:String)=Unit
            override fun protocolError(code:String)=Unit
        }
        val now=Instant.parse("2030-01-01T00:00:00Z").toEpochMilli()
        val engine=NativeCommandEngine(journal,link,events,LabFixtureCommandPolicy()){now}
        engine.ready(Binding("tenant_lab","device_lab","session_lab",1,now));engine.resume()
        val body=JSONObject().put("commandId","command_lab").put("attemptId","attempt_lab")
            .put("capability","message.send_text").put("payload",JSONObject().put("targetId","synthetic_target_001").put("text","NOT_AN_ALLOWED_FIXTURE"))
            .put("expiresAt","2030-01-01T00:02:00Z").put("policyVersion",1).put("requiresLocalConfirmation",false)
        body.put("payloadDigest",WireValidation.payloadDigest(body,"device_lab"))
        val frame=JSONObject().put("version","1.0").put("kind","command").put("eventId","event_lab")
            .put("tenantId","tenant_lab").put("deviceId","device_lab").put("sessionId","session_lab")
            .put("seq",1).put("sentAt","2030-01-01T00:00:00Z").put("mode","synthetic").put("body",body)
        engine.frame(frame)
        assertEquals(0,accepted);assertEquals(0,effects);assertFalse(online)
    }
}
