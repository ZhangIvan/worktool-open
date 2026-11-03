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
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 503 GET_MY_INFO 获取我的信息（docs/10 B2，L0）：
 * 「我」页读取姓名/企业 → 结构化 myInfo 推送（详情页富字段 alias/gender/job/phone
 * 等待真机详情页取证补齐；未取到字段按空串不缺键口径，当前面=name/corporation）。
 */
class GetMyInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetMyInfo>() {

    override val commandType: Int get() = MessageTypes.GET_MY_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetMyInfo): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "fields=name,corporation",
            steps = listOf(openMeTabStep(), readAndPushStep()),
        )

    private fun openMeTabStep(): PlanStep = PlanStep(
        id = "open-me-tab",
        description = "切换到底部「我」Tab",
    ) { driver ->
        val tab = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.ME_TAB) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「我」Tab 缺失（不在主界面？）")
        CommonSteps.clickOrReject(driver, tab, "「我」Tab")
    }

    private fun readAndPushStep(): PlanStep = PlanStep(
        id = "read-and-push",
        description = "读取「我」页姓名/企业并推送 myInfo 结构化结果",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「我」页资料位") {
            if (rules.find(it, WeworkSelectorIds.ME_NAME) != null) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「我」页资料位未出现（未登录态？）",
        )
        val name = rules.find(root, WeworkSelectorIds.ME_NAME)?.text.orEmpty()
        val corp = rules.find(root, WeworkSelectorIds.ME_CORP)?.text.orEmpty()
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("name", name)
                put("corporation", corp)
                // 未取到的富字段按空串不缺键（契约口径）；真机详情页取证后补齐
                put("alias", "")
                put("gender", "")
                put("job", "")
                put("phone", "")
                put("showName", name)
                put("sumInfo", "$name $corp")
                put("workSign", "")
            },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 505 GET_RECENT_LIST 获取最近会话列表（docs/10 B2，L0）：
 * 消息首页会话条目去重采集 → messageList 推送。
 * **容器不足 2 行 → 静默不推送**（空列表/异常态不报错不推送，任务仍成功——
 * 上游特殊分支语义保持）。
 */
class GetRecentListExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
) : TypedCommandExecutor<CommandIntent.GetRecentList>() {

    override val commandType: Int get() = MessageTypes.GET_RECENT_LIST

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetRecentList): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "dedup=true",
            steps = listOf(
                PlanStep(id = "go-home", description = "返回企微消息首页（会话列表所在页）") { driver ->
                    ChatFlowSteps.goHome(driver, rules)
                },
                readAndPushStep(),
            ),
        )

    private fun readAndPushStep(): PlanStep = PlanStep(
        id = "read-and-push",
        description = "去重采集最近会话名并推送 messageList（<2 行静默不推送）",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "界面根节点不可得")
        val names = rules.findAll(root, WeworkSelectorIds.CONVERSATION_ITEM_NAME)
            .mapNotNull { it.text?.trim() }
            .filter { it.isNotEmpty() }
            .distinct()
        if (names.size < MIN_VISIBLE_ROWS) {
            return@PlanStep StepOutcome.Success // 静默不推送（空/异常态分支）
        }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("messageList", kotlinx.serialization.json.JsonArray(names.map { JsonPrimitive(it) }))
            },
        )
        StepOutcome.Success
    }

    private companion object {
        const val MIN_VISIBLE_ROWS = 2
    }
}

/**
 * 指令 506 GET_CORP_LIST 获取企业列表（docs/10 B2，L0；厂商无公开 schema →
 * FR-34 仅契约自拟）：打开消息页左上抽屉 → 读取企业列表 → corpList 推送。
 */
class GetCorpListExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetCorpList>() {

    override val commandType: Int get() = MessageTypes.GET_CORP_LIST

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetCorpList): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "source=drawer",
            steps = listOf(openDrawerStep(), readAndPushStep()),
        )

    private fun openDrawerStep(): PlanStep = PlanStep(
        id = "open-drawer",
        description = "点击消息页左上抽屉入口展开企业列表",
    ) { driver ->
        ChatFlowSteps.goHome(driver, rules).let { home ->
            if (home is StepOutcome.Failure) return@PlanStep home
        }
        val drawer = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.DRAWER_ENTRY) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "抽屉入口缺失（消息页未就绪？）")
        CommonSteps.clickOrReject(driver, drawer, "抽屉入口")
    }

    private fun readAndPushStep(): PlanStep = PlanStep(
        id = "read-and-push",
        description = "读取抽屉企业列表并推送 corpList",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "企业列表") {
            if (rules.findAll(it, WeworkSelectorIds.CORP_LIST_ITEM).isNotEmpty()) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "企业列表未出现（单企业账号或未登录态？）",
        )
        val corps = rules.findAll(root, WeworkSelectorIds.CORP_LIST_ITEM)
            .mapNotNull { it.text?.trim() }
            .filter { it.isNotEmpty() }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("corpList", kotlinx.serialization.json.JsonArray(corps.map { JsonPrimitive(it) }))
            },
        )
        StepOutcome.Success
    }
}
