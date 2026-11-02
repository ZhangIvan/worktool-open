/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.ProtocolClient
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.WireMessage
import cn.worktool.open.core.protocol.pair.PairRequest
import cn.worktool.open.core.protocol.pair.PairResponse
import cn.worktool.open.core.protocol.pair.PairingClient
import cn.worktool.open.core.protocol.pair.PairingException
import kotlinx.serialization.json.Json
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** 门面与网络栈分支：V1Compat、V2Session 无 journal/无 wsPath 变体、OkHttpWsTransport 分支、PairingClient 错误路径。 */
class FacadeBranchTest {

    // ---- V1Compat 门面 ----

    @Test
    fun `V1Compat-td 首帧-裸心跳-上报编码`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        var now = 1_000L
        val inbound = mutableListOf<WireMessage>()
        val v1 = ProtocolClient.V1Compat(
            wsUrl = "ws://127.0.0.1:8080/webserver/wework/robot-1",
            scheduler = scheduler,
            clock = { now },
            onMessage = { inbound.add(it) },
            transportFactory = FakeTransport.Factory(transport),
        )
        v1.start()
        assertEquals("""{"td":1000}""", transport.sent[0])
        scheduler.advance(5_000)
        assertEquals("""{"type":11}""", transport.sent[1])
        // 上行信封编码
        assertTrue(v1.send(cn.worktool.open.core.protocol.V1Envelope(socketType = 2, apiSend = 0, list = listOf(WeworkMessageBean(type = 101)), listPresent = true)))
        // 入向下行消息解码
        transport.serverMessage("""{"type":11}""")
        assertTrue(inbound.single() is WireMessage.BareHeartbeat)
        v1.stop()
        assertTrue(!transport.open)
    }

    // ---- V2Session 变体分支 ----

    private val tokenHex = "5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4"

    @Test
    fun `V2Session-无 wsPath 时按 sessionId 组 URL-无 journal 直发`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        var urlSeen = ""
        val session = ProtocolClient.V2Session(
            credentials = DeviceCredentials(
                deviceId = "device-9f2c7b41",
                deviceTokenHex = tokenHex,
                tokenVersion = 1,
                sessionId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
                wsPath = null, // ← 无 wsPath：走 sessionId 拼 URL 分支
            ),
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = null, // ← 无 journal：直发分支
            scheduler = scheduler,
            clock = { 1_793_613_600_000L },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { },
            transportFactory = object : WsTransport.Factory {
                override fun create(): WsTransport = transport.also {
                    urlSeen = "placeholder" // URL 在 connect 时读取
                }
            },
        )
        // 通过包装 factory 捕获 URL：直接检查 transport.connect 路径
        val urlCapture = object : WsTransport, TransportListener by NoopListener {
            var url = ""
            override fun connect(url: String, listener: TransportListener) {
                this.url = url
                listener.onOpen()
            }
            override fun send(text: String) = true
            override fun close(code: Int, reason: String) {}
        }
        val session2 = ProtocolClient.V2Session(
            credentials = DeviceCredentials(
                deviceId = "device-9f2c7b41",
                deviceTokenHex = tokenHex,
                tokenVersion = 1,
                sessionId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
                wsPath = null,
            ),
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = null,
            scheduler = scheduler,
            clock = { 1_793_613_600_000L },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { },
            transportFactory = object : WsTransport.Factory {
                override fun create(): WsTransport = urlCapture
            },
        )
        session2.start()
        assertEquals(
            "wss://example.com/api/v2/device-sessions/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
            urlCapture.url,
        )
        // 无 journal 直发分支
        assertTrue(session2.sendReport(WeworkMessageBean(type = 101)))
        // 有 wsPath 的第一实例：走 wsPath 拼 URL（FakeTransport connect 忽略 URL，不校验）
        session.start()
        session.stop()
        session2.stop()
    }

    @Test
    fun `V2Session-sendExecResult 无 journal 直发`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val session = ProtocolClient.V2Session(
            credentials = DeviceCredentials(
                deviceId = "device-9f2c7b41",
                deviceTokenHex = tokenHex,
                tokenVersion = 1,
                wsPath = "/api/v2/device-sessions/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
            ),
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = null,
            scheduler = scheduler,
            clock = { 1_793_613_600_000L },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { },
            transportFactory = FakeTransport.Factory(transport),
        )
        session.start()
        val ok = session.sendExecResult("c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", WeworkMessageBean(type = 203, errorCode = 0))
        assertTrue(ok)
        assertEquals(2, transport.sent.size) // hello + exec result
        session.stop()
    }

    // ---- OkHttpWsTransport 分支 ----

    @Test
    fun `OkHttpWsTransport-策略违例 URL 即刻 onFailure`() {
        val t = OkHttpWsTransport()
        val failed = CountDownLatch(1)
        t.connect("ws://203.0.113.9/x", object : TransportListener {
            override fun onOpen() {}
            override fun onMessage(text: String) {}
            override fun onFailure(t: Throwable?) = failed.countDown()
            override fun onClosed(code: Int, reason: String) {}
        })
        assertTrue(failed.await(3, TimeUnit.SECONDS))
    }

    @Test
    fun `OkHttpWsTransport-未连接 send 返回 false-close 幂等`() {
        val t = OkHttpWsTransport()
        assertTrue(!t.send("x"))
        t.close(1000, "noop") // 未连接 close 不抛
        val f = OkHttpWsTransport.Factory()
        assertTrue(f.create() is OkHttpWsTransport)
    }

    @Test
    fun `OkHttpWsTransport-pingInterval 正分支可建连`() {
        val server = MockWebServer()
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)
        server.enqueue(MockResponse().withWebSocketUpgrade(object : okhttp3.WebSocketListener() {}))
        val opened = CountDownLatch(1)
        OkHttpWsTransport(pingIntervalMillis = 1_000).connect(
            "ws://127.0.0.1:" + server.port + "/x",
            object : TransportListener {
                override fun onOpen() = opened.countDown()
                override fun onMessage(text: String) {}
                override fun onFailure(t: Throwable?) {}
                override fun onClosed(code: Int, reason: String) {}
            },
        )
        assertTrue(opened.await(5, TimeUnit.SECONDS))
        server.shutdown()
    }

    // ---- EndpointPolicy 边角 ----

    @Test
    fun `EndpointPolicy 边角分支`() {
        // 127/8 全段 loopback
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("ws://127.5.5.5:9/x"))
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("ws://[::1]:9/x"))
        // 大写 scheme 归一
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("WSS://Example.COM/x"))
        assertEquals(EndpointPolicy.Check.Violated::class.java, EndpointPolicy.check("WSS://[2001:db8::1]/x").javaClass)
    }

    // ---- PairingClient 错误分支 ----

    @Test
    fun `PairingClient-响应体错误分支`() {
        val server = MockWebServer()
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)
        val base = "http://127.0.0.1:" + server.port
        val client = PairingClient(base)

        fun req() = PairRequest(
            pairingCode = "482913",
            deviceId = "device-9f2c7b41",
            ed25519PublicKeyHex = "67f1dc5eb7b5c4ea807c3c4bf86b704e646200ebb5505cac939bc94883c02f3c",
            x25519EphemeralPublicKeyHex = "7a6f28f12b1ebc96c379eb3e634e359c380c7a5ff139f11ee47140bb2774189c",
        )

        // 400
        server.enqueue(MockResponse().setResponseCode(400).setBody("{}"))
        try {
            client.pair(req()); fail("400 必须抛")
        } catch (e: PairingException) {
            assertEquals(400, e.httpCode)
        }
        // 其他码
        server.enqueue(MockResponse().setResponseCode(500).setBody("boom"))
        try {
            client.pair(req()); fail("500 必须抛")
        } catch (e: PairingException) {
            assertEquals(500, e.httpCode)
        }
        // 空响应体
        server.enqueue(MockResponse().setResponseCode(200))
        try {
            client.pair(req()); fail("空体必须抛")
        } catch (e: PairingException) {
            assertTrue(e.message!!.contains("空"))
        }
        // 非对象响应体
        server.enqueue(MockResponse().setResponseCode(200).setBody("[1]"))
        try {
            client.pair(req()); fail("数组体必须抛")
        } catch (e: PairingException) {
            assertTrue(e.message!!.contains("对象"))
        }
        // 响应 schema 违例
        server.enqueue(MockResponse().setResponseCode(200).setBody("""{"deviceId":"device-9f2c7b41"}"""))
        try {
            client.pair(req()); fail("违例响应必须抛")
        } catch (e: PairingException) {
            assertTrue(e.message!!.contains("校验失败"))
        }
        server.shutdown()
    }

    @Test
    fun `PairingClient-端点策略拒绝构造`() {
        try {
            PairingClient("ws://203.0.113.9")
            fail("非 TLS 端点必须构造失败")
        } catch (e: IllegalStateException) {
            assertTrue(e.message!!.contains("plain_text_not_loopback"))
        }
    }

    @Test
    fun `Pair 模型 fromJsonElement 错误分支`() {
        try {
            PairRequest.fromJsonElement(Json.parseToJsonElement("[]")); fail()
        } catch (_: cn.worktool.open.core.protocol.EnvelopeDecodeException) {
        }
        try {
            PairRequest.fromJsonElement(Json.parseToJsonElement("""{"pairingCode":"1"}""")); fail()
        } catch (e: cn.worktool.open.core.protocol.EnvelopeDecodeException) {
            assertTrue(e.message!!.contains("deviceId"))
        }
        try {
            PairResponse.fromJsonElement(Json.parseToJsonElement("{}")); fail()
        } catch (_: cn.worktool.open.core.protocol.EnvelopeDecodeException) {
        }
        // 合法解析（全字段）
        val resp = PairResponse.fromJsonElement(
            cn.worktool.open.core.protocol.FixtureFiles.element("pair-response.valid.json"),
        )
        assertEquals(1, resp.tokenVersion)
        assertEquals(30, resp.rotationIntervalDays)
    }

    private object NoopListener : TransportListener {
        override fun onOpen() {}
        override fun onMessage(text: String) {}
        override fun onFailure(t: Throwable?) {}
        override fun onClosed(code: Int, reason: String) {}
    }
}
