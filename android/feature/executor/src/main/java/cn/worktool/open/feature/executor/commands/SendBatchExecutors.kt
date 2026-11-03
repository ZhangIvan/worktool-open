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

/**
 * 指令 229 SEND_MULTI_MESSAGE 批量发送·逐条（docs/10 B2，L1；FR-53 白名单+限速归
 * 服务端治理，端侧执行前复检风险级）：逐接收人进房一次 → 按序逐条构造发送。
 *
 * 成果矩阵 = 接收人 × 消息序 逐格（**消息级失败继续后续**）。文本类（content
 * 非空）经输入框；文件类载荷（fileUrl/objectName）构造通道待 218 文件面真机
 * 取证接入——当前版本按格记败 fail-closed，不虚构发送。
 */
class SendMultiMessageExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.SendMultiMessage>() {

    override val commandType: Int get() = MessageTypes.SEND_MULTI_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.SendMultiMessage): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=逐条, receivers=${intent.receivers.size}, messages=${intent.messages.size}, " +
                "extraText.length=${intent.extraText?.length ?: 0}",
            steps = intent.receivers.flatMap { receiver -> stepsPerReceiver(flow, intent, receiver) } +
                flow.emitStep(),
        )
    }

    private fun stepsPerReceiver(
        flow: TargetFlow,
        intent: CommandIntent.SendMultiMessage,
        receiver: String,
    ): List<PlanStep> =
        listOf(
            flow.guardedStep("open-$receiver", "定位会话「$receiver」并进入聊天页（本接收人仅进房一次）") { driver ->
                flow.beginTarget(receiver)
                flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, receiver))
            },
            flow.guardedStep("await-chat-$receiver", "等待聊天页就绪") { driver ->
                flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
            },
        ) + intent.messages.mapIndexed { index, message ->
            // 格级步不加守卫：单条失败继续后续（消息级循环语义）
            PlanStep(
                id = "send-${index + 1}-$receiver",
                description = "向「$receiver」按序发送第 ${index + 1} 条消息（失败继续后续）",
            ) { driver ->
                val cell = "${index + 1}"
                if (flow.failed) {
                    // 进房级失败：本接收人全格记进房失败码，不触碰界面
                    flow.failCell(cell, ExecutorErrorCodes.NODE_NOT_FOUND, "会话「$receiver」未进入（进房级失败）")
                    return@PlanStep StepOutcome.Success
                }
                val text = message.content?.takeIf { it.isNotBlank() }
                if (text == null) {
                    flow.failCell(
                        cell,
                        ExecutorErrorCodes.PAYLOAD_INVALID,
                        "第 $cell 条消息无文本内容（textType=${message.textType}，文件类载荷构造通道待真机取证接入）",
                    )
                } else {
                    val outcome = ChatFlowSteps.inputAndSend(driver, rules, text)
                    if (outcome is StepOutcome.Failure) {
                        flow.failCell(cell, outcome.errorCode, outcome.reason)
                    } else {
                        flow.recordCell(cell, succeeded = true)
                    }
                }
                StepOutcome.Success
            }
        }
}

/**
 * 指令 230 SEND_MERGE_MESSAGE 合并发送（docs/10 B2，L1）：
 * 逐接收人：进房一次 → 构造合并聊天记录卡片（多条正文合并为一卡）→ 发送。
 * 合并卡片的真机构造路径（聊天记录选择流）待 W-M5R 取证——JVM 阶段按
 * 「合并正文一次输入发送」等价建模（接收侧=1 条消息，与 229 逐条 N 条分流）。
 */
class SendMergeMessageExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.SendMergeMessage>() {

    override val commandType: Int get() = MessageTypes.SEND_MERGE_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.SendMergeMessage): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        val mergeBody = intent.messages.mapIndexedNotNull { index, message ->
            message.content?.takeIf { it.isNotBlank() }?.let { "「${index + 1}」$it" }
        }.joinToString("\n")
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=合并, receivers=${intent.receivers.size}, messages=${intent.messages.size}, " +
                "mergedBody.length=${mergeBody.length}",
            steps = intent.receivers.flatMap { receiver ->
                listOf(
                    flow.guardedStep("open-$receiver", "定位会话「$receiver」并进入聊天页") { driver ->
                        flow.beginTarget(receiver)
                        flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, receiver))
                    },
                    flow.guardedStep("await-chat-$receiver", "等待聊天页就绪") { driver ->
                        flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
                    },
                    flow.guardedStep("send-merge-$receiver", "构造合并聊天记录（${intent.messages.size} 条）并发送") { driver ->
                        if (mergeBody.isBlank()) {
                            flow.failTarget(ExecutorErrorCodes.PAYLOAD_INVALID, "合并载荷无可发送文本")
                        } else {
                            flow.record(ChatFlowSteps.inputAndSend(driver, rules, mergeBody))
                        }
                        StepOutcome.Success
                    },
                    cn.worktool.open.feature.executor.exec.PlanStep(id = "finish-$receiver", description = "记录「$receiver」格结果") { _ ->
                        flow.finishTarget()
                        StepOutcome.Success
                    },
                )
            } + flow.emitStep(),
        )
    }
}
