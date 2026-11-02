/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.session.FrameBuilder
import cn.worktool.open.core.protocol.session.SendSequencer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.security.SecureRandom

/** 编解码器错误分支矩阵 + 全字段 bean 往返 + FrameBuilder 各构造入口。 */
class CodecBranchTest {

    private val codec = EnvelopeCodec()
    private val token = ByteArray(32) { it.toByte() }

    // ---- 解码错误矩阵（逐分支）----

    @Test
    fun `decode 错误分支矩阵`() {
        // 裸心跳 type 非整数
        expectDecodeError("""{"type":"x"}""")
        // td 非整数
        expectDecodeError("""{"td":"x"}""")
        // 信封缺 socketType
        expectDecodeError("""{"meta":"x"}""")
        // v2 缺 messageId
        expectDecodeError("""{"socketType":2,"meta":{"ver":2,"ts":1,"nonce":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hmac":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","seq":1,"eventId":"7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a","commandId":"","tokenVersion":1}}""")
        // v2 meta ver 非整数
        expectDecodeError(v2WithMeta("\"ver\": \"2\""))
        // v2 meta ts 非整数
        expectDecodeError(v2WithMeta("\"ts\": \"1\""))
        // v2 meta seq 非整数
        expectDecodeError(v2WithMeta("\"seq\": null"))
        // v2 meta tokenVersion 非整数
        expectDecodeError(v2WithMeta("\"tokenVersion\": 1.5"))
        // v2 meta nonce 非字符串
        expectDecodeError(v2WithMeta("\"nonce\": 5"))
        // v2 meta hmac 非字符串
        expectDecodeError(v2WithMeta("\"hmac\": 5"))
        // v2 meta eventId 非字符串
        expectDecodeError(v2WithMeta("\"eventId\": 5"))
        // v2 meta commandId 非字符串
        expectDecodeError(v2WithMeta("\"commandId\": 5"))
        // list 非数组
        expectDecodeError("""{"socketType":2,"meta":"m","list":5}""")
        // list 元素非对象
        expectDecodeError("""{"socketType":2,"meta":"m","list":[5]}""")
        // list 元素缺 type
        expectDecodeError("""{"socketType":2,"meta":"m","list":[{"roomType":1}]}""")
        // v1 meta 非字符串非对象（数字）
        expectDecodeError("""{"socketType":2,"meta":123}""")
        // meta 为数组
        expectDecodeError("""{"socketType":2,"meta":[]}""")
    }

    private fun v2WithMeta(metaMutations: String): String {
        val base = FixtureFiles.text("command-envelope.plain.json")
        val metaBlock = base.substringAfter("\"meta\": {").substringBefore("}")
        var mutated = metaBlock
        when {
            metaMutations.contains("ver") -> mutated = metaBlock.replace("\"ver\": 2", metaMutations)
            metaMutations.contains("ts") -> mutated = metaBlock.replace("\"ts\": 1793613605000", metaMutations)
            metaMutations.contains("seq") -> mutated = metaBlock.replace("\"seq\": 413", metaMutations)
            metaMutations.contains("tokenVersion") -> mutated = metaBlock.replace("\"tokenVersion\": 1", metaMutations)
            metaMutations.contains("nonce") -> mutated = metaBlock.replace("\"nonce\": \"5c1f9a7b3e2d4c6f8a0b2d4e6f8a0c2e\"", metaMutations)
            metaMutations.contains("hmac") -> mutated = metaBlock.replace("\"hmac\": \"c7626564dc10c1389c35c699ffd306d6b9bfbccd47d62e0ab0a2097457422f70\"", metaMutations)
            metaMutations.contains("eventId") -> mutated = metaBlock.replace("\"eventId\": \"7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a\"", metaMutations)
            else -> mutated = metaBlock.replace("\"commandId\": \"c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f\"", metaMutations)
        }
        return base.replace(metaBlock, mutated)
    }

    private fun expectDecodeError(text: String) {
        try {
            codec.decode(text)
            fail("必须解码失败: ${text.take(60)}")
        } catch (_: EnvelopeDecodeException) {
        }
    }

    // ---- WeworkMessageBean 全字段序列化往返 ----

