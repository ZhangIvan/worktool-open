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
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 203 SEND_MESSAGE（FR-02 发送消息）：
 * 会话列表定位 → 聊天页就绪 → 输入文本 → 发送 → 上屏校验。
 *
 * 风险级（FR-53）：L0（单条发送；「批量发送」属 L1，对应指令 229，不在 P0 子集）。
 */
class SendTextExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.SendText>() {

    override val commandType: Int get() = MessageTypes.SEND_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.SendText): ExecutionPlan {
        val at = if (intent.atList.isEmpty()) "" else "@${intent.atList.joinToString(" @")}"
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetRoom,
            paramsSummary = "content.length=${intent.content.length}, at=${intent.atList.size}$at",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.targetRoom),
                CommonSteps.awaitChatReady(rules, timeouts),
                inputTextStep(intent),
                sendStep(),
                verifySentStep(intent),
            ),
        )
    }

    private fun inputTextStep(intent: CommandIntent.SendText): PlanStep = PlanStep(
        id = "input-message",
        description = "在输入框输入消息文本（${intent.content.length} 字${atSuffix(intent)}）",
    ) { driver ->
        val input = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.CHAT_INPUT) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "聊天页输入框缺失",
            )
        if (!driver.click(input)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "聚焦输入框被界面拒绝",
            )
        }
        val text = if (intent.atList.isEmpty()) {
            intent.content
        } else {
            intent.atList.joinToString(" ") { "@$it" } + " " + intent.content
        }
        if (!driver.inputText(input, text)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入文本被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    private fun sendStep(): PlanStep = PlanStep(
        id = "send-message",
        description = "点击发送按钮",
    ) { driver ->
        val send = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.CHAT_SEND) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "发送按钮缺失（输入后未出现）",
            )
        if (!driver.click(send)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击发送按钮被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    private fun verifySentStep(intent: CommandIntent.SendText): PlanStep = PlanStep(
        id = "verify-sent",
        description = "校验消息已上屏（聊天记录出现该文本）",
    ) { driver ->
        val appeared = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "消息上屏") { root ->
            rules.findTextContains(root, intent.content)
        }
        if (appeared == null) {
            // SR-06 日志脱敏：失败原因不回显消息正文（仅长度元数据）
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "发送后聊天记录未见该消息（content.length=${intent.content.length}）",
            )
        }
        StepOutcome.Success
    }

    private fun atSuffix(intent: CommandIntent.SendText): String =
        if (intent.atList.isEmpty()) "" else "，@${intent.atList.size} 人"
}
