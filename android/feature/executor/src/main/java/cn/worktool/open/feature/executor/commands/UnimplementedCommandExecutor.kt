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
import cn.worktool.open.feature.executor.risk.RiskLevel

/**
 * 指令 215/216/217（防骚扰规则组）/231/232（语音·视频通话发起）——
 * **双侧皆无实现**（docs/10 §B B3 行 + apk-resweep：APK v3.9.1.5 分发器无分支、
 * apifox 无端点页；231/232 已证能力=来电事件回调+自动拒接，属 FR-51 收向面）。
 *
 * 我方语义（排除性冒烟）：注册占位执行器，收到即产**显式「指令未实现」回执**
 * （[ExecutorErrorCodes.COMMAND_NOT_IMPLEMENTED]；上游 101012 映射归 T3.2 网关
 * 冻结）——不静默丢弃（上游行为=消息被忽略无回执，我方显式优于静默）。
 *
 * 干跑：计划仍可产出（报告展示将回「未实现」），真实执行恒 FAILED 零界面动作。
 * 风险级（FR-53）：L1（docs/10 B3 行；未实现不降级，fail-closed）。
 *
 * 一类多实例：同一执行器类按指令号注册五次（registry 键=commandType）。
 */
class UnimplementedCommandExecutor(
    override val commandType: Int,
) : TypedCommandExecutor<CommandIntent.UnimplementedCall>() {

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.UnimplementedCall): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "notImplemented=true（N/A-APK + N/A-Doc 双侧皆无实现）",
            steps = listOf(
                PlanStep(
                    id = "reply-not-implemented",
                    description = "产出显式「指令未实现」回执（不驱动 UI、不静默丢弃）",
                ) { _ ->
                    StepOutcome.Failure(
                        ExecutorErrorCodes.COMMAND_NOT_IMPLEMENTED,
                        "指令 $commandType 未实现（docs/10 §B：双侧皆无实现，显式回执防误排期）",
                    )
                },
            ),
        )
}
