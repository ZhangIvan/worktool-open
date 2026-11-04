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
import cn.worktool.open.feature.executor.runtime.PaginatedListReader
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
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
 * **「搜索到已退出群聊」分支**（docs/10 §B 515 语义已定：已退群可搜索仍算成功、
 * 上游 201104 才算失败；对照 APK 还原稿 m3/a.java getGroupNameInfo：退群标记 +
 * 群名可读 → errorCode=0 照常回调群名/备注；群名不可读才落「未找到群聊」失败）：
 * 端侧显式处理 = 群详情页见「无法在已退出的群聊中发送消息」标记时**仍成功**推送
 * 群名/备注（与 512 同态失败相反，重要分流差异，见 GROUP_EXITED 注释）。
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
        description = "轻量读取群名/群备注（不进成员列表）并推送；已退出群聊仍成功",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群详情页") {
            if (rules.find(it, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE) != null) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到群聊: ${intent.groupName}",
        )
        // 「搜索到已退出群聊」分支显式判定（512 同一标记文本，语义相反）
        val exited = rules.findTextContains(root, "无法在已退出的群聊中发送消息") != null
        val groupName = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE)?.text.orEmpty()
        if (groupName.isBlank()) {
            // 退群 + 群名不可读：对照上游 201104「未找到群聊」失败（不虚构成功）
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                if (exited) {
                    "搜索到已退出群聊: ${intent.groupName}（群名不可读，未找到群聊失败口径）"
                } else {
                    "未找到群聊: ${intent.groupName}（群名不可读）"
                },
            )
        }
        val remark = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_REMARK_VALUE)?.text.orEmpty()
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", groupName)
                put("groupRemark", remark)
            },
        )
        StepOutcome.Success // 已退群仍可搜索=成功（回调搜索结果的群名/备注）
    }
}

/**
 * 指令 511 GET_MEETING_INFO 会议信息查询（docs/10 B3，L0；objectName=yyyy-MM-dd
 * 今天或未来 28 天内）：工作台 → 日历 → 定位日期格 → **分页滚动采集当日会议列表**
 * （[PaginatedListReader]，507/508 同形态：读无新增=末页收敛）→ **逐会进详情读取**
 * 预定时间/实际时间/组织人（按页面文本返回，不归一化）→ 推送 meetingInfoList；
 * **指定日期无会议不推送**（任务回执仍成功）。
 *
 * 会议详情族选择器为占位（**NOT_CALIBRATED，2026-11-04**，见 WeworkSelectorIds
 * 各常量注释）：详情页锚点（预定时间）真机读不到 = 如实 NODE_NOT_FOUND 失败并留痕，
 * **不虚构详情读取成功**（v1.6 前占位实现把三字段推空串冒充已读，已废弃）。
 * 锚点就绪但某字段缺失（如未开始会议无「实际时间」）→ 如实空串。
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
        val reader = PaginatedListReader(
            rules = rules,
            containerSelector = WeworkSelectorIds.CALENDAR_MEETING_LIST_CONTAINER,
            itemSelector = WeworkSelectorIds.MEETING_LIST_ITEM,
            what = "当日会议列表",
        )
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "date=${intent.date}, cell=$dayCell",
            steps = listOf(
                openWorkbenchStep(),
                openCalendarStep(),
                clickDateCellStep(dayCell),
                awaitMeetingListStep(reader),
            ) + reader.pageSteps(MAX_PAGES, "collect-meetings") + listOf(
                readMeetingDetailsStep(intent, reader),
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

    /**
     * 等当日会议列表首屏就绪（保持原契约：超时=指定日期无会议 → 收敛空过链，
     * 回执仍成功不推送）。只等不读——第 1 页交给 [PaginatedListReader] 首页步读取，
     * 否则此处读掉全部新增会让首页步「无新增」误判收敛、滚动分页永不发生。
     */
    private fun awaitMeetingListStep(reader: PaginatedListReader): PlanStep = PlanStep(
        id = "await-meeting-list",
        description = "等待当日会议列表首屏（超时=无会议，回执成功不推送）",
    ) { driver ->
        val ready = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "会议列表") { root ->
            if (rules.findAll(root, WeworkSelectorIds.MEETING_LIST_ITEM).isNotEmpty()) root else null
        }
        if (ready == null) reader.readPage(null) // 超时=无会议：置收敛（空过链）
        StepOutcome.Success
    }

    /** 逐会进详情读取三字段并推送（任一详情打不开 = 整体如实失败，不外发半份伪造结果）。 */
    private fun readMeetingDetailsStep(
        intent: CommandIntent.GetMeetingInfo,
        reader: PaginatedListReader,
    ): PlanStep = PlanStep(
        id = "read-meeting-details",
        description = "逐会进详情读取预定/实际时间与组织人并推送 meetingInfoList（无会议不推送）",
    ) { driver ->
        val meetings = reader.collected.toList()
        if (meetings.isEmpty()) {
            return@PlanStep StepOutcome.Success // 指定日期无会议安排，无需回调
        }
        val rows = mutableListOf<JsonObject>()
        for (name in meetings) {
            val item = driver.currentRoot()?.let { rules.findByText(it, name) }
                ?: return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "会议条目「$name」在列表页不可见（翻页后超出首屏？）",
                )
            if (!driver.click(item)) {
                return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.ACTION_REJECTED,
                    "点击会议「$name」被界面拒绝",
                )
            }
            val detailRoot = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "会议详情页") { root ->
                if (rules.find(root, WeworkSelectorIds.MEETING_DETAIL_SCHEDULED_TIME) != null) root else null
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "会议「$name」详情页未出现（详情族选择器 NOT_CALIBRATED 占位或界面不匹配）",
            )
            rows += buildJsonObject {
                put("meetingName", name)
                put(
                    "meetingTime",
                    rules.find(detailRoot, WeworkSelectorIds.MEETING_DETAIL_SCHEDULED_TIME)?.text?.trim().orEmpty(),
                )
                put(
                    "actualMeetingTime",
                    rules.find(detailRoot, WeworkSelectorIds.MEETING_DETAIL_ACTUAL_TIME)?.text?.trim().orEmpty(),
                )
                put(
                    "orgName",
                    rules.find(detailRoot, WeworkSelectorIds.MEETING_DETAIL_ORGANIZER)?.text?.trim().orEmpty(),
                )
            }
            driver.goBack() // 回会议列表，继续下一会
        }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("date", intent.date)
                put("meetingInfoList", JsonArray(rows))
            },
        )
        StepOutcome.Success
    }

    private companion object {
        /** 翻页护栏（单日会议量小；507/508 全量=50，此处 10 足够）。 */
        const val MAX_PAGES = 10
    }
}
