/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.Friend
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.WireMessage
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.ProtocolFixtures
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.IntentNormalizer
import cn.worktool.open.feature.executor.intent.NormalizedIntent
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 意图规范化测试：**消费 protocol/ 权威夹具**（spec §7 消费契约：单测以仓库根
 * 相对路径引用 protocol/fixtures）——203 指令夹具（v1/v2 两种信封）解码后规范化
 * 必须得到与夹具字段一致的 SendText 意图；另有载荷非法/未支持类型矩阵。
 */
class IntentNormalizerTest {

    private val normalizer = IntentNormalizer(ExecutorRegistry.defaultP0().supportedTypes)
    private val codec = EnvelopeCodec()

    // ---- protocol 夹具消费（spec §7：文件名与字段冻结为测试数据接口） ----

    @Test
    fun `夹具-v1指令203信封解码规范化为SendText`() {
        val wire = codec.decode(ProtocolFixtures.text("wire-v1-envelope.command-203.json"))
        val envelope = (wire as WireMessage.V1).envelope
        assertEquals(1, envelope.list.size)

        val normalized = normalizer.normalize(envelope.list[0])

        assertTrue("夹具 203 应规范化成功：$normalized", normalized is NormalizedIntent.Valid)
        val intent = (normalized as NormalizedIntent.Valid).intent as CommandIntent.SendText
        assertEquals(MessageTypes.SEND_MESSAGE, intent.commandType)
        assertEquals("外部群测试群", intent.targetRoom)
        assertEquals("您好，WorkTool-Open 协议测试消息", intent.content)
        assertTrue("夹具无 at/atList", intent.atList.isEmpty())
    }

    @Test
    fun `夹具-v2明文指令203信封解码规范化一致`() {
        val wire = codec.decode(ProtocolFixtures.text("command-envelope.plain.json"))
        val frame = (wire as WireMessage.V2).frame
        val normalized = normalizer.normalize(frame.list[0])

        assertTrue(normalized is NormalizedIntent.Valid)
        val intent = (normalized as NormalizedIntent.Valid).intent as CommandIntent.SendText
        assertEquals("外部群测试群", intent.targetRoom)
        assertEquals("您好，WorkTool-Open 协议测试消息", intent.content)
    }

    // ---- 203 载荷校验 ----

    @Test
    fun `203-缺目标会话或缺正文均Invalid`() {
        val noTarget = bean(203, extras = mapOf("receivedContent" to JsonPrimitive("hi")))
        assertTrue(normalizer.normalize(noTarget) is NormalizedIntent.Invalid)

        val noContent = bean(203, extras = mapOf("targetRoom" to JsonPrimitive("群")))
        assertTrue(normalizer.normalize(noContent) is NormalizedIntent.Invalid)
    }

    @Test
    fun `203-atList规范化入意图`() {
        val b = bean(
            203,
            extras = mapOf(
                "targetRoom" to JsonPrimitive("外部群测试群"),
                "receivedContent" to JsonPrimitive("通知"),
            ),
            atList = listOf("张三", "李四"),
        )
        val intent = normalizer.normalize(b) as NormalizedIntent.Valid
        assertEquals(
            listOf("张三", "李四"),
            (intent.intent as CommandIntent.SendText).atList,
        )
    }

    // ---- 206/207/225 ----

    @Test
    fun `206-群名必填成员可选`() {
        val ok = bean(
            206,
            extras = mapOf(
                "groupName" to JsonPrimitive("客户交付群"),
                "groupMembers" to JsonArray(listOf(JsonPrimitive("张三"))),
                "announcement" to JsonPrimitive("公告"),
            ),
        )
        val intent = (normalizer.normalize(ok) as NormalizedIntent.Valid).intent as CommandIntent.InitGroup
        assertEquals("客户交付群", intent.groupName)
        assertEquals(listOf("张三"), intent.members)
        assertEquals("公告", intent.announcement)

        val noName = bean(206)
        assertTrue(normalizer.normalize(noName) is NormalizedIntent.Invalid)
    }

    @Test
    fun `207-至少一项变更否则Invalid`() {
        val ok = bean(
            207,
            extras = mapOf(
                "targetRoom" to JsonPrimitive("外部群测试群"),
                "announcement" to JsonPrimitive("新公告"),
                "addMembers" to JsonArray(listOf(JsonPrimitive("张三"))),
            ),
        )
        val intent = (normalizer.normalize(ok) as NormalizedIntent.Valid).intent as CommandIntent.UpdateGroup
        assertEquals("新公告", intent.announcement)
        assertEquals(listOf("张三"), intent.addMembers)

        val noChange = bean(207, extras = mapOf("targetRoom" to JsonPrimitive("外部群测试群")))
        val invalid = normalizer.normalize(noChange)
        assertTrue(invalid is NormalizedIntent.Invalid)
        assertTrue((invalid as NormalizedIntent.Invalid).field.contains("announcement"))
    }

    @Test
    fun `225-成员与备注可取Friend已还原子结构`() {
        val b = bean(
            225,
            extras = mapOf("targetRoom" to JsonPrimitive("外部群测试群")),
            friend = Friend(name = "王五", markName = "重点客户", tagList = listOf("VIP")),
        )
        val intent =
            (normalizer.normalize(b) as NormalizedIntent.Valid).intent as CommandIntent.ModifyGroupMemberInfo
        assertEquals("王五", intent.memberName)
        assertEquals("重点客户", intent.markName)
        assertEquals(listOf("VIP"), intent.tagList)
    }

    // ---- 未支持类型 ----

    @Test
    fun `未注册类型-P0子集之外返回Unsupported`() {
        val dismiss = bean(MessageTypes.DISMISS_GROUP)
        assertTrue(normalizer.normalize(dismiss) is NormalizedIntent.Unsupported)

        val delContact = bean(MessageTypes.DELETE_CONTACT)
        assertTrue(normalizer.normalize(delContact) is NormalizedIntent.Unsupported)
    }

    // ---- 工具 ----

    private fun bean(
        type: Int,
        extras: Map<String, JsonElement> = emptyMap(),
        atList: List<String>? = null,
        friend: Friend? = null,
    ) = WeworkMessageBean(
        type = type,
        atList = atList,
        friend = friend,
        extras = extras,
    )
}
