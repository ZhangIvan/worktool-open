/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.PaginatedListReader
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 507 GET_ALL_FRIEND_INFO 获取全部好友信息（docs/10 B2，L0）：
 * 通讯录 →「我的客户」→ 分页去重采集全量名称 → nameList 推送
 * （采集范围=外部客户，不含内部同事；与 508 群聊列表对称）。
 */
class GetAllFriendInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetAllFriendInfo>() {

    override val commandType: Int get() = MessageTypes.GET_ALL_FRIEND_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetAllFriendInfo): ExecutionPlan {
        val reader = PaginatedListReader(
            rules = rules,
            containerSelector = WeworkSelectorIds.MY_CUSTOMERS_LIST_CONTAINER,
            itemSelector = WeworkSelectorIds.MY_CUSTOMERS_LIST_ITEM,
            what = "我的客户列表",
        )
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "maxPages=${DEFAULT_MAX_PAGES}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openMyCustomersStep(),
            ) + reader.pageSteps(DEFAULT_MAX_PAGES, "collect-page") + listOf(
                emitStep(reader),
            ),
        )
    }

    private fun openMyCustomersStep(): PlanStep = PlanStep(
        id = "open-my-customers",
        description = "进入通讯录「我的客户」列表页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「我的客户」入口") { root ->
            rules.find(root, WeworkSelectorIds.MY_CUSTOMERS_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「我的客户」入口未出现（外部联系人面未开通？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「我的客户」入口")
    }

    private fun emitStep(reader: PaginatedListReader): PlanStep = PlanStep(
        id = "emit-result",
        description = "输出全量客户名 nameList 至结构化结果端口",
    ) { _ ->
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("nameList", kotlinx.serialization.json.JsonArray(reader.collected.toList().map { JsonPrimitive(it) }))
            },
        )
        StepOutcome.Success
    }

    private companion object {
        const val DEFAULT_MAX_PAGES = 50
    }
}

/**
 * 指令 508 GET_ALL_GROUP_INFO 获取全部群信息（docs/10 B2，L0）：
 * 两段式——①「群聊」列表全量分页采集群名；②逐群进入详情读取
 * （群名/群主/公告/成员列表）→ **每群独立一条结构化推送**（非打包一条）。
 *
 * 段②的群集合运行期才可得 → 单步内循环逐群读取（只读零副作用；暂停门粒度=
 * 步边界，副作用面为零符合 FR-52 语义）。逐群进房失败 → 该群记 failList 推送
 * （容错不中断其余群）。
 */
class GetAllGroupInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetAllGroupInfo>() {

    override val commandType: Int get() = MessageTypes.GET_ALL_GROUP_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetAllGroupInfo): ExecutionPlan {
        val reader = PaginatedListReader(
            rules = rules,
            containerSelector = WeworkSelectorIds.GROUP_LIST_CONTAINER,
            itemSelector = WeworkSelectorIds.GROUP_LIST_ITEM,
            what = "群聊列表",
        )
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "maxPages=${DEFAULT_MAX_PAGES}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openGroupListStep(),
            ) + reader.pageSteps(DEFAULT_MAX_PAGES, "collect-page") + listOf(
                readEachGroupStep(reader),
            ),
        )
    }

    private fun openGroupListStep(): PlanStep = PlanStep(
        id = "open-group-list",
        description = "进入通讯录「群聊」列表页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「群聊」入口") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_CHAT_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「群聊」入口未出现（通讯录页未就绪？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「群聊」入口")
    }

    /** 段②：逐群读详情 → 每群一条推送；单群失败 → 该群 failList 推送并继续。 */
    private fun readEachGroupStep(reader: PaginatedListReader): PlanStep = PlanStep(
        id = "read-each-group",
        description = "逐群进入详情读取并每群独立推送（共 ${reader.collected.size} 群，运行期计数）",
    ) { driver ->
        val groups = reader.collected.toList()
        var failed = 0
        for (group in groups) {
            val node = driver.currentRoot()?.let { rules.findByText(it, group) }
            if (node == null || !driver.click(node)) {
                failed++
                pushGroupResult(group, succeeded = false, reason = "未能进入群详情（已退出或不可搜索）")
                continue
            }
            val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群详情页") {
                if (rules.find(it, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE) != null) it else null
            }
            if (root == null) {
                failed++
                pushGroupResult(group, succeeded = false, reason = "群详情页未就绪")
                driver.goBack()
                continue
            }
            val owner = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_OWNER)?.text.orEmpty()
            val announcement = rules.find(root, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_VALUE)?.text.orEmpty()
            val members = rules.findAll(root, WeworkSelectorIds.GROUP_MEMBER_LIST_ITEM)
                .mapNotNull { it.text?.trim() }
                .filter { it.isNotEmpty() }
            structured.onStructuredResult(
                commandType,
                buildJsonObject {
                    put("groupName", group)
                    put("groupOwner", owner)
                    put("groupAnnouncement", announcement)
                    put("memberList", kotlinx.serialization.json.JsonArray(members.map { JsonPrimitive(it) }))
                    put("memberCount", members.size)
                },
            )
            driver.goBack()
        }
        if (groups.isEmpty()) {
            StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "群聊列表为空（未找到群聊列表族）")
        } else {
            StepOutcome.Success
        }
    }

    private fun pushGroupResult(group: String, succeeded: Boolean, reason: String) {
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", group)
                put("succeeded", succeeded)
                put("reason", reason)
            },
        )
    }

    private companion object {
        const val DEFAULT_MAX_PAGES = 50
    }
}
