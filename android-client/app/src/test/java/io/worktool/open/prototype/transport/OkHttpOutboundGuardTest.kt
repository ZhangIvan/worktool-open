package io.worktool.open.prototype.transport

import io.worktool.open.prototype.core.*
import okhttp3.Request
import okhttp3.WebSocket
import okio.ByteString
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** Actual send/disconnect wrapper, injected in-memory socket. No connect, HTTP, listener or network. */
class OkHttpOutboundGuardTest {
    private class Socket:WebSocket {
        val frames=mutableListOf<String>();var accept=true;var cancelled=false
        override fun request()=Request.Builder().url("https://example.invalid").build()
        override fun queueSize()=0L
        override fun send(text:String):Boolean {if(accept)frames.add(text);return accept}
        override fun send(bytes:ByteString)=false
        override fun close(code:Int,reason:String?)=true
        override fun cancel(){cancelled=true}
    }
    private class Listener:LinkListener {
        val closes=mutableListOf<String>()
        override fun ready(binding:Binding)=Unit
        override fun frame(value:JSONObject)=Unit
        override fun closed(reasonCode:String){closes.add(reasonCode)}
    }
    private fun set(link:OkHttpDeviceLink,name:String,value:Any) {
        link.javaClass.getDeclaredField(name).apply { isAccessible=true }.set(link,value)
    }
    private fun connected(clock:()->Long,budget:OutboundSessionBudget):Triple<OkHttpDeviceLink,Socket,Listener> {
        val link=OkHttpDeviceLink(elapsedClock=clock);val socket=Socket();val listener=Listener()
        set(link,"binding",Binding("tenant_demo","device_demo","session_demo",1,0))
        set(link,"socket",socket);set(link,"listener",listener);set(link,"outbound",budget)
        return Triple(link,socket,listener)
    }
    @Test fun returnedEventMeansActualSocketEnqueueInStrictSequence() {
        val (link,socket,listener)=connected({0},OutboundSessionBudget(0))
        val first=link.send("accepted",JSONObject());val second=link.send("started",JSONObject())
        assertEquals(2,socket.frames.size)
        assertEquals(first,JSONObject(socket.frames[0]).getString("eventId"))
        assertEquals(second,JSONObject(socket.frames[1]).getString("eventId"))
        assertEquals(1,JSONObject(socket.frames[0]).getLong("seq"));assertEquals(2,JSONObject(socket.frames[1]).getLong("seq"))
        assertTrue(listener.closes.isEmpty());link.disconnect()
    }
    @Test fun exhaustedSessionDoesNotQueueStartedOrReturnEventId() {
        val budget=OutboundSessionBudget(0);repeat(240){assertTrue(budget.take(it*1000L,100))}
        val (link,socket,listener)=connected({240000},budget)
        assertNull(link.send("started",JSONObject()))
        assertTrue(socket.frames.isEmpty());assertTrue(socket.cancelled);assertFalse(link.connected())
        assertEquals(listOf("OUTBOUND_LIMIT_MANUAL_RESUME_REQUIRED"),listener.closes)
    }
    @Test fun socketRefusalClosesBeforeLaterSequenceCanEnqueue() {
        val (link,socket,listener)=connected({0},OutboundSessionBudget(0));socket.accept=false
        assertNull(link.send("started",JSONObject()));socket.accept=true
        assertNull(link.send("execution_result",JSONObject()));assertTrue(socket.frames.isEmpty())
        assertEquals(listOf("SEND_FAILED"),listener.closes)
    }
    @Test fun fullUtf8EnvelopeCountsAgainstSingleFrameLimit() {
        val (link,socket,listener)=connected({0},OutboundSessionBudget(0))
        assertNull(link.send("execution_result",JSONObject().put("code","中".repeat(22000))))
        assertTrue(socket.frames.isEmpty());assertEquals(listOf("OUTBOUND_LIMIT_MANUAL_RESUME_REQUIRED"),listener.closes)
    }
    @Test fun heartbeatAndResultUseSameAdmissionBudget() {
        val (link,socket,listener)=connected({0},OutboundSessionBudget(0))
        repeat(4){assertNotNull(link.send("heartbeat",JSONObject()))}
        assertNull(link.send("execution_result",JSONObject()))
        assertEquals(4,socket.frames.size);assertEquals(listOf("OUTBOUND_LIMIT_MANUAL_RESUME_REQUIRED"),listener.closes)
    }
}
