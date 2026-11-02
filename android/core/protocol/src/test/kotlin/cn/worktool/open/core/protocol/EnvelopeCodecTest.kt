/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** 信封编解码器测试：v1/v2 判别、全字段保真（含待冻结字段）、裸心跳/td 帧、错误分支。 */
class EnvelopeCodecTest {

    private val codec = EnvelopeCodec()

    // ---- 形态判别 ----

    @Test
    fun `meta 对象判为 v2-字符串判为 v1`() {
        val v2 = codec.decode(FixtureFiles.text("command-envelope.plain.json"))
        assertTrue(v2 is WireMessage.V2)
        val v1 = codec.decode(FixtureFiles.text("wire-v1-envelope.command-203.json"))
        assertTrue(v1 is WireMessage.V1)
        assertEquals("apifox-demo", (v1 as WireMessage.V1).envelope.meta)
    }

    @Test
    fun `裸心跳与 td 建连帧`() {
        val hb = codec.decode("""{"type":11}""")
        assertEquals(MessageTypes.HEART_BEAT, (hb as WireMessage.BareHeartbeat).type)
        val td = codec.decode("""{"td":1793613600000}""")
        assertEquals(1793613600000L, (td as WireMessage.ConnectionHello).td)
        assertEquals("""{"td":1793613600000}""", codec.connectionHelloJson(1793613600000L))
        assertEquals("""{"type":11}""", codec.bareHeartbeatJson())
        // 非法裸形态
        try {
            codec.decode("""{"type":"11"}""")
            fail("type 必须是整数")
        } catch (_: EnvelopeDecodeException) {
        }
        try {
            codec.decode("""{"td":"x"}""")
            fail("td 必须是整数")
        } catch (_: EnvelopeDecodeException) {
        }
    }

    // ---- v1 全字段与未知字段保真 ----

    @Test
    fun `v1 未知字段与待冻结字段往返保真`() {
        val decoded = codec.decode(FixtureFiles.text("wire-v1-envelope.command-203.json"))
        val env = (decoded as WireMessage.V1).envelope
        val bean = env.list[0]
        // 待冻结候选字段（spec §4.4）保留在 extras
        assertEquals("外部群测试群", bean.extraString("targetRoom"))
        assertEquals("您好，WorkTool-Open 协议测试消息", bean.extraString("receivedContent"))
        assertEquals(203, bean.type)
        assertEquals(1, bean.roomType)
        // 往返
        val rt = codec.decode(codec.encodeV1(env)) as WireMessage.V1
        assertEquals(env, rt.envelope)
        assertEquals("外部群测试群", rt.envelope.list[0].extraString("targetRoom"))
    }

    @Test
    fun `v1 report-101 子结构全字段`() {
        val env = (codec.decode(FixtureFiles.text("wire-v1-envelope.report-101.json")) as WireMessage.V1).envelope
        val bean = env.list[0]
        assertEquals(101, bean.type)
        assertEquals(2, bean.roomType)
        assertEquals(1, bean.textType)
        val sub = bean.subMessageList!!.single()
        assertEquals(0, sub.sender)
        assertEquals(1, sub.textType)
        val item = sub.itemMessageList!!.single()
        assertEquals(2, item.feature)
        assertEquals("示例入站文本消息", item.text)
        assertEquals(listOf("张三"), sub.nameList)
        assertEquals(false, sub.imageRepeat)
        assertEquals(0L, sub.imageSize)
        assertEquals("", sub.image)
        assertEquals(2, env.socketType) // report-101 为消息列表帧（socketType=2）
        assertEquals(0, env.encryptType)
        assertEquals(0, env.apiSend)
    }

    @Test
    fun `v1 加密形态 encryptType=1 解码兼容`() {
        // SR-02/G2：上游 AES/CBC 默认禁用，仅保留解码兼容
        val env = V1Envelope(
            socketType = 2,
            messageId = "2026-11-02#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
            meta = "",
            apiSend = 1,
            list = emptyList(),
            listPresent = false,
            encryptedList = "deadbeef",
            encryptType = 1,
        )
        val text = codec.encodeV1(env)
        val back = (codec.decode(text) as WireMessage.V1).envelope
        assertEquals("deadbeef", back.encryptedList)
        assertEquals(1, back.encryptType)
        assertTrue(back.list.isEmpty())
        assertTrue(!back.listPresent)
        // 编码时加密形态不重复输出 list
        assertTrue(!text.substringAfter("\"encryptedList\"").substringBefore("}").contains("\"list\""))
    }

