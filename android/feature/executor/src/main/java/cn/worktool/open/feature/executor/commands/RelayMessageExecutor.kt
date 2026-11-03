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
 * 指令 205 RELAY_MESSAGE 转发单条消息（docs/10 B2，L1）：
 * 逐（源会话 × 接收人）：定位源消息 → 长按「转发」→ 选择接收人 → 确认转发。
 *
 * 粒度说明：成果矩阵 = 源会话×接收人 逐格；真机多选转发面板（一次多选多人）的
 * 校准归 W-M5R 真机取证——JVM 阶段按逐接收人独立流建模（同一菜单流，语义等价）。
 * extraText 附加留言的输入位待真机取证落位（当前版本不虚构输入步）。
 *
 * 风险级（FR-53）：L1（docs/10 B2 行 205=L1：转发扩散面）。
 */
class RelayMessageExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.RelayMessage>() {

    override val commandType: Int get() = MessageTypes.RELAY_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.RelayMessage): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        val pairs = intent.sourceRooms.flatMap { room -> intent.receivers.map { room to it } }
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "rooms=${intent.sourceRooms.size}, receivers=${intent.receivers.size}, " +
                "quoted=${intent.quotedSender}/${intent.quotedContent.length}, textType=${intent.quotedTextType}, " +
                "extraText.length=${intent.extraText?.length ?: 0}",
            steps = pairs.flatMap { (room, receiver) -> stepsFor(flow, intent, room, receiver) } +
                flow.emitStep(),
        )
    }

    private fun stepsFor(
        flow: TargetFlow,
        intent: CommandIntent.RelayMessage,
        room: String,
        receiver: String,
    ): List<cn.worktool.open.feature.executor.exec.PlanStep> {
        val target = "$room→$receiver"
        return listOf(
            flow.guardedStep("open-$target", "定位源会话「$room」并进入聊天页") { driver ->
                flow.beginTarget(target)
                flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, room))
            },
            flow.guardedStep("locate-$target", "按发送人「${intent.quotedSender}」+原文定位待转发消息") { driver ->
                val bubble = driver.currentRoot()
                    ?.let { ChatFlowSteps.locateBubble(it, rules, intent.quotedSender, intent.quotedContent) }
                if (bubble == null) {
                    flow.failTarget(
                        ExecutorErrorCodes.NODE_NOT_FOUND,
                        "源会话未找到待转发消息（发送人=${intent.quotedSender}）",
                    )
                }
                StepOutcome.Success
            },
            flow.guardedStep("longpress-relay-$target", "长按消息并点击「转发」") { driver ->
                val bubble = driver.currentRoot()
                    ?.let { ChatFlowSteps.locateBubble(it, rules, intent.quotedSender, intent.quotedContent) }
                    ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "待转发消息已不可见")
                flow.record(
                    ChatFlowSteps.longPressMenu(driver, rules, timeouts, bubble, WeworkSelectorIds.CHAT_MENU_RELAY, "转发"),
                )
            },
            flow.guardedStep("pick-$target", "在转发面板选择接收人「$receiver」") { driver ->
                val item = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "接收人「$receiver」") { root ->
                    rules.findByText(root, receiver)
                } ?: return@guardedStep flow.failAsGuarded(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "转发面板未见接收人「$receiver」（非机器人可见联系人？）",
                )
                flow.record(CommonSteps.clickOrReject(driver, item, "接收人「$receiver」"))
            },
            flow.guardedStep("confirm-$target", "确认转发给「$receiver」") { driver ->
                val confirm = driver.currentRoot()
                    ?.let { rules.find(it, WeworkSelectorIds.RELAY_PICKER_CONFIRM) }
                    ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "转发确认按钮缺失")
                flow.record(CommonSteps.clickOrReject(driver, confirm, "转发确认"))
            },
            cn.worktool.open.feature.executor.exec.PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
                flow.finishTarget()
                StepOutcome.Success
            },
        )
    }
}
