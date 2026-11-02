package io.worktool.open.prototype.transport

import io.worktool.open.prototype.core.*
import io.worktool.open.prototype.policy.ConnectionProfile
import io.worktool.open.prototype.policy.VariantFeatures
import io.worktool.open.prototype.policy.EndpointPolicy
import io.worktool.open.prototype.policy.HttpsEndpointPolicy
import okhttp3.*
import io.worktool.open.prototype.policy.AuthorizedRoutes
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import okio.ByteString
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.time.Instant
import java.time.temporal.ChronoUnit
import java.util.UUID
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledFuture
import java.util.concurrent.TimeUnit

/** Variant-injected origin boundary; platform TLS verification where applicable, no redirects/logging. */
class OkHttpDeviceLink(private val endpointPolicy: EndpointPolicy = HttpsEndpointPolicy()) : DeviceLink {
    private val client=OkHttpClient.Builder().followRedirects(false).followSslRedirects(false)
        .connectTimeout(15,TimeUnit.SECONDS).readTimeout(20,TimeUnit.SECONDS).callTimeout(30,TimeUnit.SECONDS).build()
    private val timer=Executors.newSingleThreadScheduledExecutor { r -> Thread(r,"device-heartbeat").apply { isDaemon=true } }
    private var heartbeat: ScheduledFuture<*>?=null
    private var helloTimeout: ScheduledFuture<*>?=null
    private var socket: WebSocket?=null
    private var call: Call?=null
    private var binding: Binding?=null
    private var listener: LinkListener?=null
    private var generation=0L
    private var seq=0L
    private var received=0L
    private var offset=0L
    private var permitEpoch=endpointPolicy.budget.epoch()
    private val routes=AuthorizedRoutes(endpointPolicy)
    private fun request(endpoint:String,path:String,body:JSONObject,bearer:String?=null): Request {
        val b=Request.Builder().url(routes.url(endpoint,path))
            .post(body.toString().toRequestBody("application/json; charset=utf-8".toMediaType()))
        if(bearer!=null) b.header("Authorization","Bearer $bearer")
        return b.build()
    }
    private fun post(request:Request,epoch:Long,done:(Result<JSONObject>)->Unit):Call {
        val c=client.newCall(request)
        val callback=object:Callback {
            override fun onFailure(call:Call,e:IOException) { done(Result.failure(IllegalStateException("NETWORK_FAILURE"))) }
            override fun onResponse(call:Call,response:Response) {
                val result=runCatching { response.use { r ->
                    check(r.code==201) { "HTTP_${r.code}" }
                    val body=r.body ?: error("EMPTY_RESPONSE")
                    val source=body.source(); source.request(65537)
                    require(source.buffer.size<=65536) { "RESPONSE_TOO_LARGE" }
                    val bytes=source.buffer.readByteArray()
                    require(bytes.size<=65536) { "RESPONSE_TOO_LARGE" }
                    JSONObject(String(bytes,Charsets.UTF_8))
                } }
                if(endpointPolicy.budget.valid(epoch))done(result)
                else done(Result.failure(IllegalStateException("CLIENT_PERMIT_EXPIRED")))
            }
        }
        if(!endpointPolicy.budget.runIfValid(epoch){c.enqueue(callback)}) {
            c.cancel();done(Result.failure(IllegalStateException("CLIENT_PERMIT_EXPIRED")))
        }
        return c
    }
    @Synchronized override fun enroll(endpoint:String,enrollmentId:String,enrollmentToken:String,callback:(Result<Identity>)->Unit) {
        val request=try { request(endpoint,"/api/v1/devices/enroll",JSONObject().put("enrollmentId",enrollmentId).put("enrollmentToken",enrollmentToken).put("deviceLabel","Android foreground prototype").put("localConfirmed",true)) }
        catch (_:Exception) { callback(Result.failure(IllegalArgumentException("ORIGIN_NOT_AUTHORIZED"))); return }
        val current=generation; val epoch=endpointPolicy.budget.epoch();permitEpoch=epoch
        call=post(request,epoch) { r -> synchronized(this) {
            if(current!=generation) callback(Result.failure(IllegalStateException("PAIRING_CANCELLED")))
            else callback(r.mapCatching { Identity(endpointPolicy.authorize(endpoint),it.getString("deviceId"),it.getString("keyId"),it.getString("deviceSecret")) })
        } }
    }
    override fun connect(identity:Identity,listener:LinkListener) = connect(identity,listener,ConnectionProfile.SYNTHETIC)
    @Synchronized fun connect(identity:Identity,listener:LinkListener,profile:ConnectionProfile) {
        if(!profile.mode.allowed(VariantFeatures.ownFixtureAccessibility)) { listener.closed("VARIANT_MODE_DENIED"); return }
        disconnect(); this.listener=listener; val current=generation; permitEpoch=endpointPolicy.budget.epoch(); val epoch=permitEpoch
        val request=try { request(identity.endpoint,"/api/v1/device-sessions",JSONObject().put("deviceId",identity.deviceId).put("keyId",identity.keyId),identity.secret) }
        catch (_:Exception) { listener.closed("ORIGIN_NOT_AUTHORIZED"); return }
        call=post(request,epoch) { r -> synchronized(this) {
            if(current!=generation || !endpointPolicy.budget.valid(epoch)) return@synchronized
            if(r.isFailure) { listener.closed("SESSION_FAILED"); return@synchronized }
            try {
                val session=r.getOrThrow(); val sessionId=session.getString("sessionId")
                val wsRequest=Request.Builder().url(routes.webSocketHandshake(identity.endpoint))
                    .header("Authorization","Bearer ${session.getString("accessToken")}").build()
                val socketListener=object:WebSocketListener() {
                    override fun onOpen(ws:WebSocket,response:Response) { synchronized(this@OkHttpDeviceLink) {
                        if(current!=generation || !endpointPolicy.budget.valid(epoch)) { ws.close(1000,"superseded"); return }
                        helloTimeout=timer.schedule({ synchronized(this@OkHttpDeviceLink) { if(current==generation && binding==null) { disconnect(); listener.closed("HELLO_TIMEOUT") } } },5,TimeUnit.SECONDS)
                        var helloSent=false
                        endpointPolicy.budget.runIfValid(epoch){helloSent=ws.send(profile.hello().toString())}
                        if(!helloSent){disconnect();listener.closed("CLIENT_PERMIT_EXPIRED")}
                    } }
                    override fun onMessage(ws:WebSocket,text:String) { synchronized(this@OkHttpDeviceLink) {
                        if(current!=generation) return
                        try {
                            val decodedBytes=text.toByteArray(Charsets.UTF_8).size
                            require(endpointPolicy.budget.inboundFrame(epoch,decodedBytes)) { "CLIENT_FRAME_BUDGET_DENIED" }
                            require(decodedBytes<=65536)
                            val j=JSONObject(text)
                            if(binding==null) {
                                WireValidation.exact(j,setOf("kind","version","tenantId","deviceId","sessionId","policyVersion","serverTime","heartbeatIntervalSec","maxFrameBytes","mode"))
                                require(j.getString("kind")=="hello_ack" && j.getString("version")==profile.mode.wireVersion && j.getString("mode")=="synthetic")
                                WireValidation.id(j,"tenantId");WireValidation.id(j,"deviceId");WireValidation.id(j,"sessionId");WireValidation.integer(j,"policyVersion");WireValidation.time(j,"serverTime")
                                require(j.getString("deviceId")==identity.deviceId && j.getString("sessionId")==sessionId)
                                require(j.getInt("heartbeatIntervalSec")==15 && j.getInt("maxFrameBytes")==65536)
                                val b=Binding(j.getString("tenantId"),identity.deviceId,sessionId,j.getLong("policyVersion"),Instant.parse(j.getString("serverTime")).toEpochMilli(),profile.mode.wireVersion)
                                require(b.policyVersion>0); helloTimeout?.cancel(false); helloTimeout=null; offset=b.serverTimeMs-System.currentTimeMillis(); binding=b
                                listener.ready(b)
                                heartbeat=timer.scheduleAtFixedRate({ synchronized(this@OkHttpDeviceLink) { if(current==generation) send("heartbeat",JSONObject().put("policyVersion",b.policyVersion).put("lastReceivedSeq",received)) } },15,15,TimeUnit.SECONDS)
                            } else {
                                val b=binding!!
                                require(j.getString("version")==b.protocolVersion)
                                require(j.getString("tenantId")==b.tenantId && j.getString("deviceId")==b.deviceId && j.getString("sessionId")==b.sessionId)
                                received=maxOf(received,j.getLong("seq")); listener.frame(j)
                            }
                        } catch (_:Exception) { disconnect(); listener.closed("PROTOCOL_ERROR") }
                    } }
                    override fun onMessage(ws:WebSocket,bytes:ByteString) { end("BINARY_FRAME_DENIED") }
                    override fun onClosing(ws:WebSocket,code:Int,reason:String) { ws.close(code,null) }
                    override fun onClosed(ws:WebSocket,code:Int,reason:String) { end("CONNECTION_CLOSED") }
                    override fun onFailure(ws:WebSocket,t:Throwable,response:Response?) { end("CONNECTION_FAILED") }
                    private fun end(code:String) { synchronized(this@OkHttpDeviceLink) { if(current==generation) { disconnect(); listener.closed(code) } } }
                }
                if(!endpointPolicy.budget.runIfValid(epoch){socket=client.newWebSocket(wsRequest,socketListener)}) listener.closed("CLIENT_PERMIT_EXPIRED")
            } catch (_:Exception) { listener.closed("SESSION_RESPONSE_INVALID") }
        } }
    }
    @Synchronized override fun send(kind:String,body:JSONObject): String? {
        val b=binding ?: return null
        if(!endpointPolicy.budget.valid(permitEpoch)){disconnect();return null}
        require(kind in setOf("accepted","started","execution_result","heartbeat","device_event","transport_ack"))
        val eventId="evt_"+UUID.randomUUID().toString().replace("-","")
        val frame=JSONObject().put("version",b.protocolVersion).put("kind",kind).put("eventId",eventId)
            .put("tenantId",b.tenantId).put("deviceId",b.deviceId).put("sessionId",b.sessionId).put("seq",++seq)
            .put("sentAt",Instant.ofEpochMilli(System.currentTimeMillis()+offset).truncatedTo(ChronoUnit.SECONDS).toString()).put("mode","synthetic").put("body",body)
        var enqueued=false
        endpointPolicy.budget.runIfValid(permitEpoch){enqueued=socket?.send(frame.toString())==true}
        if(!enqueued) { val l=listener; disconnect(); l?.closed("SEND_FAILED"); return null }
        return eventId
    }
    @Synchronized override fun connected():Boolean = binding!=null && endpointPolicy.budget.valid(permitEpoch)
    @Synchronized fun cancelPermit(epoch:Long) {if(permitEpoch==epoch)disconnect()}
    @Synchronized override fun disconnect() { generation++; helloTimeout?.cancel(false);helloTimeout=null; heartbeat?.cancel(false); heartbeat=null; call?.cancel();call=null;socket?.cancel();socket=null;binding=null;seq=0;received=0 }
}
