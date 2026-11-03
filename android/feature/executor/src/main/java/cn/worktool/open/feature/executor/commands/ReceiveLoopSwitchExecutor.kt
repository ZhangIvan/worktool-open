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
import cn.worktool.open.feature.executor.ports.ReceiveLoopPort
import cn.worktool.open.feature.executor.risk.RiskLevel

/**
 * 指令 202 LOOP_RECEIVE_NEW_MESSAGE（docs/10 B1：新消息接收主循环开关）：
 * 把 FR-01 新消息接收主循环置为目标态（开/关），开关落位经 [ReceiveLoopPort]
 * （生产=DeviceSessionLoop 接线；JVM=Fake 断言）。
 *
 * 载荷缺省 = 拒绝（fail-closed：开关型指令不允许猜默认态，避免误开/误关）。
 *
 * 风险级（FR-53）：L0（本地循环开关，无企微副作用）。
 */
class ReceiveLoopSwitchExecutor(
    private val loop: ReceiveLoopPort,
) : TypedCommandExecutor<CommandIntent.ReceiveLoopSwitch>() {

    override val commandType: Int get() = MessageTypes.LOOP_RECEIVE_NEW_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.ReceiveLoopSwitch): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "enabled=${intent.enabled}",
            steps = listOf(applySwitchStep(intent)),
        )

    private fun applySwitchStep(intent: CommandIntent.ReceiveLoopSwitch): PlanStep = PlanStep(
        id = "apply-loop-switch",
        description = "把新消息接收主循环置为「${if (intent.enabled) "开启" else "关闭"}」",
    ) { _ ->
        if (loop.setReceiveLoopEnabled(intent.enabled)) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "主循环开关设置被拒（循环组件不可用或目标态冲突）",
            )
        }
    }
}
