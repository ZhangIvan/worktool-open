/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.risk.RiskLevel
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 对等计划全量批注册表（docs/10 §B 68 条矩阵 → 端侧可执行 66 条映射）：
 * 覆盖集 / 011·101 双例外（非缺漏，docs/02 §3 分工）/ FR-53 风险级矩阵
 * （docs/10 §B 各批行 + FR-53 基线表）。
 */
class FullBatchRegistryTest {

    private val registry = ExecutorRegistry.defaultFullBatch(testBindings())

    @Test
    fun `全量批-端侧可执行66条全覆盖`() {
        val expected = setOf(
            // 首批 8
            MessageTypes.SEND_MESSAGE, MessageTypes.INIT_GROUP, MessageTypes.UPDATE_GROUP,
            MessageTypes.MODIFY_GROUP_MEMBER_INFO, MessageTypes.PASS_ALL_FRIEND_REQUEST,
            MessageTypes.GET_GROUP_INFO, MessageTypes.GET_FRIEND_INFO, MessageTypes.GET_LOCAL_FILE,
            // B1 4
            MessageTypes.STOP_AND_GO_HOME, MessageTypes.TYPE_CONSOLE_TOAST,
            MessageTypes.LOOP_RECEIVE_NEW_MESSAGE, ExtendedCommandTypes.DO_RECEIVE_NEW_MESSAGE,
            // B2 22
            MessageTypes.REPLY_MESSAGE, MessageTypes.RELAY_MESSAGE,
            MessageTypes.RELAY_MULTI_MESSAGE, MessageTypes.RELAY_MERGE_MESSAGE,
            MessageTypes.SEND_MULTI_MESSAGE, MessageTypes.SEND_MERGE_MESSAGE,
            ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE, MessageTypes.RECALL_MESSAGE,
            MessageTypes.ADD_FRIEND_BY_PHONE, MessageTypes.ADD_FRIEND_BY_GROUP,
            MessageTypes.DELETE_CONTACT, ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT,
            MessageTypes.PUSH_FILE, MessageTypes.PUSH_LINK,
            MessageTypes.PUSH_MICRO_DISK_IMAGE, MessageTypes.PUSH_MICRO_DISK_FILE,
            MessageTypes.GET_MY_INFO, MessageTypes.GET_RECENT_LIST, MessageTypes.GET_CORP_LIST,
            MessageTypes.GET_GROUP_QRCODE, MessageTypes.GET_ALL_FRIEND_INFO, MessageTypes.GET_ALL_GROUP_INFO,
            // B3 30
            MessageTypes.DISMISS_GROUP, MessageTypes.SHOW_GROUP_INFO,
            ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE,
            ExtendedCommandTypes.ADD_GROUP_BOARD_WEBPAGE, ExtendedCommandTypes.EXIT_GROUP,
            ExtendedCommandTypes.CLEAN_CACHE, MessageTypes.ADD_NEED_DEAL, MessageTypes.CLOCK_IN,
            MessageTypes.SWITCH_CORP, MessageTypes.SCAN_QR_CODE,
            MessageTypes.INIT_ANTI_HARASSMENT_RULE, MessageTypes.UPDATE_ANTI_HARASSMENT_RULE,
            MessageTypes.DELETED_ANTI_HARASSMENT_RULE, MessageTypes.VOICE_CALL, MessageTypes.VIDEO_CALL,
            ExtendedCommandTypes.CLICK_RECOMMEND_ITEM,
            ExtendedCommandTypes.GET_MEETING_INFO, ExtendedCommandTypes.GET_GROUP_MEMBER_INFO,
            ExtendedCommandTypes.GET_GROUP_NAME_INFO, ExtendedCommandTypes.GET_DEVICE_RESOURCE,
            ExtendedCommandTypes.GET_LOG_SNIPPET, ExtendedCommandTypes.UPLOAD_LOG_FILE,
            MessageTypes.ROBOT_LOG, MessageTypes.ROBOT_ERROR_LOG,
            MessageTypes.ROBOT_QUEUE_CLEAR, ExtendedCommandTypes.ROBOT_QUEUE_REMOVE,
            ExtendedCommandTypes.ROBOT_QUEUE_GET, ExtendedCommandTypes.ROBOT_CONFIG_UPDATED,
            MessageTypes.ROBOT_CONTROLLER_TEST, ExtendedCommandTypes.QR_CODE_JOIN_GROUP,
            // B4 2
            MessageTypes.PUSH_MICROPROGRAM, MessageTypes.PUSH_OFFICE,
        )
        assertEquals("全量批 = 8+4+22+30+2 = 66 条", expected, registry.supportedTypes)
        assertEquals(66, registry.supportedTypes.size)
        assertTrue("首批 8 条是全量批子集", ExecutorRegistry.defaultFirstBatch(
            cn.worktool.open.feature.executor.fake.RecordingQuerySink(),
            cn.worktool.open.feature.executor.fake.FakeLocalFiles(mapOf(), null),
        ).supportedTypes.all { registry.supports(it) })
    }

