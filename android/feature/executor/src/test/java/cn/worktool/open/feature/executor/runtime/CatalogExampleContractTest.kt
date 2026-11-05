/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.WeworkMessageBeanSerializer
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.fixture.ProtocolFixtures
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.IntentNormalizer
import cn.worktool.open.feature.executor.intent.NormalizedIntent
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 管理台指令目录 × 端侧载荷解析路径契约一致性（2026-11-05 契约冻结车道）。
 *
 * 数据源 = `protocol/fixtures/admin-command-examples.json`（52 条目录示例的跨语言
 * 交换夹具；与 `server/internal/admin/catalog.go` adminCommandCatalog 逐字节同步，
 * 同步性由 Go 侧 TestCatalogExamplesFixtureInSync 锁定——本测试不依赖 Go 代码，
 * Go 侧不依赖本测试，夹具是唯一交接面）。
 *
 * 冻结断言：**每条目录 example 经执行器载荷解析路径（[IntentNormalizer]）必须
 * 得到 Valid 意图**——目录示例是管理台表单「最小可提交载荷」，端侧不可消费即
 * 表单静默产出死载荷（203 titleList vs targetRoom 一类漂移的回归锁）。
 * 第二批表单八指令（204/205/218/227/228/229/230/238）另做逐字段消费深断言：
 * 解析产物各字段与 example 值逐一相等（证明「字段可被消费」而非仅「通过校验」）。
 */
class CatalogExampleContractTest {

    /** 支持集 = 全量批注册表（与生产 SessionRuntime 同面）。 */
    private val normalizer = IntentNormalizer(
        ExecutorRegistry.defaultFullBatch(testBindings()).supportedTypes,
    )

    /** 载荷解码器与设备侧同构（已知键类型化、其余入 extras）。 */
    private val payloadJson = Json { ignoreUnknownKeys = true }

    private data class CatalogEntry(val type: Long, val name: String, val example: String)

    private fun catalog(): List<CatalogEntry> {
        val root = payloadJson.parseToJsonElement(ProtocolFixtures.text("admin-command-examples.json")).jsonObject
        val entries = root["commands"]!!.jsonArray.map {
            val o = it.jsonObject
            CatalogEntry(o["type"]!!.jsonPrimitive.longOrNull!!, o["name"]!!.jsonPrimitive.content, o["example"]!!.jsonPrimitive.content)
        }
        assertEquals("目录夹具应与 openapi 注册表同面 52 条", 52, entries.size)
        assertEquals("夹具 type 不得重复", 52, entries.map { it.type }.toSet().size)
        return entries
    }

    private fun decode(entry: CatalogEntry): WeworkMessageBean =
        payloadJson.decodeFromString(WeworkMessageBeanSerializer, entry.example)

    private fun validIntent(entry: CatalogEntry): CommandIntent {
        val normalized = normalizer.normalize(decode(entry))
        assertTrue(
            "目录示例 type=${entry.type}（${entry.name}）端侧不可消费（载荷解析失败）：$normalized\nexample=${entry.example}",
            normalized is NormalizedIntent.Valid,
        )
        return (normalized as NormalizedIntent.Valid).intent
    }

    @Test
    fun `全部52条目录示例经执行器载荷解析路径可消费`() {
        catalog().forEach { entry ->
            val intent = validIntent(entry)
            assertEquals("解析产物指令号须与目录编号一致", entry.type.toLong(), intent.commandType.toLong())
        }
    }

    @Test
    fun `夹具每条类型均在注册表支持集内（目录⊆端侧可执行面）`() {
        val catalogTypes = catalog().map { it.type.toInt() }.toSet()
        val supported = ExecutorRegistry.defaultFullBatch(testBindings()).supportedTypes
        val unsupported = catalogTypes - supported
        assertTrue(
            "目录类型不在端侧注册表支持集（示例不可消费）: $unsupported",
            unsupported.isEmpty(),
        )
    }

    // ---- 第二批表单（204/205/218/227/228/229/230/238）逐字段消费深断言 ----

    @Test
    fun `第二批-204引用回复四字段全消费`() {
        val entry = catalog().first { it.type == 204L }
        val reply = validIntent(entry) as CommandIntent.ReplyMessage
        assertEquals(listOf("成员甲"), reply.targets)
        assertEquals("发送人昵称", reply.quotedSender)
        assertEquals("被引用消息原文", reply.quotedContent)
        assertEquals("收到", reply.content)
    }

