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
 * 指令 204 REPLY_MESSAGE 引用回复（docs/10 B2：会话定位 + 长按菜单流）：
 * 逐目标：定位会话 → 按发送人+原文定位被引用消息 → 长按「回复」→
 * 输入正文发送 → 上屏校验。逐目标成败经 [BatchOutcomeSink] 出端口
 * （部分成功不压平：单目标失败继续后续目标）。
 *
 * 风险级（FR-53）：L0（docs/10 B2 行 204/205=L0/L1 之 204=L0；单目标回复非批量）。
 */
class ReplyMessageExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ReplyMessage>() {

    override val commandType: Int get() = MessageTypes.REPLY_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.ReplyMessage): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, quotedSender=${intent.quotedSender}, " +
                "quotedContent.length=${intent.quotedContent.length}, " +
                "textType=${intent.quotedTextType}, content.length=${intent.content.length}",
            steps = intent.targets.flatMap { target -> stepsFor(flow, intent, target) } +
                flow.emitStep(),
        )
    }

    private fun stepsFor(
        flow: TargetFlow,
        intent: CommandIntent.ReplyMessage,
        target: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
        },
        flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
            flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
        },
        flow.guardedStep("locate-quoted-$target", "按发送人「${intent.quotedSender}」+原文定位被引用消息") { driver ->
            val bubble = driver.locateQuoted(intent)
            if (bubble == null) {
                flow.failTarget(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "未找到被引用消息（发送人=${intent.quotedSender}，原文长度=${intent.quotedContent.length}）",
                )
            }
            StepOutcome.Success
        },
        flow.guardedStep("longpress-reply-$target", "长按目标消息并点击「回复」") { driver ->
            val bubble = driver.locateQuoted(intent)
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "被引用消息已不可见")
            flow.record(ChatFlowSteps.longPressMenu(driver, rules, timeouts, bubble, WeworkSelectorIds.CHAT_MENU_REPLY, "回复"))
        },
        flow.guardedStep("send-reply-$target", "在引用输入态输入回复正文并发送") { driver ->
            flow.record(ChatFlowSteps.inputAndSend(driver, rules, intent.content))
        },
        flow.guardedStep("verify-reply-$target", "校验回复已上屏") { driver ->
            val appeared = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "回复上屏") { root ->
                rules.findTextContains(root, intent.content)
            }
            if (appeared == null) {
                flow.failTarget(ExecutorErrorCodes.VERIFY_FAILED, "回复发送后未见上屏（content.length=${intent.content.length}）")
            }
            StepOutcome.Success
        },
        cn.worktool.open.feature.executor.exec.PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )

    private fun cn.worktool.open.feature.executor.runtime.UiDriver.locateQuoted(
        intent: CommandIntent.ReplyMessage,
    ) = currentRoot()?.let { root ->
        ChatFlowSteps.locateBubble(root, rules, intent.quotedSender, intent.quotedContent)
    }
}
