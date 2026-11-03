/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 512 GET_GROUP_MEMBER_INFO 群成员信息查询（docs/10 B3，L0）：
 * 搜索进群 → 群详情读群名/备注/群主/公告/成员列表 → 结构化推送。
 * **已退出群=失败**（GROUP_EXITED；对照 515 同态=成功——重要分流差异）。
 * 空成员列表仍推送（memberList=[], memberCount=0）。
 */
class GetGroupMemberInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetGroupMemberInfo>() {

    override val commandType: Int get() = ExtendedCommandTypes.GET_GROUP_MEMBER_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetGroupMemberInfo): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}, exitedGroupMustFail=true",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                readAndPushStep(intent),
            ),
        )

    private fun readAndPushStep(intent: CommandIntent.GetGroupMemberInfo): PlanStep = PlanStep(
        id = "read-and-push",
        description = "读群名/备注/群主/公告/成员列表并推送（空成员列表仍推送）",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群详情页") {
            if (rules.find(it, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE) != null) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到群聊: ${intent.groupName}（未进入群详情页）",
        )
        if (rules.findTextContains(root, "无法在已退出的群聊中发送消息") != null) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.GROUP_EXITED, "检测到已退出的群聊：${intent.groupName}")
        }
        val groupName = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE)?.text.orEmpty()
        if (groupName.isBlank()) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到群聊: ${intent.groupName}（未读取到真实群名）")
        }
        val remark = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_REMARK_VALUE)?.text.orEmpty()
        val owner = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_OWNER)?.text.orEmpty()
        val announcement = rules.find(root, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_VALUE)?.text.orEmpty()
        val members = rules.findAll(root, WeworkSelectorIds.GROUP_MEMBER_LIST_ITEM)
            .mapNotNull { it.text?.trim() }
            .filter { it.isNotEmpty() }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", groupName)
                put("groupRemark", remark)
                put("groupOwner", owner)
                put("groupAnnouncement", announcement)
                put("memberList", JsonArray(members.map { JsonPrimitive(it) }))
                put("memberCount", members.size)
            },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 515 GET_GROUP_NAME_INFO 获取群名和群备注（docs/10 B3，L0；轻量查询：
 * **不读成员列表**）：搜索进群详情 → 只取群名/备注 → 推送。
 * **已退群仍可搜索=成功**（回调搜索结果的群名/备注——与 512 相反语义）。
 */
class GetGroupNameInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetGroupNameInfo>() {

    override val commandType: Int get() = ExtendedCommandTypes.GET_GROUP_NAME_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetGroupNameInfo): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}, exitedGroupStillSuccess=true",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                readAndPushStep(intent),
            ),
        )

    private fun readAndPushStep(intent: CommandIntent.GetGroupNameInfo): PlanStep = PlanStep(
        id = "read-and-push",
        description = "轻量读取群名/群备注（不进成员列表）并推送",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群详情页") {
            if (rules.find(it, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE) != null) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到群聊: ${intent.groupName}",
        )
        val groupName = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE)?.text.orEmpty()
        if (groupName.isBlank()) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到群聊: ${intent.groupName}（群名不可读）")
        }
        val remark = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_REMARK_VALUE)?.text.orEmpty()
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", groupName)
                put("groupRemark", remark)
            },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 511 GET_MEETING_INFO 会议信息查询（docs/10 B3，L0；objectName=yyyy-MM-dd
 * 今天或未来 28 天内）：工作台 → 日历 → 定位日期格 → 读会议列表 →
 * 推送 meetingInfoList；**指定日期无会议不推送**（任务回执仍成功）。
 * 会议详情字段（预定/实际时间、组织人）按页面文本返回，不归一化。
 */
class GetMeetingInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetMeetingInfo>() {

    override val commandType: Int get() = ExtendedCommandTypes.GET_MEETING_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetMeetingInfo): ExecutionPlan {
        val dayCell = intent.date.takeLast(2).trimStart('0') + "日"
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "date=${intent.date}, cell=$dayCell",
            steps = listOf(
                openWorkbenchStep(),
                openCalendarStep(),
                clickDateCellStep(dayCell),
                readMeetingsStep(intent),
            ),
        )
    }

    private fun openWorkbenchStep(): PlanStep = PlanStep(
        id = "open-workbench",
        description = "切换到底部「工作台」Tab",
    ) { driver ->
        val tab = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.WORKBENCH_TAB) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「工作台」Tab 缺失")
        CommonSteps.clickOrReject(driver, tab, "「工作台」Tab")
    }

    private fun openCalendarStep(): PlanStep = PlanStep(
        id = "open-calendar",
        description = "进入「日历」页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「日历」入口") { root ->
            rules.find(root, WeworkSelectorIds.CALENDAR_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到日历入口")
        CommonSteps.clickOrReject(driver, entry, "「日历」入口")
    }

    private fun clickDateCellStep(dayCell: String): PlanStep = PlanStep(
        id = "click-date-cell",
        description = "在日历定位「$dayCell」日期格并点击",
    ) { driver ->
        val cell = driver.currentRoot()?.let { rules.findByText(it, dayCell) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "未找到$dayCell 日期（超出今天或未来 28 天窗口？）",
            )
        CommonSteps.clickOrReject(driver, cell, "「$dayCell」日期格")
    }

    private fun readMeetingsStep(intent: CommandIntent.GetMeetingInfo): PlanStep = PlanStep(
        id = "read-meetings",
        description = "读取当日会议列表并推送（无会议不推送）",
    ) { driver ->
        val meetings = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "会议列表") { root ->
            if (rules.findAll(root, WeworkSelectorIds.MEETING_LIST_ITEM).isNotEmpty()) root else null
        }?.let { root ->
            rules.findAll(root, WeworkSelectorIds.MEETING_LIST_ITEM).mapNotNull { it.text?.trim() }
        } ?: emptyList()
        if (meetings.isEmpty()) {
            return@PlanStep StepOutcome.Success // 指定日期无会议安排，无需回调
        }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("date", intent.date)
                put(
                    "meetingInfoList",
                    JsonArray(
                        meetings.map { name ->
                            buildJsonObject {
                                put("meetingName", name)
                                put("meetingTime", "")
                                put("actualMeetingTime", "")
                                put("orgName", "")
                            }
                        },
                    ),
                )
            },
        )
        StepOutcome.Success
    }
}
