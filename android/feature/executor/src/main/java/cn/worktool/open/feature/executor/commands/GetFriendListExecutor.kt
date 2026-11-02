/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.PaginatedListReader
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 502 GET_FRIEND_INFO（FR-29 好友列表查询；docs/05 P0 首批行 501/502）：
 * 通讯录 Tab → 联系人列表分页读取至收敛 → 结构化结果经 [QueryResultSink] 输出。
 *
 * 与 501 差异：联系人列表就在通讯录页（无二级入口）；空列表 = 合法结果
 * （结构化空 [QueryResult.FriendList] 照常输出，非错误）。
 *
 * 只读指令 → 风险级 L0（FR-53 裁决③）。
 */
class GetFriendListExecutor(
    private val sink: QueryResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
) : TypedCommandExecutor<CommandIntent.GetFriendList>() {

    override val commandType: Int get() = MessageTypes.GET_FRIEND_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetFriendList): ExecutionPlan {
        val reader = PaginatedListReader(
            rules = rules,
            containerSelector = WeworkSelectorIds.CONTACTS_LIST_CONTAINER,
            itemSelector = WeworkSelectorIds.CONTACT_LIST_ITEM,
            what = "联系人列表",
        )
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "maxPages=${intent.maxPages}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
            ) + reader.pageSteps(intent.maxPages, PAGE_STEP_PREFIX) + listOf(
                emitResultStep(reader),
            ),
        )
    }

    private fun emitResultStep(reader: PaginatedListReader): PlanStep = PlanStep(
        id = "emit-result",
        description = "输出结构化好友列表结果至查询结果端口",
    ) { _ ->
        sink.onQueryResult(commandType, QueryResult.FriendList(reader.collected.toList()))
        StepOutcome.Success
    }

    private companion object {
        const val PAGE_STEP_PREFIX = "collect-page"
    }
}
