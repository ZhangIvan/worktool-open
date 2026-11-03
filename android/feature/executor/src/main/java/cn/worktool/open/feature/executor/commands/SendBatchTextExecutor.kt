/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules

/**
 * 指令 238 SEND_BATCH_TEXT_MESSAGE 批量连续文本（docs/10 B2，L1；R2 复核采纳：
 * 结果=多目标×有序文本**二维矩阵**，逐格成败，不得压平）：
 * 每目标会话**仅进房一次**，批内每条只做输入+发送（单条失败继续后续）；
 * atList 前缀逐条生效（群聊目标；单聊目标静默忽略归服务端载荷层）。
 *
 * 风险级（FR-53）：L1（批量发送族，FR-53 L1 定义「批量发送」；对齐 229 分级）。
 */
class SendBatchTextExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.SendBatchText>() {

    override val commandType: Int get() = ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.SendBatchText): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        val atPrefix = if (intent.atList.isEmpty()) "" else intent.atList.joinToString(" ") { "@$it" } + " "
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, texts=${intent.texts.size}, at=${intent.atList.size}",
            steps = intent.targets.flatMap { target -> stepsPerTarget(flow, intent, target, atPrefix) } +
                flow.emitStep(),
        )
    }

    private fun stepsPerTarget(
        flow: TargetFlow,
        intent: CommandIntent.SendBatchText,
        target: String,
        atPrefix: String,
    ): List<PlanStep> =
        listOf(
            flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页（本目标仅进房一次）") { driver ->
                flow.beginTarget(target)
                flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
            },
            flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
                flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
            },
        ) + intent.texts.mapIndexed { index, text ->
            // 格级步不加守卫：单条失败继续后续（契约「单条失败继续后续」）
            PlanStep(
                id = "send-${index + 1}-$target",
                description = "向「$target」发送第 ${index + 1} 条文本（失败继续后续）",
            ) { driver ->
                val cell = "${index + 1}"
                if (flow.failed) {
                    flow.failCell(cell, cn.worktool.open.feature.executor.exec.ExecutorErrorCodes.NODE_NOT_FOUND, "会话「$target」未进入（进房级失败）")
                    return@PlanStep StepOutcome.Success
                }
                val outcome = ChatFlowSteps.inputAndSend(driver, rules, "$atPrefix$text")
                if (outcome is StepOutcome.Failure) {
                    flow.failCell(cell, outcome.errorCode, outcome.reason)
                } else {
                    flow.recordCell(cell, succeeded = true)
                }
                StepOutcome.Success
            }
        }
}