    @Test
    fun `第二批-205转发四字段全消费`() {
        val entry = catalog().first { it.type == 205L }
        val relay = validIntent(entry) as CommandIntent.RelayMessage
        assertEquals(listOf("来源会话"), relay.sourceRooms)
        assertEquals("发送人昵称", relay.quotedSender)
        assertEquals("消息原文", relay.quotedContent)
        assertEquals(listOf("成员乙"), relay.receivers)
    }

    @Test
    fun `第二批-218推送文件五字段全消费`() {
        val entry = catalog().first { it.type == 218L }
        val file = validIntent(entry) as CommandIntent.PushFile
        assertEquals(listOf("成员甲"), file.targets)
        assertEquals("交付文件.bin", file.objectName)
        assertEquals("https://example.com/f.bin", file.fileUrl)
        assertEquals("*", file.fileType)
    }

    @Test
    fun `第二批-227批量转发三段全消费`() {
        val entry = catalog().first { it.type == 227L }
        val relay = validIntent(entry) as CommandIntent.RelayMultiMessage
        assertEquals(listOf("来源会话"), relay.sourceRooms)
        assertEquals(listOf("消息原文一", "消息原文二"), relay.messages.map { it.originalContent })
        assertEquals(listOf(1, 1), relay.messages.map { it.textType })
        assertEquals(listOf("成员甲"), relay.receivers)
    }

    @Test
    fun `第二批-228合并转发同构消费`() {
        val entry = catalog().first { it.type == 228L }
        val relay = validIntent(entry) as CommandIntent.RelayMergeMessage
        assertEquals(listOf("来源会话"), relay.sourceRooms)
        assertEquals(listOf("消息原文一"), relay.messages.map { it.originalContent })
        assertEquals(listOf("成员甲"), relay.receivers)
    }

    @Test
    fun `第二批-229批量发送weworkMessageList加nameList消费`() {
        val entry = catalog().first { it.type == 229L }
        val send = validIntent(entry) as CommandIntent.SendMultiMessage
        assertEquals(1, send.messages.size)
        assertEquals("通知", send.messages.single().content)
        assertEquals(1, send.messages.single().textType)
        assertEquals(listOf("成员甲", "成员乙"), send.receivers)
    }

    @Test
    fun `第二批-230合并发送weworkMessageList加nameList消费`() {
        val entry = catalog().first { it.type == 230L }
        val send = validIntent(entry) as CommandIntent.SendMergeMessage
        assertEquals("内容一", send.messages.single().content)
        assertEquals(listOf("成员甲"), send.receivers)
    }

    @Test
    fun `第二批-238批量文本JSON数组串消费`() {
        val entry = catalog().first { it.type == 238L }
        val batch = validIntent(entry) as CommandIntent.SendBatchText
        assertEquals(listOf("成员甲"), batch.targets)
        assertEquals(listOf("文本一", "文本二"), batch.texts)
    }

    // ---- 执行器迁移面（203/206/207/225 主读字段迁移的回归锁） ----

    @Test
    fun `迁移-203单元素titleList消费`() {
        val entry = catalog().first { it.type == 203L }
        val send = validIntent(entry) as CommandIntent.SendText
        assertEquals("成员甲", send.targetRoom)
        assertEquals("你好", send.content)
    }

    @Test
    fun `迁移-203多元素titleList显式拒绝不静默截断`() {
        val bean = payloadJson.decodeFromString(
            WeworkMessageBeanSerializer,
            """{"type":203,"titleList":["群一","群二"],"receivedContent":"你好"}""",
        )
        val normalized = normalizer.normalize(bean)
        assertTrue("多元素 titleList 应 Invalid（203 单目标语义）：$normalized", normalized is NormalizedIntent.Invalid)
        assertTrue(
            "拒绝原因应指引批量通道：${(normalized as NormalizedIntent.Invalid).reason}",
            normalized.reason.contains("229"),
        )
    }

    @Test
    fun `迁移-206成员读selectList`() {
        val entry = catalog().first { it.type == 206L }
        val group = validIntent(entry) as CommandIntent.InitGroup
        assertEquals("交付群", group.groupName)
        assertEquals(listOf("成员甲"), group.members)
    }

    @Test
    fun `迁移-207主读groupName`() {
        val entry = catalog().first { it.type == 207L }
        val group = validIntent(entry) as CommandIntent.UpdateGroup
        assertEquals("交付群", group.targetGroup)
        assertEquals("新公告", group.announcement)
    }

    @Test
    fun `迁移-225主读groupName成员取friend子结构`() {
        val entry = catalog().first { it.type == 225L }
        val member = validIntent(entry) as CommandIntent.ModifyGroupMemberInfo
        assertEquals("交付群", member.targetGroup)
        assertEquals("成员乙", member.memberName)
        assertEquals("重要", member.markName)
    }
}
