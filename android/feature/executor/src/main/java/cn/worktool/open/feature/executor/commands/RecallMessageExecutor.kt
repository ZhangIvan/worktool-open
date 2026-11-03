/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 226 RECALL_MESSAGE 消息撤回（docs/10 B2，L1）：
 * 逐目标会话：按 originalContent 定位机器人近期所发消息 → 长按「撤回」→
 * 校验撤回提示上屏。
 *
 * 语义边界（契约要点）：仅撤**机器人所发**消息；外部联系人/外部群消息存在
 * 2 分钟撤回窗（超窗 = 界面无「撤回」菜单项 → STEP_TIMEOUT 记败，不重试）。
 *
 * 风险级（FR-53）：L1（docs/10 B2 行 226=L1）。
 */
class RecallMessageExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.RecallMessage>() {

    override val commandType: Int get() = MessageTypes.RECALL_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.RecallMessage): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, originalContent.length=${intent.originalContent.length}, " +
                "textType=${intent.textType}",
            steps = intent.targets.flatMap { stepsFor(flow, intent, it) } + flow.emitStep(),
        )
    }

    private fun stepsFor(
        flow: TargetFlow,
        intent: CommandIntent.RecallMessage,
        target: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(CommonSteps.openConversation(rules, target).action(driver))
        },
        flow.guardedStep("locate-$target", "按原文定位机器人所发待撤回消息") { driver ->
            val bubble = driver.currentRoot()
                ?.let { ChatFlowSteps.locateBubble(it, rules, null, intent.originalContent) }
            if (bubble == null) {
                flow.failTarget(ExecutorErrorCodes.NODE_NOT_FOUND, "会话内未找到待撤回消息（原文长度=${intent.originalContent.length}）")
            }
            StepOutcome.Success
        },
        flow.guardedStep("recall-$target", "长按消息并点击「撤回」") { driver ->
            val bubble = driver.currentRoot()
                ?.let { ChatFlowSteps.locateBubble(it, rules, null, intent.originalContent) }
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "待撤回消息已不可见")
            flow.record(
                ChatFlowSteps.longPressMenu(driver, rules, timeouts, bubble, WeworkSelectorIds.CHAT_MENU_RECALL, "撤回"),
            )
        },
        flow.guardedStep("verify-recall-$target", "校验撤回提示上屏") { driver ->
            val appeared = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "撤回提示") { root ->
                rules.findTextContains(root, "撤回了一条消息")
            }
            if (appeared == null) {
                flow.failTarget(ExecutorErrorCodes.VERIFY_FAILED, "撤回后未见系统撤回提示（可能超撤回时间窗）")
            }
            StepOutcome.Success
        },
        cn.worktool.open.feature.executor.exec.PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )
}
