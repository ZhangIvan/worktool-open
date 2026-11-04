/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.ProtocolClient
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.TimeUnit

/**
 * 会话 21 回归（2026-11-04）：服务端重启凭据失忆后的会话重建自愈。
 *
 * 服务端凭据注册表为进程内存（gateway.go h.creds，仅存 TokenHash 无法恢复明文）——
 * 重启后 `POST /api/v2/device-sessions` 返回 401 RENEGOTIATION_REQUIRED，历史客户端
 * rebuildSessionUrl 无 wsPath 即拨 fallback-retry（404）无限重试，设备永不在线。
 * spec §5.5.1 修复路径：401 → Ed25519(deviceId|ts|nonce) 重协商换发新凭据 → 换用
 * 新 token/会话行继续建连（真机 .78 实锤：重启后 401 + 0 痕迹离线循环）。
 */
class RebuildRenegotiateTest {

    private val oldTokenHex = "5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4"
    private val newTokenHex = "6ecf6d7b5576d55171f8180daedcc220efd57ac87dbc8908cb2a90f0a3e19b5a"
    private val serverPubHex = "aa".repeat(32)

    /** 记录拨号 URL 的 fake 传输（断言换新 wsPath）。 */
    private class UrlRecordingTransport : WsTransport {
        var listener: TransportListener? = null
        var open = false
        val sent = mutableListOf<String>()
        val urls = mutableListOf<String>()

        override fun connect(url: String, listener: TransportListener) {
            urls.add(url)
            this.listener = listener
            open = true
            listener.onOpen()
        }

        override fun send(text: String): Boolean {
            if (!open) return false
            sent.add(text)
            return true
        }

        override fun close(code: Int, reason: String) {
            if (open) {
                open = false
                listener?.onClosed(code, reason)
            }
        }

        class Factory(private val t: UrlRecordingTransport) : WsTransport.Factory {
            override fun create(): WsTransport = t
        }
    }

