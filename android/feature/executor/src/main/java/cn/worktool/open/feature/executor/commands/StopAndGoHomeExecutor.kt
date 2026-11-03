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
import cn.worktool.open.feature.executor.ports.SessionControlPort
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 201 STOP_AND_GO_HOME（docs/10 B1：停止任务回首页；FR-52 接管联动——
 * 暂停请求 + 回首页语义并入同一执行器）：
 * 请求用户接管暂停 → 逐层返回企微消息首页 → 等待首页就绪。
 *
 * 风险级（FR-53）：L0（导航归位 + 暂停请求，无业务副作用；未入 L1/L2 基线表）。
 */
class StopAndGoHomeExecutor(
    private val session: SessionControlPort,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.StopAndGoHome>() {

    override val commandType: Int get() = MessageTypes.STOP_AND_GO_HOME

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.StopAndGoHome): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = intent.reason?.let { "reason.length=${it.length}" } ?: "reason=未携带",
            steps = listOf(
                requestPauseStep(intent),
                goHomeStep(),
                awaitHomeReadyStep(),
            ),
        )

    /** FR-52 联动：请求接管暂停（被拒 = 存在更高优先级状态，fail-closed 不再归位）。 */
    private fun requestPauseStep(intent: CommandIntent.StopAndGoHome): PlanStep = PlanStep(
        id = "request-takeover-pause",
        description = "请求用户接管暂停（FR-52 联动，来源=${intent.reason ?: "cloud-201"}）",
    ) { _ ->
        if (!session.requestTakeoverPause(intent.reason ?: "cloud-201")) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ILLEGAL_OPERATION,
                "接管暂停请求被拒（已有更高优先级接管态？未执行归位）",
            )
        }
        StepOutcome.Success
    }

    private fun goHomeStep(): PlanStep = PlanStep(
        id = "go-home",
        description = "逐层返回至企微消息首页（已在首页则空过）",
    ) { driver -> ChatFlowSteps.goHome(driver, rules) }

    private fun awaitHomeReadyStep(): PlanStep = PlanStep(
        id = "await-home-ready",
        description = "等待消息首页就绪（底部「通讯录」Tab 可见，超时 ${timeouts.awaitNodeMs}ms）",
    ) { driver ->
        val ready = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "消息首页 Tab 栏") { root ->
            rules.find(root, WeworkSelectorIds.CONTACTS_TAB)
        }
        if (ready == null) {
            StepOutcome.Failure(
                ExecutorErrorCodes.STEP_TIMEOUT,
                "返回后首页 Tab 栏未在 ${timeouts.awaitNodeMs}ms 内出现",
            )
        } else {
            StepOutcome.Success
        }
    }
}
