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
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 227 RELAY_MULTI_MESSAGE 多条逐条转发（docs/10 B2，L1）：
 * 逐（源会话 × 接收人）：长按首条「多选」→ 逐条勾选 messageList →
 * 「逐条转发」→ 选择接收人 → 确认。接收侧 = N 条独立消息。
 */
class RelayMultiMessageExecutor(
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : RelayBatchExecutor(
    commandTypeValue = MessageTypes.RELAY_MULTI_MESSAGE,
    modeSelector = WeworkSelectorIds.RELAY_MODE_SEQUENCE,
    modeLabel = "逐条转发",
    sink = sink,
    rules = rules,
    timeouts = timeouts,
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1
}

/**
 * 指令 228 RELAY_MERGE_MESSAGE 多条合并转发（docs/10 B2，L1）：
 * 与 227 同流（多选 → 模式按钮不同）：接收侧 = 一条合并聊天记录卡片。
 */
class RelayMergeMessageExecutor(
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : RelayBatchExecutor(
    commandTypeValue = MessageTypes.RELAY_MERGE_MESSAGE,
    modeSelector = WeworkSelectorIds.RELAY_MODE_MERGE,
    modeLabel = "合并转发",
    sink = sink,
    rules = rules,
    timeouts = timeouts,
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1
}

/**
 * 227/228 共用执行体（上游同型：同一多选流，仅模式按钮不同——
 * 模式差异经 [modeSelector] 注入，计划步描述携带模式语义）。
 */
abstract class RelayBatchExecutor(
    private val commandTypeValue: Int,
    private val modeSelector: String,
    private val modeLabel: String,
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules,
    private val timeouts: StepTimeouts,
) : TypedCommandExecutor<CommandIntent.RelayBatchIntent>() {

    final override val commandType: Int get() = commandTypeValue

    final override fun buildTypedPlan(intent: CommandIntent.RelayBatchIntent): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        val pairs = intent.sourceRooms.flatMap { room -> intent.receivers.map { room to it } }
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=$modeLabel, rooms=${intent.sourceRooms.size}, " +
                "messages=${intent.messages.size}, receivers=${intent.receivers.size}",
            steps = pairs.flatMap { (room, receiver) -> stepsFor(flow, intent, room, receiver) } +
                flow.emitStep(),
        )
    }

    private fun stepsFor(
        flow: TargetFlow,
        intent: CommandIntent.RelayBatchIntent,
        room: String,
        receiver: String,
    ): List<PlanStep> {
        val target = "$room→$receiver"
        val first = intent.messages.firstOrNull()
        return listOf(
            flow.guardedStep("open-$target", "定位源会话「$room」并进入聊天页") { driver ->
                flow.beginTarget(target)
                flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, room))
            },
            flow.guardedStep("enter-multiselect-$target", "长按首条消息并点击「多选」") { driver ->
                val firstMsg = first ?: return@guardedStep flow.failAsGuarded(
                    ExecutorErrorCodes.PAYLOAD_INVALID, "messageList 为空",
                )
                val bubble = driver.currentRoot()
                    ?.let { ChatFlowSteps.locateBubble(it, rules, null, firstMsg.originalContent) }
                    ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "首条待转发消息未找到")
                flow.record(
                    ChatFlowSteps.longPressMenu(driver, rules, timeouts, bubble, WeworkSelectorIds.CHAT_MENU_MULTI_SELECT, "多选"),
                )
            },
        ) + intent.messages.mapIndexed { index, message ->
            flow.guardedStep("select-$index-$target", "勾选第 ${index + 1} 条待转发消息") { driver ->
                val node = driver.currentRoot()
                    ?.let { ChatFlowSteps.locateBubble(it, rules, null, message.originalContent) }
                    ?: return@guardedStep flow.failAsGuarded(
                        ExecutorErrorCodes.NODE_NOT_FOUND,
                        "第 ${index + 1} 条消息（原文长度=${message.originalContent.length}）未找到",
                    )
                flow.record(CommonSteps.clickOrReject(driver, node, "消息勾选"))
            }
        } + listOf(
            flow.guardedStep("mode-$modeLabel-$target", "点击「$modeLabel」模式按钮") { driver ->
                val mode = driver.currentRoot()?.let { rules.find(it, modeSelector) }
                    ?: return@guardedStep flow.failAsGuarded(
                        ExecutorErrorCodes.NODE_NOT_FOUND,
                        "「$modeLabel」按钮缺失",
                    )
                flow.record(CommonSteps.clickOrReject(driver, mode, "「$modeLabel」"))
            },
            flow.guardedStep("pick-$target", "在转发面板选择接收人「$receiver」") { driver ->
                val item = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "接收人「$receiver」") { root ->
                    rules.findByText(root, receiver)
                } ?: return@guardedStep flow.failAsGuarded(
                    ExecutorErrorCodes.NODE_NOT_FOUND, "转发面板未见接收人「$receiver」",
                )
                flow.record(CommonSteps.clickOrReject(driver, item, "接收人「$receiver」"))
            },
            flow.guardedStep("confirm-$target", "确认$modeLabel 给「$receiver」") { driver ->
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