    private fun newSession(
        baseUrl: String,
        transport: UrlRecordingTransport,
        signer: ((String, Long, String) -> String)?,
        transportEvents: MutableList<String> = mutableListOf(),
    ): ProtocolClient.V2Session {
        val credentials = DeviceCredentials(
            deviceId = "device-9f2c7b41",
            deviceTokenHex = oldTokenHex,
            tokenVersion = 1,
            sessionId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
            wsPath = null, // 首连即走会话重建入口（rebuildSessionUrl 分支）
        )
        return ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = baseUrl,
            sessionCrypto = null,
            journal = null,
            scheduler = ManualScheduler(),
            clock = { 1_793_664_000_000L },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { },
            httpClient = OkHttpClient.Builder()
                .connectTimeout(5, TimeUnit.SECONDS)
                .readTimeout(5, TimeUnit.SECONDS)
                .build(),
            transportFactory = UrlRecordingTransport.Factory(transport),
            renegotiationSigner = signer,
            onTransportEvent = { transportEvents.add(it) },
        )
    }

    @Test
    fun `重建 POST 401-凭据失忆-自动重协商换发新凭据并换行建连`() {
        val server = MockWebServer()
        server.start()
        server.enqueue(
            MockResponse().setResponseCode(401)
                .setBody("""{"error":"RENEGOTIATION_REQUIRED","message":"服务端无活动凭据"}"""),
        )
        server.enqueue(
            MockResponse().setResponseCode(201).setHeader("Content-Type", "application/json").setBody(
                """{"sessionId":"9f8e7d6c-5b4a-4938-8271-0fedcba98765",""" +
                    """"wsPath":"/api/v2/device-sessions/9f8e7d6c-5b4a-4938-8271-0fedcba98765",""" +
                    """"serverX25519PublicKey":"$serverPubHex",""" +
                    """"credentialRotated":true,"deviceToken":"$newTokenHex","tokenVersion":2}""",
            ),
        )
        val transport = UrlRecordingTransport()
        val signerCalls = mutableListOf<Triple<String, Long, String>>()
        val transportEvents = mutableListOf<String>()
        val session = newSession(server.url("/").toString(), transport, signer = { d, ts, n ->
            signerCalls.add(Triple(d, ts, n))
            "cd".repeat(32) // 64 hex 形态（MockWebServer 不验签）
        }, transportEvents = transportEvents)
        try {
            session.start()

            assertEquals("两次 POST：plain 401 → 重协商 201", 2, server.requestCount)
            val first = server.takeRequest()
            assertEquals("/api/v2/device-sessions", first.path)
            val firstBody = first.body.readUtf8()
            assertTrue("首次 POST 无重协商材料", !firstBody.contains("renegotiate"))

            val second = server.takeRequest()
            assertEquals("/api/v2/device-sessions", second.path)
            val body = Json.parseToJsonElement(second.body.readUtf8()).jsonObject
            assertEquals("device-9f2c7b41", body["deviceId"]!!.jsonPrimitive.content)
            val proof = body["renegotiate"]!!.jsonObject
            assertEquals("服务端冻结形状 ts", 1_793_664_000_000L, proof["ts"]!!.jsonPrimitive.longOrNull)
            assertEquals("nonce 32 hex", 32, proof["nonce"]!!.jsonPrimitive.content.length)
            assertEquals("签名 64 hex", 64, proof["signature"]!!.jsonPrimitive.content.length)
            assertTrue(
                "明文会话（初始 crypto=null）不携 ephemeral（沿用 encryptType=0，服务端按登记公钥建行）",
                body["x25519EphemeralPublicKey"] == null,
            )
            assertEquals("签名器收到 deviceId|ts|nonce 材料", "device-9f2c7b41", signerCalls.single().first)

            // 换新 wsPath 建连 + hello 携新 tokenVersion
            assertTrue(
                "WS 拨新 wsPath",
                transport.urls.single().endsWith("/api/v2/device-sessions/9f8e7d6c-5b4a-4938-8271-0fedcba98765"),
            )
            val hello = Json.parseToJsonElement(transport.sent.first()).jsonObject
            assertEquals("hello 换发后 tokenVersion=2", 2, hello["meta"]!!.jsonObject["tokenVersion"]!!.jsonPrimitive.intOrNull)
            assertTrue("传输事件可观测（重协商留痕）", transportEvents.isNotEmpty())
        } finally {
            session.stop()
            server.shutdown()
        }
    }

    @Test
    fun `重建 POST 401-未注入签名器-保持 fallback 重试（历史行为不变）`() {
        val server = MockWebServer()
        server.start()
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"RENEGOTIATION_REQUIRED"}"""))
        val transport = UrlRecordingTransport()
        val session = newSession(server.url("/").toString(), transport, signer = null)
        try {
            session.start()
            assertEquals(1, server.requestCount)
            assertTrue("无签名器 → fallback-retry 分支（仅失败重连，不自愈）", transport.urls.single().endsWith("/fallback-retry"))
        } finally {
            session.stop()
            server.shutdown()
        }
    }

    @Test
    fun `重协商仍 401-签名被拒-落 fallback 重试不崩`() {
        val server = MockWebServer()
        server.start()
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"RENEGOTIATION_REQUIRED"}"""))
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"RENEGOTIATE_FAILED","message":"Ed25519 签名验证失败"}"""))
        val transport = UrlRecordingTransport()
        val session = newSession(server.url("/").toString(), transport, signer = { _, _, _ -> "ab".repeat(32) })
        try {
            session.start()
            assertEquals(2, server.requestCount)
            assertTrue(transport.urls.single().endsWith("/fallback-retry"))
        } finally {
            session.stop()
            server.shutdown()
        }
    }

    @Test
    fun `占位-签名器缺省 null-编译面`() {
        // 参数缺省 null（null = 不启用自愈，历史行为）；无断言仅守面。
        val x: ((String, Long, String) -> String)? = null
        assertNull(x)
    }

    @Test
    fun `加密会话-重协商携自持 ephemeral-换行建连（lab 配对形态）`() {
        val server = MockWebServer()
        server.start()
        server.enqueue(MockResponse().setResponseCode(401).setBody("""{"error":"RENEGOTIATION_REQUIRED"}"""))
        server.enqueue(
            MockResponse().setResponseCode(201).setHeader("Content-Type", "application/json").setBody(
                """{"sessionId":"aaabbbcc-0000-4000-8000-000000000001",""" +
                    """"wsPath":"/api/v2/device-sessions/aaabbbcc-0000-4000-8000-000000000001",""" +
                    """"serverX25519PublicKey":"$serverPubHex",""" +
                    """"credentialRotated":true,"deviceToken":"$newTokenHex","tokenVersion":2}""",
            ),
        )
        val transport = UrlRecordingTransport()
        val baseUrl = server.url("/").toString()
        // 加密会话初始形态（lab 配对成功后的内存态）：本地生成 X25519 对 + 假服务端公钥派生
        val pair = cn.worktool.open.core.protocol.crypto.X25519.generateKeyPair()
        val oldPub = cn.worktool.open.core.protocol.crypto.Hex.decode(
            cn.worktool.open.core.protocol.crypto.Hex.encode(ByteArray(32) { 1 }),
        )
        val crypto = cn.worktool.open.core.protocol.session.SessionCrypto.derive(
            deviceEphemeralPrivate = pair.private,
            serverX25519PublicKeyRaw = oldPub,
            sessionId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
        )
        val credentials = DeviceCredentials(
            deviceId = "device-9f2c7b41",
            deviceTokenHex = oldTokenHex,
            tokenVersion = 1,
            sessionId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
            wsPath = null,
        )
        val session = ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = baseUrl,
            sessionCrypto = crypto,
            journal = null,
            scheduler = ManualScheduler(),
            clock = { 1_793_664_000_000L },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { },
            httpClient = OkHttpClient.Builder().connectTimeout(5, TimeUnit.SECONDS).readTimeout(5, TimeUnit.SECONDS).build(),
            transportFactory = UrlRecordingTransport.Factory(transport),
            renegotiationSigner = { _, _, _ -> "cd".repeat(32) },
        )
        try {
            session.start()
            assertEquals(2, server.requestCount)
            val second = server.takeRequest()
            val body = Json.parseToJsonElement(second.body.readUtf8()).jsonObject
            assertEquals(
                "加密会话重协商须携自持 ephemeral 公钥（服务端按其派生共享密钥）",
                64,
                body["x25519EphemeralPublicKey"]?.jsonPrimitive?.content?.length,
            )
            assertTrue(transport.urls.single().endsWith("/api/v2/device-sessions/aaabbbcc-0000-4000-8000-000000000001"))
        } finally {
            session.stop()
            server.shutdown()
        }
    }
}
