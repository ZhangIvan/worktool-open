/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.crypto.Ed25519
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.X25519
import cn.worktool.open.core.protocol.pair.PairRequest
import cn.worktool.open.core.protocol.pair.PairingClient
import cn.worktool.open.core.protocol.pair.PairingException
import cn.worktool.open.core.protocol.session.SendSequencer
import cn.worktool.open.core.protocol.session.FrameBuilder
import cn.worktool.open.core.protocol.transport.OkHttpWsTransport
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * OkHttp 网络栈冒烟测试（MockWebServer，JVM 真实 socket）：
 * v2 hello 帧经真实 WS 链路送达并被对端验签；配对/重协商 HTTP 往返。
 */
class OkHttpSmokeTest {

    private val token = Hex.decode("5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4")
    private val codec = EnvelopeCodec()

    @Test
    fun `v2 hello 经真实 WS 链路送达且可被对端逐字节验签`() {
        val server = MockWebServer()
        val helloReceived = CountDownLatch(1)
        var receivedText: String? = null
        server.enqueue(
            MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                override fun onMessage(webSocket: WebSocket, text: String) {
                    receivedText = text
                    helloReceived.countDown()
                }
            }),
        )
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)

        var now = 1_793_613_604_000L
        val clock = { now }
        val builder = FrameBuilder(
            codec = codec,
            deviceToken = { token },
            tokenVersion = { 1 },
            crypto = null,
            clock = clock,
            sequencer = SendSequencer(),
        )
        val helloText = builder.buildHello(
            deviceId = "device-9f2c7b41",
            lastEpoch = 128,
            lastReceivedSeq = 412,
            clientVersion = "0.1.0",
            encryptTypeMax = 2,
            commands = listOf(203, 204),
        )

        // 显式绕过系统代理（corp 环境下 ProxySelector 会劫持 localhost）
        val noProxyClient = okhttp3.OkHttpClient.Builder()
            .proxy(java.net.Proxy.NO_PROXY)
            .build()
        val transport = OkHttpWsTransport(noProxyClient)
        val opened = CountDownLatch(1)
        transport.connect("ws://127.0.0.1:" + server.port + "/api/v2/device-sessions/abc", object :
            cn.worktool.open.core.protocol.transport.TransportListener {
            override fun onOpen() = opened.countDown()
            override fun onMessage(text: String) = Unit
            override fun onFailure(t: Throwable?) = Unit
            override fun onClosed(code: Int, reason: String) = Unit
        })
        assertTrue(opened.await(5, TimeUnit.SECONDS))
        assertTrue(transport.send(helloText))
        assertTrue(helloReceived.await(5, TimeUnit.SECONDS))
        server.shutdown()

        // 对端（= 服务端同构校验）重算：hello 为空载荷帧，payloadDigest = SHA-256(空)
        val helloJson = Json.parseToJsonElement(receivedText!!).jsonObject
        assertEquals("hello", helloJson["type"]!!.jsonPrimitive.content)
        val meta = helloJson["meta"]!!.jsonObject
        val digest = FrameAuth.payloadDigestOfList(null)
        assertTrue(
            FrameAuth.verifyHmac(
                token, meta["hmac"]!!.jsonPrimitive.content,
                "", meta["eventId"]!!.jsonPrimitive.content,
                meta["seq"]!!.jsonPrimitive.content.toLong(),
                meta["ts"]!!.jsonPrimitive.content.toLong(),
                meta["nonce"]!!.jsonPrimitive.content,
                digest,
            ),
        )
    }

    @Test
    fun `配对 POST 请求-响应往返（夹具数据）`() {
        val server = MockWebServer()
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setHeader("Content-Type", "application/json")
                .setBody(FixtureFiles.text("pair-response.valid.json")),
        )
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)
        val client = PairingClient("http://127.0.0.1:" + server.port)

        val request = PairRequest(
            pairingCode = "482913",
            deviceId = "device-9f2c7b41",
            deviceName = "lab-main",
            ed25519PublicKeyHex = "67f1dc5eb7b5c4ea807c3c4bf86b704e646200ebb5505cac939bc94883c02f3c",
            x25519EphemeralPublicKeyHex = "7a6f28f12b1ebc96c379eb3e634e359c380c7a5ff139f11ee47140bb2774189c",
        )
        val response = client.pair(request)
        assertEquals("5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80", response.sessionId)
        assertEquals(1, response.tokenVersion)
        assertEquals(15, response.heartbeatIntervalSeconds)
        assertEquals(45, response.sessionTimeoutSeconds)
        assertEquals(30, response.rotationIntervalDays)
        assertEquals("/api/v2/device-sessions/5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80", response.wsPath)

        val recorded = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("/api/v2/pair", recorded.path)
        assertEquals(
            "482913",
            Json.parseToJsonElement(recorded.body.readUtf8()).jsonObject["pairingCode"]!!.jsonPrimitive.content,
        )
        server.shutdown()
    }

    @Test
    fun `配对码无效 403 映射 PAIRING_CODE_INVALID`() {
        val server = MockWebServer()
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"PAIRING_CODE_INVALID"}"""))
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)
        val client = PairingClient("http://127.0.0.1:" + server.port)
        try {
            client.pair(
                PairRequest(
                    pairingCode = "000000",
                    deviceId = "device-9f2c7b41",
                    ed25519PublicKeyHex = "67f1dc5eb7b5c4ea807c3c4bf86b704e646200ebb5505cac939bc94883c02f3c",
                    x25519EphemeralPublicKeyHex = "7a6f28f12b1ebc96c379eb3e634e359c380c7a5ff139f11ee47140bb2774189c",
                ),
            )
            fail("403 必须抛 PairingException")
        } catch (e: PairingException) {
            assertEquals("PAIRING_CODE_INVALID", e.errorCode)
            assertEquals(403, e.httpCode)
        }
        server.shutdown()
    }

    @Test
    fun `非法配对请求本地校验拒绝（不发网络）`() {
        val client = PairingClient("http://localhost:1/")
        try {
            client.pair(
                PairRequest(
                    pairingCode = "12ab!",
                    deviceId = "device-9f2c7b41",
                    ed25519PublicKeyHex = "67f1dc5eb7b5c4ea807c3c4bf86b704e646200ebb5505cac939bc94883c02f3c",
                    x25519EphemeralPublicKeyHex = "7a6f28f12b1ebc96c379eb3e634e359c380c7a5ff139f11ee47140bb2774189c",
                ),
            )
            fail("非法请求必须本地拒绝")
        } catch (e: PairingException) {
            assertTrue(e.message!!.contains("本地"))
        }
    }

    @Test
    fun `重协商签名可被对端验证`() {
        val server = MockWebServer()
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setHeader("Content-Type", "application/json")
                .setBody(FixtureFiles.text("pair-response.valid.json")),
        )
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)

        val signingPair = Ed25519.generateKeyPair()
        val client = PairingClient("http://127.0.0.1:" + server.port)
        val response = client.renegotiate("device-9f2c7b41", signingPair.private)
        assertEquals("5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80", response.sessionId)

        // 服务端视角：请求体携带 deviceId + 嵌套 renegotiate{ts,nonce,signature}（服务端冻结
        // 契约 v2.go renegotiateProof），签名串 = deviceId|ts|nonce
        val recorded = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("/api/v2/device-sessions", recorded.path)
        val body = Json.parseToJsonElement(recorded.body.readUtf8()).jsonObject
        val deviceId = body["deviceId"]!!.jsonPrimitive.content
        val proof = body["renegotiate"]!!.jsonObject
        val ts = proof["ts"]!!.jsonPrimitive.content
        val nonce = proof["nonce"]!!.jsonPrimitive.content
        val sig = Hex.decode(proof["signature"]!!.jsonPrimitive.content)
        assertTrue(
            Ed25519.verify(
                Ed25519.publicRaw(signingPair.public),
                "$deviceId|$ts|$nonce".toByteArray(Charsets.UTF_8),
                sig,
            ),
        )
        server.shutdown()
    }

    @Test
    fun `X25519 密钥对生成与 raw 公钥 hex 导出（配对载荷形态）`() {
        val pair = X25519.generateKeyPair()
        val raw = X25519.publicRaw(pair.public)
        assertEquals(64, Hex.encode(raw).length)
        assertTrue(Hex.encode(raw).matches(Regex("^[0-9a-f]{64}$")))
    }
}
