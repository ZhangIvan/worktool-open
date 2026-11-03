/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.ports.RemoteFilePort
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules

/**
 * 指令 210 PUSH_MICROPROGRAM / 211 PUSH_OFFICE（docs/10 §B B4，L0/L0）：
 * 卡片式推送（标题+描述+跳转 URL[+icon]）——R6 终局决策的 UI 自动化等价路径
 * （原 WWAPI 桥剥离）。两指令厂商均无公开 schema → FR-34 冒烟范围注记：仅契约
 * 自拟；**211 厂商已废弃（企微≥4.1.32 失效）**——我方等价实现保通道，真机验收
 * 需登记版本差异。复用 224 链接卡片执行流（同形载荷，仅指令号/描述面不同）。
 */
abstract class PushCardExecutorBase<I : CommandIntent>(
    private val commandTypeValue: Int,
    private val kindLabel: String,
    private val delegate: PushLinkExecutor,
) : TypedCommandExecutor<I>() {

    final override val commandType: Int get() = commandTypeValue

    protected fun plan(
        intent: I,
        targets: List<String>,
        title: String,
        description: String?,
        url: String,
        iconUrl: String?,
        targetDescription: String,
    ): ExecutionPlan {
        val linkIntent = CommandIntent.PushLink(
            targets = targets,
            title = title,
            description = description ?: title,
            url = url,
            iconUrl = iconUrl,
        )
        val plan = delegate.buildPlan(linkIntent)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = targetDescription,
            paramsSummary = "kind=$kindLabel, " + plan.paramsSummary,
            steps = plan.steps,
        )
    }
}

/** 210 推小程序卡片（R6：UI 自动化等价；自拟 schema）。 */
class PushMicroprogramExecutor(
    remoteFiles: RemoteFilePort,
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : PushCardExecutorBase<CommandIntent.PushMicroprogram>(
    commandTypeValue = MessageTypes.PUSH_MICROPROGRAM,
    kindLabel = "microprogram",
    delegate = PushLinkExecutor(remoteFiles, sink, rules, timeouts),
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.PushMicroprogram): ExecutionPlan =
        plan(
            intent,
            intent.targets,
            intent.title,
            intent.description,
            intent.pageUrl,
            intent.iconUrl,
            intent.targetDescription,
        )
}

/** 211 推腾讯文档卡片（厂商已废弃企微≥4.1.32；我方等价实现保通道）。 */
class PushOfficeExecutor(
    remoteFiles: RemoteFilePort,
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : PushCardExecutorBase<CommandIntent.PushOffice>(
    commandTypeValue = MessageTypes.PUSH_OFFICE,
    kindLabel = "office",
    delegate = PushLinkExecutor(remoteFiles, sink, rules, timeouts),
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.PushOffice): ExecutionPlan =
        plan(
            intent,
            intent.targets,
            intent.title,
            intent.description,
            intent.docUrl,
            intent.iconUrl,
            intent.targetDescription,
        )
}
