/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.delivery.InMemoryJournalStore
import cn.worktool.open.core.protocol.delivery.JournalState
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.session.FrameVerifier
import cn.worktool.open.core.protocol.transport.FakeTransport
import cn.worktool.open.core.protocol.transport.ManualScheduler
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * ProtocolClient 门面接线测试（V2Session 全链路：hello → 上报帧 → 对端整链验签；
 * 入向帧 → onCommand）。传输用 fake（网络栈冒烟见 OkHttpSmokeTest）。
 */
class ProtocolClientTest {

    private val tokenHex = "5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4"
    private val credentials = DeviceCredentials(
        deviceId = "device-9f2c7b41",
        deviceTokenHex = tokenHex,
        tokenVersion = 1,
        wsPath = "/api/v2/device-sessions/5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80",
    )

    @Test
    fun `V2Session-hello 首帧与上报帧可被对端整链验签`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val journal = InMemoryJournalStore()
        var now = 1_793_613_604_000L

        val session = ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = journal,
            scheduler = scheduler,
            clock = { now },
            capabilities = ProtocolClient.Capabilities(
                clientVersion = "0.1.0",
                encryptTypeMax = 2,
                commands = listOf(203, 204),
            ),
            lastEpoch = { journal.appliedEpoch() },
            lastReceivedSeq = { 0L },
            onCommand = { _, _, _ -> },
            onRejected = { },
            transportFactory = FakeTransport.Factory(transport),
        )
        session.start()

        // hello 首帧（seq=1）
        val hello = Json.parseToJsonElement(transport.sent.first()).jsonObject
        assertEquals("hello", hello["type"]!!.jsonPrimitive.content)
        assertEquals(1L, hello["meta"]!!.jsonObject["seq"]!!.jsonPrimitive.content.toLong())

        // 上报帧经 AtLeastOnce 队列（先落盘）发送（seq=2）
        now += 1_000
        val sent = session.sendReport(
            WeworkMessageBean(type = MessageTypes.TYPE_RECEIVE_MESSAGE_LIST, roomType = 2, textType = 1),
        )
        assertTrue(sent)
        assertEquals(1, journal.entries().size) // 先落盘语义
        // journal 条目 = 帧自身 eventId（服务端按 eventId 幂等去重）
        val reportMeta = Json.parseToJsonElement(transport.sent[1]).jsonObject["meta"]!!.jsonObject
        assertEquals(reportMeta["eventId"]!!.jsonPrimitive.content, journal.entries().first().eventId)
        assertEquals(2, transport.sent.size)

        // 对端（服务端同构）验签：hello（空载荷帧，HMAC 直接复算 + schema 校验）与
        // 上报帧（信封形态走 FrameVerifier 整链）
        val helloMeta = hello["meta"]!!.jsonObject
        val helloValid = cn.worktool.open.core.protocol.validation.SessionHelloValidator.validate(hello)
        assertTrue("hello schema: $helloValid", helloValid.isEmpty())
        val helloHmacOk = cn.worktool.open.core.protocol.crypto.FrameAuth.verifyHmac(
            Hex.decode(tokenHex),
            helloMeta["hmac"]!!.jsonPrimitive.content,
            "", helloMeta["eventId"]!!.jsonPrimitive.content,
            helloMeta["seq"]!!.jsonPrimitive.content.toLong(),
            helloMeta["ts"]!!.jsonPrimitive.content.toLong(),
            helloMeta["nonce"]!!.jsonPrimitive.content,
            cn.worktool.open.core.protocol.crypto.FrameAuth.payloadDigestOfList(null),
        )
        assertTrue("hello 帧逐字节验签", helloHmacOk)
        val serverVerifier = FrameVerifier(
            codec = EnvelopeCodec(),
            deviceToken = { Hex.decode(tokenHex) },
            tokenVersion = { 1 },
            crypto = null,
            clock = { now },
        )
        val v2 = serverVerifier.verify(transport.sent[1])
        assertTrue("上报帧验签: $v2", v2 is FrameVerifier.Verification.Accepted)
        assertEquals(1, journal.entries().count { it.state != JournalState.DONE }) // 未 ack → 待发
        session.stop()
    }

    @Test
    fun `V2Session-入向帧验签通过回调 onCommand`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val now = 1_793_613_604_000L
        val received = mutableListOf<Int>()

        val session = ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = null,
            scheduler = scheduler,
            clock = { now },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { frame, _, dup -> if (!dup) received.add(frame.list.first().type) },
            onRejected = { },
            transportFactory = FakeTransport.Factory(transport),
        )
        session.start()

        // 服务端下发：seq=1 指令帧（用对端构造器同构生成）
        val serverBuilder = cn.worktool.open.core.protocol.session.FrameBuilder(
            codec = EnvelopeCodec(),
            deviceToken = { Hex.decode(tokenHex) },
            tokenVersion = { 1 },
            crypto = null,
            clock = { now },
            sequencer = cn.worktool.open.core.protocol.session.SendSequencer(),
        )
        val command = serverBuilder.build(
            socketType = 2,
            commandId = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f",
            apiSend = 1,
            list = listOf(WeworkMessageBean(type = 203, roomType = 1)),
        )
        transport.serverMessage(command.text)
        assertEquals(listOf(203), received)
        session.stop()
    }

    @Test
    fun `V2Session-鉴权失败帧触发 onRejected 并断连`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val now = 1_793_613_604_000L
        val rejectedRules = mutableListOf<String>()

        val session = ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = null,
            scheduler = scheduler,
            clock = { now },
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            onCommand = { _, _, _ -> },
            onRejected = { rejectedRules.add(it.rule) },
            transportFactory = FakeTransport.Factory(transport),
        )
        session.start()

        // 非法 JSON → 4400 → onRejected + 断连
        transport.serverMessage("!!!not-json!!!")
        assertEquals(listOf("format"), rejectedRules)
        // stop 已由门面自动执行（鉴权类失败关闭会话）；FakeTransport.open == false
        assertTrue(!transport.open)
    }
}
