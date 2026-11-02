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
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.PaginatedListReader
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 501 GET_GROUP_INFO（FR-28 群列表查询；docs/05 P0 首批行 501/502）：
 * 通讯录 Tab →「群聊」→ 分页读取至收敛 → 结构化结果经 [QueryResultSink] 输出。
 *
 * 只读指令（零副作用：点击导航 + 滚动 + 文本读取）→ 风险级 L0
 * （FR-53 裁决③「其余已实现指令=L0」；docs/05 行备注「Schema 校验」 =
 * 回执映射以 T3.2 apifox 冻结为准）。
 *
 * 结果边界：群聊列表页只可得群显示名——GroupList 即名字序（owner/成员数等
 * 富结构不虚构，待冻结）；真实执行结束**不回退导航**（留在群聊页，由上层
 * 指令序列决定去向；201 STOP_AND_GO_HOME 负责归位）。
 */
class GetGroupListExecutor(
    private val sink: QueryResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetGroupList>() {

    override val commandType: Int get() = MessageTypes.GET_GROUP_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetGroupList): ExecutionPlan {
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
            paramsSummary = "maxPages=${intent.maxPages}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openGroupListStep(),
            ) + reader.pageSteps(intent.maxPages, PAGE_STEP_PREFIX) + listOf(
                emitResultStep(reader),
            ),
        )
    }

    /** 进入「群聊」列表页；入口缺失 = 通讯录未就绪，超时兜底。 */
    private fun openGroupListStep(): PlanStep = PlanStep(
        id = "open-group-list",
        description = "进入通讯录「群聊」列表页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「群聊」入口") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_CHAT_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「群聊」入口未在 ${timeouts.awaitNodeMs}ms 内出现（通讯录页未就绪？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「群聊」入口")
    }

    /** 末步：结构化结果出端口（FAILED/PAUSED 路径不会到达本步 → 不外发部分结果）。 */
    private fun emitResultStep(reader: PaginatedListReader): PlanStep = PlanStep(
        id = "emit-result",
        description = "输出结构化群列表结果至查询结果端口",
    ) { _ ->
        sink.onQueryResult(commandType, QueryResult.GroupList(reader.collected.toList()))
        StepOutcome.Success
    }

    private companion object {
        /** 分页步骤 ID 前缀（`collect-page-N`；报告/测试引用）。 */
        const val PAGE_STEP_PREFIX = "collect-page"
    }
}