    // ---- v2 帧全字段 ----

    @Test
    fun `v2 帧全字段解码`() {
        val frame = (codec.decode(FixtureFiles.text("command-envelope.exec-result.json")) as WireMessage.V2).frame
        assertEquals(3, frame.socketType)
        assertEquals("2026-11-02#10:00:07#ddeeff00-2233-4a58-8b8b-1c2d3e4f5067", frame.messageId)
        assertEquals(2, frame.meta.ver)
        assertEquals(1_793_613_607_000L, frame.meta.ts)
        assertEquals(87L, frame.meta.seq)
        assertEquals(1, frame.meta.tokenVersion)
        val bean = frame.list[0]
        assertEquals(0, bean.errorCode)
        assertEquals(1830L, bean.timeCost)
        assertEquals(listOf("外部群测试群"), bean.successList)
        assertEquals("{\"dispatchId\":\"c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f\"}", bean.metaJson)
    }

    @Test
    fun `v2 加密帧解码`() {
        val frame = (codec.decode(FixtureFiles.text("command-envelope.encrypted.json")) as WireMessage.V2).frame
        assertEquals(2, frame.encryptType)
        assertEquals(
            "244e3fd5a4d243fca2fec1c9330de45c440e2c7808b25e25ff3375c6434d4098117d6232eaebaba28015a7bc6283a1f1",
            frame.encryptedList,
        )
        assertTrue(frame.list.isEmpty())
    }

    // ---- 错误分支 ----

    @Test
    fun `非法 JSON 与非对象根`() {
        try {
            codec.decode("")
            fail()
        } catch (_: EnvelopeDecodeException) {
        }
        try {
            codec.decode("""[1,2]""")
            fail()
        } catch (_: EnvelopeDecodeException) {
        }
    }

    @Test
    fun `缺 socketType 或 meta 形态非法`() {
        try {
            codec.decode("""{"foo":1}""")
            fail()
        } catch (_: EnvelopeDecodeException) {
        }
        try {
            codec.decode("""{"socketType":2,"meta":123}""")
            fail()
        } catch (_: EnvelopeDecodeException) {
        }
    }

    @Test
    fun `v2 meta 未知字段拒绝（additionalProperties false）`() {
        try {
            val text = FixtureFiles.text("command-envelope.plain.json")
                .replace("\"ver\": 2", "\"ver\": 2, \"extra\": 1")
            codec.decode(text)
            fail("meta 未知字段必须拒绝")
        } catch (e: EnvelopeDecodeException) {
            assertTrue(e.message!!.contains("未知字段"))
        }
    }

    @Test
    fun `v2 meta 必含字段缺失拒绝`() {
        try {
            val text = FixtureFiles.text("command-envelope.plain.json").replace("\"ver\": 2,", "")
            codec.decode(text)
            fail("缺 ver 必须拒绝")
        } catch (e: EnvelopeDecodeException) {
            assertTrue(e.message!!.contains("ver"))
        }
    }

    // ---- messageId 生成格式 ----

    @Test
    fun `messageId 格式 yyyy-MM-dd#HH-mm-ss#uuid`() {
        val id = codec.newMessageId(java.time.LocalDateTime.of(2026, 11, 2, 10, 0, 5))
        assertTrue(id, id.matches(Regex("""^2026-11-02#10:00:05#[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$""")))
    }

    @Test
    fun `v1 meta 缺省编码为空字符串（上游形态）`() {
        val env = V1Envelope(socketType = 0, list = listOf(WeworkMessageBean(type = 11)), listPresent = true)
        val text = codec.encodeV1(env)
        assertTrue(text.contains("\"meta\":\"\""))
        val back = (codec.decode(text) as WireMessage.V1).envelope
        assertEquals("", back.meta)
        assertNull(back.messageId)
    }
}