    @Test
    fun `双例外-011心跳与101上报不经注册表（docs_02分工非缺漏）`() {
        assertFalse("011 = 协议层通道（core:protocol M2 已交付）", registry.supports(MessageTypes.HEART_BEAT))
        assertFalse("101 = 端→云上报方向（MessageEventReporter 承担）", registry.supports(MessageTypes.TYPE_RECEIVE_MESSAGE_LIST))
    }

    /** FR-53 风险级矩阵（docs/10 §B 各批行：L2={219,234,236,304,237}；L1 批量/建群/拉人/转发族；其余 L0）。 */
    @Test
    fun `风险级矩阵-L2清单`() {
        val l2Samples = mapOf(
            MessageTypes.DISMISS_GROUP to CommandIntent.DismissGroup("群"),
            MessageTypes.DELETE_CONTACT to CommandIntent.DeleteContact("联系人"),
            ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT to
                CommandIntent.DeleteSingleDirectionalContact(listOf("单向客户")),
            MessageTypes.ROBOT_QUEUE_CLEAR to CommandIntent.RobotQueueClear(),
            ExtendedCommandTypes.CLEAN_CACHE to CommandIntent.CleanCache(),
        )
        l2Samples.forEach { (type, intent) ->
            assertEquals("type=$type 应为 L2", RiskLevel.L2, registry.executorFor(type)!!.riskLevel(intent))
        }
    }

    @Test
    fun `风险级矩阵-L1清单（含防骚扰组与通话占位）`() {
        val l1Samples: List<Pair<Int, CommandIntent>> = listOf(
            MessageTypes.INIT_GROUP to CommandIntent.InitGroup("新群"),
            MessageTypes.UPDATE_GROUP to CommandIntent.UpdateGroup("群", addMembers = listOf("张三")),
            MessageTypes.RELAY_MESSAGE to CommandIntent.RelayMessage(listOf("源"), "甲", "内容", 7, listOf("仑哥")),
            MessageTypes.RELAY_MULTI_MESSAGE to CommandIntent.RelayMultiMessage(listOf("源"), listOf(CommandIntent.RelayItem("m", 1)), listOf("r")),
            MessageTypes.RELAY_MERGE_MESSAGE to CommandIntent.RelayMergeMessage(listOf("源"), listOf(CommandIntent.RelayItem("m", 1)), listOf("r")),
            MessageTypes.SEND_MULTI_MESSAGE to CommandIntent.SendMultiMessage(listOf(CommandIntent.OutboundMessage(1, "t")), listOf("r")),
            MessageTypes.SEND_MERGE_MESSAGE to CommandIntent.SendMergeMessage(listOf(CommandIntent.OutboundMessage(1, "t")), listOf("r")),
            ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE to CommandIntent.SendBatchText(listOf("A"), listOf("t1", "t2")),
            MessageTypes.RECALL_MESSAGE to CommandIntent.RecallMessage(listOf("A"), "原文", 1),
            MessageTypes.ADD_FRIEND_BY_PHONE to CommandIntent.AddFriendByPhone("13010001000", leavingMsg = "hi"),
            MessageTypes.ADD_FRIEND_BY_GROUP to CommandIntent.AddFriendByGroup("群", "成员"),
            MessageTypes.PUSH_MICRO_DISK_IMAGE to CommandIntent.PushMicroDiskImage(listOf("仑哥"), "logo2"),
            MessageTypes.PUSH_MICRO_DISK_FILE to CommandIntent.PushMicroDiskFile(listOf("仑哥"), "报告.pdf"),
            MessageTypes.SHOW_GROUP_INFO to CommandIntent.ShowGroupInfo("群"),
            ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE to CommandIntent.CreateCustomerGroupLiveQrCode(null, emptyList(), "新客户群"),
            ExtendedCommandTypes.ADD_GROUP_BOARD_WEBPAGE to CommandIntent.AddGroupBoardWebpage("群", "https://x"),
            ExtendedCommandTypes.EXIT_GROUP to CommandIntent.ExitGroup("群"),
            MessageTypes.SWITCH_CORP to CommandIntent.SwitchCorp("B公司"),
            MessageTypes.INIT_ANTI_HARASSMENT_RULE to CommandIntent.UnimplementedCall(MessageTypes.INIT_ANTI_HARASSMENT_RULE),
            MessageTypes.UPDATE_ANTI_HARASSMENT_RULE to CommandIntent.UnimplementedCall(MessageTypes.UPDATE_ANTI_HARASSMENT_RULE),
            MessageTypes.DELETED_ANTI_HARASSMENT_RULE to CommandIntent.UnimplementedCall(MessageTypes.DELETED_ANTI_HARASSMENT_RULE),
            MessageTypes.VOICE_CALL to CommandIntent.UnimplementedCall(MessageTypes.VOICE_CALL),
            MessageTypes.VIDEO_CALL to CommandIntent.UnimplementedCall(MessageTypes.VIDEO_CALL),
            ExtendedCommandTypes.CLICK_RECOMMEND_ITEM to CommandIntent.ClickRecommendItem(listOf("Dory"), "发起收款", amount = "10"),
            ExtendedCommandTypes.QR_CODE_JOIN_GROUP to CommandIntent.QrCodeJoinGroup("https://x/qr.jpg"),
        )
        l1Samples.forEach { (type, intent) ->
            assertEquals("type=$type 应为 L1", RiskLevel.L1, registry.executorFor(type)!!.riskLevel(intent))
        }
    }

