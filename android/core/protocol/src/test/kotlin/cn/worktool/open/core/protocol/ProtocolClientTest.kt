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
            // 服务端计数器已消费 hello（seq=1，python 规范客户端 send_hello 同款
            // in_seq=1 预置）→ 上报帧期望 seq=2（hello 后帧连续 +1，spec §5.4.2）
            sequencer = cn.worktool.open.core.protocol.session.SessionSequencer().apply { resetForReconnect(1) },
        )
        val v2 = serverVerifier.verify(transport.sent[1])
        assertTrue("上报帧验签: $v2", v2 is FrameVerifier.Verification.Accepted)
        assertEquals(1, journal.entries().count { it.state == JournalState.DONE }) // 发送成功即 DONE（H1：不再依赖 ack）
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
    fun `V2Session-断线重连 flushPending 重放帧按同 eventId 重建`() {
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
            capabilities = ProtocolClient.Capabilities("0.1.0", 2, listOf(203)),
            lastEpoch = { journal.appliedEpoch() },
            lastReceivedSeq = { 0L },
            onCommand = { _, _, _ -> },
            onRejected = { },
            transportFactory = FakeTransport.Factory(transport),
        )
        session.start()
        // H1 DONE 化后重放语义：只有**发送失败**的条目才留在 journal 待重放——
        // 先断线再入队（发送失败 → 条目保持未完成），重连 flushPending 补发。
        transport.close(1006, "net-gone")
        // 结果回执(seq=2，经 AtLeastOnce 先落盘；发送失败不上网)
        assertTrue(session.sendExecResult("cmd-replay", WeworkMessageBean(type = 203, errorCode = 0)))
        assertEquals(1, journal.entries().size)
        assertTrue(
            "发送失败条目保持未完成（待重放）",
            journal.entries().single().state != JournalState.DONE,
        )
        // 固化帧文本 = journal 落盘内容（首投未上网，原样文本仅存 journal）
        val original = Json.parseToJsonElement(journal.entries().single().frameText).jsonObject
        val origMeta = original["meta"]!!.jsonObject

        // 断线 60s（越过 ts±30s 与 nonce 5min 窗口）→ 重连：hello seq 恒 1（计数器
        // 在构造 hello 前复位），flushPending 重放 = 同 eventId 重建信封（新 nonce/ts/seq
        // + 重算 HMAC）——原样重发固化帧文本会被 4403/4402/4405+903 拒收（CR T34-01）
        now += 60_000
        scheduler.advance(10_000) // 退避重连触发（greeting → onConnected → flushPending）

        assertEquals("hello + 重连 hello + 重放帧 = 3 帧", 3, transport.sent.size)
        val hello2 = Json.parseToJsonElement(transport.sent[1]).jsonObject
        assertEquals(1L, hello2["meta"]!!.jsonObject["seq"]!!.jsonPrimitive.content.toLong())
        val rebuilt = Json.parseToJsonElement(transport.sent[2]).jsonObject
        val rebuiltMeta = rebuilt["meta"]!!.jsonObject

        assertEquals("重放帧 eventId 须与原帧一致（§5.4.3 去重键）", origMeta["eventId"]!!, rebuiltMeta["eventId"]!!)
        assertEquals("载荷逐值一致（同 payloadDigest）", original["list"]!!, rebuilt["list"]!!)
        assertTrue(
            "nonce 须新鲜",
            origMeta["nonce"]!!.jsonPrimitive.content != rebuiltMeta["nonce"]!!.jsonPrimitive.content,
        )
        assertEquals("重放帧 seq = hello(1) 后下一帧序", 2L, rebuiltMeta["seq"]!!.jsonPrimitive.content.toLong())
        assertTrue(
            "重放帧 HMAC 按新信封重算",
            cn.worktool.open.core.protocol.crypto.FrameAuth.verifyHmac(
                Hex.decode(tokenHex),
                rebuiltMeta["hmac"]!!.jsonPrimitive.content,
                "cmd-replay",
                rebuiltMeta["eventId"]!!.jsonPrimitive.content,
                2L, now,
                rebuiltMeta["nonce"]!!.jsonPrimitive.content,
                cn.worktool.open.core.protocol.crypto.FrameAuth.payloadDigestOfList(rebuilt["list"]),
            ),
        )
        session.stop()
    }

    @Test
    fun `V2Session-鉴权失败帧触发 onRejected 并断连`() {        val transport = FakeTransport()
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
