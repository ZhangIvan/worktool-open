/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.protocol.Friend
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.intent.IntentNormalizer
import cn.worktool.open.feature.executor.intent.NormalizedIntent
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 对等计划批（B1~B4）意图规范化矩阵：支持集=全量批 66 条；逐指令正例 + 负例
 * （载荷校验 fail-closed）。
 */
class ExtendedIntentNormalizerTest {

    private val normalizer = IntentNormalizer(
        ExecutorRegistry.defaultFullBatch(testBindings()).supportedTypes,
    )

    private fun bean(
        type: Int,
        extras: Map<String, kotlinx.serialization.json.JsonElement> = emptyMap(),
        friend: Friend? = null,
        extraText: String? = null,
        fileUrl: String? = null,
        textType: Int? = null,
        errorCode: Int? = null,
        errorReason: String? = null,
        atList: List<String>? = null,
    ) = WeworkMessageBean(
        type = type,
        extras = extras,
        friend = friend,
        extraText = extraText,
        fileUrl = fileUrl,
        textType = textType,
        errorCode = errorCode,
        errorReason = errorReason,
        atList = atList,
    )

    private fun valid(type: Int, build: (CommandIntent) -> Unit) {
        val normalized = normalizer.normalize(bean(type))
        assertTrue("type=$type 应规范化成功：$normalized", normalized is NormalizedIntent.Valid)
        build((normalized as NormalizedIntent.Valid).intent)
    }

    private fun invalid(type: Int, vararg beans: WeworkMessageBean) {
        val target = beans.ifEmpty { arrayOf(bean(type)) }
        target.forEach {
            assertTrue("type=$type 应 Invalid：${normalizer.normalize(it)}", normalizer.normalize(it) is NormalizedIntent.Invalid)
        }
    }

    // ---- B1 ----

    @Test
    fun `B1-201无参默认意图`() {
        valid(MessageTypes.STOP_AND_GO_HOME) { }
    }

    @Test
    fun `B1-102回执字段映射`() {
        val intent = normalizer.normalize(
            bean(MessageTypes.TYPE_CONSOLE_TOAST, errorCode = 501013, errorReason = "机器人重复登录"),
        ) as NormalizedIntent.Valid
        assertEquals(501013, (intent.intent as CommandIntent.ConsoleToast).errorCode)
        assertEquals("机器人重复登录", (intent.intent as CommandIntent.ConsoleToast).errorReason)
    }

    @Test
    fun `B1-202开关字段缺失拒绝`() {
        invalid(MessageTypes.LOOP_RECEIVE_NEW_MESSAGE)
        val off = normalizer.normalize(
            bean(MessageTypes.LOOP_RECEIVE_NEW_MESSAGE, extras = mapOf("loopEnabled" to JsonPrimitive("false"))),
        ) as NormalizedIntent.Valid
        assertEquals(false, (off.intent as CommandIntent.ReceiveLoopSwitch).enabled)
    }

    // ---- B2 ----