    @Test
    fun `风险级矩阵-其余已实现指令L0抽查`() {
        val l0Samples: List<Pair<Int, CommandIntent>> = listOf(
            MessageTypes.SEND_MESSAGE to CommandIntent.SendText("群", "hi"),
            MessageTypes.MODIFY_GROUP_MEMBER_INFO to CommandIntent.ModifyGroupMemberInfo("群", "成员", markName = "m"),
            MessageTypes.PASS_ALL_FRIEND_REQUEST to CommandIntent.PassFriend(),
            MessageTypes.STOP_AND_GO_HOME to CommandIntent.StopAndGoHome(),
            MessageTypes.TYPE_CONSOLE_TOAST to CommandIntent.ConsoleToast(501013, "x"),
            MessageTypes.LOOP_RECEIVE_NEW_MESSAGE to CommandIntent.ReceiveLoopSwitch(true),
            ExtendedCommandTypes.DO_RECEIVE_NEW_MESSAGE to CommandIntent.TriggerReceiveNewMessage(),
            MessageTypes.REPLY_MESSAGE to CommandIntent.ReplyMessage(listOf("A"), "张三", "原文", 1, "回复"),
            MessageTypes.PUSH_FILE to CommandIntent.PushFile(listOf("仑哥"), "a.png", "https://x/a.png", "image"),
            MessageTypes.PUSH_LINK to CommandIntent.PushLink(listOf("A"), "标题", "副标题", "https://x"),
            MessageTypes.GET_MY_INFO to CommandIntent.GetMyInfo(),
            MessageTypes.GET_RECENT_LIST to CommandIntent.GetRecentList(),
            MessageTypes.GET_CORP_LIST to CommandIntent.GetCorpList(),
            MessageTypes.GET_GROUP_QRCODE to CommandIntent.GetGroupQrCode("群"),
            MessageTypes.GET_ALL_FRIEND_INFO to CommandIntent.GetAllFriendInfo(),
            MessageTypes.GET_ALL_GROUP_INFO to CommandIntent.GetAllGroupInfo(),
            MessageTypes.ADD_NEED_DEAL to CommandIntent.AddNeedDeal(listOf("张三"), "待办"),
            MessageTypes.CLOCK_IN to CommandIntent.ClockIn(),
            MessageTypes.SCAN_QR_CODE to CommandIntent.ScanQrCode(),
            ExtendedCommandTypes.GET_MEETING_INFO to CommandIntent.GetMeetingInfo("2026-11-05"),
            ExtendedCommandTypes.GET_GROUP_MEMBER_INFO to CommandIntent.GetGroupMemberInfo("群"),
            ExtendedCommandTypes.GET_GROUP_NAME_INFO to CommandIntent.GetGroupNameInfo("群"),
            ExtendedCommandTypes.GET_DEVICE_RESOURCE to CommandIntent.GetDeviceResource(),
            ExtendedCommandTypes.GET_LOG_SNIPPET to CommandIntent.GetLogSnippet(targetMessageId = "x"),
            ExtendedCommandTypes.UPLOAD_LOG_FILE to CommandIntent.UploadLogFile(),
            MessageTypes.ROBOT_LOG to CommandIntent.RobotLog(),
            MessageTypes.ROBOT_ERROR_LOG to CommandIntent.RobotErrorLog(),
            ExtendedCommandTypes.ROBOT_QUEUE_REMOVE to CommandIntent.RobotQueueRemove(listOf("id")),
            ExtendedCommandTypes.ROBOT_QUEUE_GET to CommandIntent.RobotQueueGet(),
            ExtendedCommandTypes.ROBOT_CONFIG_UPDATED to CommandIntent.RobotConfigUpdated(),
            MessageTypes.ROBOT_CONTROLLER_TEST to CommandIntent.RobotControllerTest(),
            MessageTypes.PUSH_MICROPROGRAM to CommandIntent.PushMicroprogram(listOf("A"), "标题", pageUrl = "https://x"),
            MessageTypes.PUSH_OFFICE to CommandIntent.PushOffice(listOf("A"), "标题", docUrl = "https://x"),
        )
        l0Samples.forEach { (type, intent) ->
            assertEquals("type=$type 应为 L0", RiskLevel.L0, registry.executorFor(type)!!.riskLevel(intent))
        }
    }

    @Test
    fun `防御性重申-236恒L2防矩阵误改`() {
        assertEquals(
            RiskLevel.L2,
            registry.executorFor(ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT)!!
                .riskLevel(CommandIntent.DeleteSingleDirectionalContact(listOf("a", "b"))),
        )
    }
}
