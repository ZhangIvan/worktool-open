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
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.intent.MessageEvent
import cn.worktool.open.feature.executor.intent.MessageOrigin
import cn.worktool.open.feature.executor.report.MessageReportSink
import cn.worktool.open.feature.executor.report.toReportBean
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 283 DO_RECEIVE_NEW_MESSAGE（docs/10 B1：触发一次新消息接收上报）：
 * 回消息首页 → 扫描会话列表可见条目（会话名 + 最近一条预览文本）→
 * 逐条组装 [MessageEvent]（origin=CONVERSATION_LIST）→ 经 [MessageReportSink]
 * 以 **101 信封**上报（socketType=2，线形态与 FR-01 被动上报同构）。
 *
 * 与 [cn.worktool.open.feature.executor.report.MessageEventReporter] 的分工：
 * reporter=无障碍事件驱动的被动面（通知栏+会话列表双通道）；本执行器=云端/调度
 * 主动触发的补拉面（单轮、可见即报，不猜未读态）。
 *
 * 风险级（FR-53）：L0（只读扫描 + 上报，无企微副作用）。
 */
class TriggerReceiveNewMessageExecutor(
    private val sink: MessageReportSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
) : TypedCommandExecutor<CommandIntent.TriggerReceiveNewMessage>() {

    override val commandType: Int get() = ExtendedCommandTypes.DO_RECEIVE_NEW_MESSAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    /** 本轮扫描到的事件（scan 步骤填充，emit 步骤消费）。 */
    private val scanned = ArrayList<MessageEvent>()

    override fun buildTypedPlan(intent: CommandIntent.TriggerReceiveNewMessage): ExecutionPlan {
        scanned.clear()
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "maxConversations=${intent.maxConversations}",
            steps = listOf(
                goHomeStep(),
                scanStep(intent.maxConversations),
                emitReportStep(),
            ),
        )
    }

    private fun goHomeStep(): PlanStep = PlanStep(
        id = "go-home",
        description = "返回企微消息首页（会话列表所在页）",
    ) { driver -> ChatFlowSteps.goHome(driver, rules) }

    /** 读取可见会话条目：会话名 + 同行预览文本（无可读正文者跳过，不虚构内容）。 */
    private fun scanStep(max: Int): PlanStep = PlanStep(
        id = "scan-conversations",
        description = "扫描会话列表可见条目并组装新消息事件（上限 $max 条）",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "界面根节点不可得")
        val names = rules.findAll(root, WeworkSelectorIds.CONVERSATION_ITEM_NAME)
        for (nameNode in names) {
            if (scanned.size >= max) break
            val room = nameNode.text?.trim().orEmpty()
            if (room.isEmpty()) continue
            val preview = ChatFlowSteps.siblingSenderText(nameNode)
            if (preview.isNullOrBlank()) continue // 无可读正文：跳过（不虚构）
            scanned += MessageEvent(
                origin = MessageOrigin.CONVERSATION_LIST,
                targetRoom = room,
                senderNames = emptyList(),
                content = preview,
                eventTimeMillis = 0L,
            )
        }
        StepOutcome.Success
    }

    /** 末步：以 101 信封批量上报（零事件=空过成功，本轮无新消息）。 */
    private fun emitReportStep(): PlanStep = PlanStep(
        id = "emit-report",
        description = "把本轮扫描事件以 101 信封上报（0 条则空过）",
    ) { _ ->
        if (scanned.isEmpty()) return@PlanStep StepOutcome.Success
        val beans = scanned.map { it.toReportBean() }
        if (!sink.sendReport(beans)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "101 上报通道不可投递（${beans.size} 条事件未发出）",
            )
        }
        StepOutcome.Success
    }
}