    @Test
    fun `B2-204引用回复字段全取`() {
        val intent = normalizer.normalize(
            bean(
                MessageTypes.REPLY_MESSAGE,
                textType = 1,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("测试群A"))),
                    "receivedName" to JsonPrimitive("张三"),
                    "originalContent" to JsonPrimitive("你好"),
                    "receivedContent" to JsonPrimitive("收到"),
                ),
            ),
        ) as NormalizedIntent.Valid
        val reply = intent.intent as CommandIntent.ReplyMessage
        assertEquals(listOf("测试群A"), reply.targets)
        assertEquals("张三", reply.quotedSender)
        assertEquals(1, reply.quotedTextType)
        invalid(MessageTypes.REPLY_MESSAGE, bean(MessageTypes.REPLY_MESSAGE, extras = mapOf("titleList" to JsonArray(listOf(JsonPrimitive("A"))))))
    }

    @Test
    fun `B2-238批量文本JSON数组解析与两条下限`() {
        val batchOk = normalizer.normalize(
            bean(
                ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("WT28号测试群"))),
                    "receivedContent" to JsonPrimitive("""["第一条","第二条"]"""),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals(listOf("第一条", "第二条"), (batchOk.intent as CommandIntent.SendBatchText).texts)
        val one = bean(
            ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE,
            extras = mapOf(
                "titleList" to JsonArray(listOf(JsonPrimitive("A"))),
                "receivedContent" to JsonPrimitive("""["唯一"]"""),
            ),
        )
        val badJson = bean(
            ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE,
            extras = mapOf(
                "titleList" to JsonArray(listOf(JsonPrimitive("A"))),
                "receivedContent" to JsonPrimitive("第一条,第二条"),
            ),
        )
        invalid(ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE, one, badJson)
    }

    @Test
    fun `B2-213双语义分流键leavingMsg`() {
        val add = normalizer.normalize(
            bean(
                MessageTypes.ADD_FRIEND_BY_PHONE,
                friend = Friend(phone = "13010001000", leavingMsg = "你好", markName = "备注"),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("添加语义保留附言", "你好", (add.intent as CommandIntent.AddFriendByPhone).leavingMsg)
        val modify = normalizer.normalize(
            bean(MessageTypes.ADD_FRIEND_BY_PHONE, friend = Friend(name = "小白", markName = "重点客户")),
        ) as NormalizedIntent.Valid
        assertEquals("修改语义无附言", null, (modify.intent as CommandIntent.AddFriendByPhone).leavingMsg)
        invalid(MessageTypes.ADD_FRIEND_BY_PHONE) // friend 缺失
    }

    @Test
    fun `B2-236去重去空与50上限`() {
        val names = (1..51).map { JsonPrimitive("客户$it") }
        invalid(
            ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT,
            bean(ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT, extras = mapOf("nameList" to JsonArray(names))),
            bean(ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT), // 双空
        )
        val dedup = normalizer.normalize(
            bean(
                ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT,
                extras = mapOf("nameList" to JsonArray(listOf(JsonPrimitive("A"), JsonPrimitive("A"), JsonPrimitive(""), JsonPrimitive("B")))),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("去重去空", listOf("A", "B"), (dedup.intent as CommandIntent.DeleteSingleDirectionalContact).names)
    }

    @Test
    fun `B2-218与224的URL校验`() {
        invalid(
            MessageTypes.PUSH_FILE,
            bean(
                MessageTypes.PUSH_FILE,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("仑哥"))),
                    "objectName" to JsonPrimitive("a.png"),
                    "fileUrl" to JsonPrimitive("ftp://x/a.png"),
                    "fileType" to JsonPrimitive("image"),
                ),
            ),
        )
        invalid(
            MessageTypes.PUSH_LINK,
            bean(
                MessageTypes.PUSH_LINK,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("A"))),
                    "objectName" to JsonPrimitive("标题"),
                    "extraText" to JsonPrimitive("副标题"),
                    "fileUrl" to JsonPrimitive("not-a-url"),
                ),
            ),
        )
    }

    @Test
    fun `B2-227_228_229_230数组载荷解析`() {
        val relayPayload = mapOf(
            "titleList" to JsonArray(listOf(JsonPrimitive("源群A"))),
            "messageList" to JsonArray(
                listOf(
                    buildJsonObject {
                        put("originalContent", "第一条")
                        put("textType", 1)
                    },
                ),
            ),
            "nameList" to JsonArray(listOf(JsonPrimitive("仑哥"))),
        )
        val relay = normalizer.normalize(bean(MessageTypes.RELAY_MULTI_MESSAGE, extras = relayPayload)) as NormalizedIntent.Valid
        assertEquals(1, (relay.intent as CommandIntent.RelayMultiMessage).messages.size)

        val sendPayload = mapOf(
            "weworkMessageList" to buildJsonArray {
                add(
                    buildJsonObject {
                        put("textType", 1)
                        put("receivedContent", "第一条")
                    },
                )
            },
            "nameList" to JsonArray(listOf(JsonPrimitive("仑哥"))),
        )
        val send = normalizer.normalize(bean(MessageTypes.SEND_MULTI_MESSAGE, extras = sendPayload)) as NormalizedIntent.Valid
        assertEquals(1, (send.intent as CommandIntent.SendMultiMessage).messages.size)
        invalid(MessageTypes.RELAY_MULTI_MESSAGE) // messageList 空
        invalid(MessageTypes.SEND_MULTI_MESSAGE) // weworkMessageList 空
    }

    // ---- B3 ----

    @Test
    fun `B3-235金额与商品图册条件校验`() {
        invalid(
            ExtendedCommandTypes.CLICK_RECOMMEND_ITEM,
            // 发起收款金额格式（三位小数）
            bean(
                ExtendedCommandTypes.CLICK_RECOMMEND_ITEM,
                extraText = "10.567",
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("Dory"))),
                    "receivedContent" to JsonPrimitive("发起收款"),
                ),
            ),
            // 商品图册缺 objectName
            bean(
                ExtendedCommandTypes.CLICK_RECOMMEND_ITEM,
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("Dory"))),
                    "receivedContent" to JsonPrimitive("商品图册"),
                ),
            ),
        )
        val ok = normalizer.normalize(
            bean(
                ExtendedCommandTypes.CLICK_RECOMMEND_ITEM,
                extraText = "￥10",
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("Dory"))),
                    "receivedContent" to JsonPrimitive("发起收款"),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("￥10", (ok.intent as CommandIntent.ClickRecommendItem).amount)
    }

    @Test
    fun `B3-239newGroupName必填且selectList上限`() {
        invalid(ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE)
        invalid(
            ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE,
            bean(
                ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE,
                extras = mapOf(
                    "newGroupName" to JsonPrimitive("活动客户群"),
                    "selectList" to JsonArray((1..6).map { JsonPrimitive("群$it") }),
                ),
            ),
        )
        val liveQrOk = normalizer.normalize(
            bean(
                ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE,
                extras = mapOf("newGroupName" to JsonPrimitive("活动客户群")),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("活动客户群", (liveQrOk.intent as CommandIntent.CreateCustomerGroupLiveQrCode).newGroupName)
    }

    @Test
    fun `B3-511与510日期格式校验`() {
        invalid(
            ExtendedCommandTypes.GET_MEETING_INFO,
            bean(ExtendedCommandTypes.GET_MEETING_INFO, extras = mapOf("objectName" to JsonPrimitive("2026/10/16"))),
        )
        invalid(
            ExtendedCommandTypes.UPLOAD_LOG_FILE,
            bean(ExtendedCommandTypes.UPLOAD_LOG_FILE, extras = mapOf("logDate" to JsonPrimitive("2026-1-1"))),
        )
        valid(ExtendedCommandTypes.UPLOAD_LOG_FILE) {} // 缺省=今天
    }

    @Test
    fun `B3-305中英逗号分隔批量解析`() {
        val intent = normalizer.normalize(
            bean(
                ExtendedCommandTypes.ROBOT_QUEUE_REMOVE,
                extras = mapOf("originalContent" to JsonPrimitive("idA,idB，idC")),
            ),
        ) as NormalizedIntent.Valid
        assertEquals(listOf("idA", "idB", "idC"), (intent.intent as CommandIntent.RobotQueueRemove).messageIds)
        invalid(ExtendedCommandTypes.ROBOT_QUEUE_REMOVE)
    }

    @Test
    fun `B3-514双模式条件校验`() {
        invalid(ExtendedCommandTypes.GET_LOG_SNIPPET) // 双空
        val byId = normalizer.normalize(
            bean(ExtendedCommandTypes.GET_LOG_SNIPPET, extras = mapOf("objectName" to JsonPrimitive("1784417250785046528"))),
        ) as NormalizedIntent.Valid
        assertEquals("1784417250785046528", (byId.intent as CommandIntent.GetLogSnippet).targetMessageId)
        val byRange = normalizer.normalize(
            bean(
                ExtendedCommandTypes.GET_LOG_SNIPPET,
                extras = mapOf(
                    "logStartTime" to JsonPrimitive("08:00"),
                    "logEndTime" to JsonPrimitive("20:00"),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("20:00", (byRange.intent as CommandIntent.GetLogSnippet).logEndTime)
    }

    @Test
    fun `B3-未实现族五指令规范化为占位意图`() {
        listOf(
            MessageTypes.INIT_ANTI_HARASSMENT_RULE,
            MessageTypes.UPDATE_ANTI_HARASSMENT_RULE,
            MessageTypes.DELETED_ANTI_HARASSMENT_RULE,
            MessageTypes.VOICE_CALL,
            MessageTypes.VIDEO_CALL,
        ).forEach { type ->
            val intent = normalizer.normalize(bean(type)) as NormalizedIntent.Valid
            assertEquals("type=$type", type, (intent.intent as CommandIntent.UnimplementedCall).commandType)
        }
    }

    // ---- B4 ----

    @Test
    fun `B4-210_211卡片载荷规范化`() {
        val payload = mapOf(
            "titleList" to JsonArray(listOf(JsonPrimitive("测试群007"))),
            "objectName" to JsonPrimitive("点餐小程序"),
            "fileUrl" to JsonPrimitive("https://m.example.com/app"),
        )
        val program = normalizer.normalize(bean(MessageTypes.PUSH_MICROPROGRAM, extras = payload)) as NormalizedIntent.Valid
        assertEquals("https://m.example.com/app", (program.intent as CommandIntent.PushMicroprogram).pageUrl)
        val office = normalizer.normalize(
            bean(
                MessageTypes.PUSH_OFFICE,
                extraText = "交付手册说明",
                extras = mapOf(
                    "titleList" to JsonArray(listOf(JsonPrimitive("测试群007"))),
                    "objectName" to JsonPrimitive("交付手册"),
                    "fileUrl" to JsonPrimitive("https://doc.example.com/h"),
                ),
            ),
        ) as NormalizedIntent.Valid
        assertEquals("交付手册说明", (office.intent as CommandIntent.PushOffice).description)
        invalid(MessageTypes.PUSH_OFFICE, bean(MessageTypes.PUSH_OFFICE)) // 缺标题/URL
    }

    @Test
    fun `双例外-011与101在全量批支持集之外`() {
        assertTrue(normalizer.normalize(bean(MessageTypes.HEART_BEAT)) is NormalizedIntent.Unsupported)
        assertTrue(normalizer.normalize(bean(MessageTypes.TYPE_RECEIVE_MESSAGE_LIST)) is NormalizedIntent.Unsupported)
    }
}
