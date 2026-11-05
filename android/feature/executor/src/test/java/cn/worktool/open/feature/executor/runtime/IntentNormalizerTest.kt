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

    /** 支持集 = v0.1 首批全量注册表（五指令 + 501/502/509）。 */
    private val normalizer = IntentNormalizer(
        ExecutorRegistry.defaultFirstBatch(
            querySink = cn.worktool.open.feature.executor.fake.RecordingQuerySink(),
            files = cn.worktool.open.feature.executor.fake.FakeLocalFiles(mapOf(), null),
        ).supportedTypes,
    )
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

    // ---- 203 载荷校验（2026-11-05 契约冻结：目标字段 canonical=titleList，
    //      候选名 targetRoom 为迁移期只读别名；多元素 titleList 显式拒绝） ----

    @Test
    fun `203-缺目标会话或缺正文均Invalid`() {
        val noTarget = bean(203, extras = mapOf("receivedContent" to JsonPrimitive("hi")))
        assertTrue(normalizer.normalize(noTarget) is NormalizedIntent.Invalid)

        val noContent = bean(203, extras = mapOf("titleList" to JsonArray(listOf(JsonPrimitive("群")))))
        assertTrue(normalizer.normalize(noContent) is NormalizedIntent.Invalid)
    }

    @Test
    fun `203-titleList主读且多元素显式拒绝`() {
        val ok = normalizer.normalize(
            bean(
                203,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("外部群测试群"))),
                    "receivedContent" to JsonPrimitive("通知"),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("外部群测试群", (ok.intent as CommandIntent.SendText).targetRoom)

        val alias = normalizer.normalize(
            bean(
                203,
                extras = mapOf(
                    "targetRoom" to JsonPrimitive("外部群测试群"),
                    "receivedContent" to JsonPrimitive("通知"),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("候选别名 targetRoom 迁移期可读", "外部群测试群", (alias.intent as CommandIntent.SendText).targetRoom)

        val multi = bean(
            203,
            extras = mapOf(
                "titleList" to JsonArray(listOf(JsonPrimitive("群一"), JsonPrimitive("群二"))),
                "receivedContent" to JsonPrimitive("通知"),
            ),
        )
        val invalid = normalizer.normalize(multi)
        assertTrue("多元素 titleList 应 Invalid（203 单目标语义）", invalid is NormalizedIntent.Invalid)
    }

    @Test
    fun `203-atList规范化入意图`() {
        val b = bean(
            203,
            extras = mapOf(
                "titleList" to JsonArray(listOf(JsonPrimitive("外部群测试群"))),
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
    fun `206-群名必填成员可选（名单冻结selectList）`() {
        val ok = bean(
            206,
            extras = mapOf(
                "groupName" to JsonPrimitive("客户交付群"),
                "selectList" to JsonArray(listOf(JsonPrimitive("张三"))),
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
    fun `207-至少一项变更否则Invalid（目标冻结groupName）`() {
        val ok = bean(
            207,
            extras = mapOf(
                "groupName" to JsonPrimitive("外部群测试群"),
                "announcement" to JsonPrimitive("新公告"),
                "addMembers" to JsonArray(listOf(JsonPrimitive("张三"))),
            ),
        )
        val intent = (normalizer.normalize(ok) as NormalizedIntent.Valid).intent as CommandIntent.UpdateGroup
        assertEquals("外部群测试群", intent.targetGroup)
        assertEquals("新公告", intent.announcement)
        assertEquals(listOf("张三"), intent.addMembers)

        val noChange = bean(207, extras = mapOf("groupName" to JsonPrimitive("外部群测试群")))
        val invalid = normalizer.normalize(noChange)
        assertTrue(invalid is NormalizedIntent.Invalid)
        assertTrue((invalid as NormalizedIntent.Invalid).field.contains("announcement"))
    }

    @Test
    fun `225-目标冻结groupName且成员可取Friend已还原子结构`() {
        val b = bean(
            225,
            extras = mapOf("groupName" to JsonPrimitive("外部群测试群")),
            friend = Friend(name = "王五", markName = "重点客户", tagList = listOf("VIP")),
        )
        val intent =
            (normalizer.normalize(b) as NormalizedIntent.Valid).intent as CommandIntent.ModifyGroupMemberInfo
        assertEquals("外部群测试群", intent.targetGroup)
        assertEquals("王五", intent.memberName)
        assertEquals("重点客户", intent.markName)
        assertEquals(listOf("VIP"), intent.tagList)
    }

    // ---- 501/502/509（v0.1 首批补齐，docs/05 P0 首批行） ----

    @Test
    fun `501与502-无必填字段默认意图规范化`() {
        val group = normalizer.normalize(bean(MessageTypes.GET_GROUP_INFO)) as NormalizedIntent.Valid
        assertEquals(CommandIntent.GetGroupList(), group.intent)

        val friend = normalizer.normalize(bean(MessageTypes.GET_FRIEND_INFO)) as NormalizedIntent.Valid
        assertEquals(CommandIntent.GetFriendList(), friend.intent)
    }

    @Test
    fun `501-selectList名单进意图（会话10对齐原版）`() {
        val b = bean(
            MessageTypes.GET_GROUP_INFO,
            extras = mapOf(
                "selectList" to kotlinx.serialization.json.JsonArray(
                    listOf(
                        kotlinx.serialization.json.JsonPrimitive("成员甲、成员乙、成员丙、成员丁"),
                        kotlinx.serialization.json.JsonPrimitive("交付群"),
                    ),
                ),
            ),
        )
        val intent = (normalizer.normalize(b) as NormalizedIntent.Valid).intent as CommandIntent.GetGroupList
        assertEquals(listOf("成员甲、成员乙、成员丙、成员丁", "交付群"), intent.groupNames)
    }

    @Test
    fun `509-filePath候选字段可选`() {
        val withPath = normalizer.normalize(
            bean(MessageTypes.GET_LOCAL_FILE, extras = mapOf("filePath" to JsonPrimitive("/sdcard/Download/手册.pdf"))),
        ) as NormalizedIntent.Valid
        assertEquals("/sdcard/Download/手册.pdf", (withPath.intent as CommandIntent.GetLocalFile).filePath)

        val blank = normalizer.normalize(
            bean(MessageTypes.GET_LOCAL_FILE, extras = mapOf("filePath" to JsonPrimitive("  "))),
        ) as NormalizedIntent.Valid
        assertEquals("空白路径归默认（当前日志）", null, (blank.intent as CommandIntent.GetLocalFile).filePath)

        val none = normalizer.normalize(bean(MessageTypes.GET_LOCAL_FILE)) as NormalizedIntent.Valid
        assertEquals(null, (none.intent as CommandIntent.GetLocalFile).filePath)
    }

    // ---- 未支持类型 ----

    @Test
    fun `未注册类型-P0子集之外返回Unsupported`() {
        val dismiss = bean(MessageTypes.DISMISS_GROUP)
        assertTrue(normalizer.normalize(dismiss) is NormalizedIntent.Unsupported)

        val delContact = bean(MessageTypes.DELETE_CONTACT)
        assertTrue(normalizer.normalize(delContact) is NormalizedIntent.Unsupported)

        val getMyInfo = bean(MessageTypes.GET_MY_INFO) // 503 未实现（不在首批）
        assertTrue(normalizer.normalize(getMyInfo) is NormalizedIntent.Unsupported)
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