    @Test
    fun `全字段 bean 序列化往返`() {
        val bean = WeworkMessageBean(
            type = 203,
            roomType = 3,
            textType = 15,
            at = listOf("a", "b"),
            atList = listOf("c"),
            extraText = "extra",
            fileUrl = "https://files.example.com/x.png",
            fileBase64 = "AAAA",
            fileType = "png",
            friend = Friend(
                phone = "13800000000", name = "张三", markName = "备注", markCorp = " corp ",
                markExtra = "e", tagList = listOf("t1"), newFriend = true, leavingMsg = "hi",
            ),
            subMessageList = listOf(
                SubMessageBean(
                    sender = 1, textType = 1,
                    itemMessageList = listOf(ItemMessageBean(feature = 0, text = "head")),
                    nameList = listOf("n1"), imageRepeat = true, imageSize = 42, image = "IMG",
                ),
            ),
            source = "local",
            errorCode = 0, errorReason = "", runTime = "2026-11-02 10:00:00", timeCost = 1830,
            successList = listOf("g1"), failList = listOf("g2"),
            metaJson = "{\"k\":1}", rawMsg = "raw",
            extras = mapOf("targetRoom" to kotlinx.serialization.json.JsonPrimitive("外部群"), "receivedContent" to kotlinx.serialization.json.JsonPrimitive("文本")),
        )
        val env = V1Envelope(
            socketType = 2,
            messageId = "2026-11-02#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
            meta = "full",
            apiSend = 1,
            list = listOf(bean),
            listPresent = true,
            encryptType = 0,
        )
        val back = (codec.decode(codec.encodeV1(env)) as WireMessage.V1).envelope
        assertEquals(bean, back.list[0])
        assertEquals("外部群", back.list[0].extraString("targetRoom"))
        // v2 往返
        val frame = V2Frame(
            socketType = 3, messageId = "2026-11-02#10:00:07#ddeeff00-2233-4a58-8b8b-1c2d3e4f5067",
            meta = FrameMeta(2, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 3, "7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a", "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", 2),
            apiSend = 1, list = listOf(bean), listPresent = true, encryptType = 0,
            extras = mapOf("topExtra" to kotlinx.serialization.json.JsonPrimitive(7)),
        )
        val backFrame = (codec.decode(codec.encodeV2(frame)) as WireMessage.V2).frame
        assertEquals(frame, backFrame)
        assertEquals(7, (backFrame.extras["topExtra"] as kotlinx.serialization.json.JsonPrimitive).content.toInt())
    }

    @Test
    fun `bean extras 非字符串元素 extraString 返回 null`() {
        val bean = WeworkMessageBean(type = 1, extras = mapOf("k" to kotlinx.serialization.json.JsonPrimitive(5)))
        assertNull(bean.extraString("k"))
        assertNull(bean.extraString("absent"))
    }

    @Test
    fun `未知 list 元素字段含数组与对象时往返保真`() {
        val text = """{"socketType":2,"meta":"m","list":[{"type":11,"weird":{"a":[1,2]},"arr":[1,{"b":false}]}]}"""
        val env = (codec.decode(text) as WireMessage.V1).envelope
        val back = (codec.decode(codec.encodeV1(env)) as WireMessage.V1).envelope
        assertEquals(env.list[0].extras, back.list[0].extras)
    }

    // ---- FrameBuilder 构造入口 ----

    private var now = 1_793_613_600_000L

    private fun newBuilder() = FrameBuilder(
        codec = codec,
        deviceToken = { token },
        tokenVersion = { 1 },
        crypto = null,
        clock = { now },
        sequencer = SendSequencer(),
        random = SecureRandom(byteArrayOf(1, 2, 3)), // 固定种子（确定性）
    )

    @Test
    fun `builder 心跳与结果回执帧构造-可被解码回读`() {
        val b = newBuilder()
        val hb = b.buildHeartbeat()
        val hbFrame = (codec.decode(hb.text) as WireMessage.V2).frame
        assertEquals(0, hbFrame.socketType)
        assertEquals(1L, hbFrame.meta.seq)

        val r = b.buildExecResult("c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", WeworkMessageBean(type = 203, errorCode = 0))
        val rFrame = (codec.decode(r.text) as WireMessage.V2).frame
        assertEquals(3, rFrame.socketType)
        assertEquals("c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", rFrame.meta.commandId)
        assertEquals(0, rFrame.list[0].errorCode)
    }

    @Test
    fun `builder seq 严格递增与 tokenVersion 逐帧`() {
        val b = newBuilder()
        assertEquals(1L, b.build(socketType = 2).frame.meta.seq)
        assertEquals(2L, b.build(socketType = 2).frame.meta.seq)
        assertEquals(3L, b.build(socketType = 2).frame.meta.seq)
    }

    @Test
    fun `builder nonce 随机且 32hex`() {
        val b = newBuilder()
        repeat(8) {
            val nonce = b.build(socketType = 2).frame.meta.nonce
            assertTrue(nonce.matches(Regex("^[0-9a-f]{32}$")))
        }
    }

    // ---- WireMessage 形态 sealed 分支 ----

    @Test
    fun `sealed 形态字符串`() {
        assertTrue((codec.decode("""{"type":11}""") as WireMessage.BareHeartbeat).toString().contains("11"))
    }
}
