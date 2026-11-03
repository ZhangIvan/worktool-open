/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 208/209 PUSH_MICRO_DISK_IMAGE / PUSH_MICRO_DISK_FILE（docs/10 B2，L1；
 * 企微专属指令——微信侧 N/A，FR-56 双规则包）：
 * 文件源 = **微盘已存文件**（与 218 的 fileUrl 下载分流：本族零下载动作）：
 * 逐目标会话 →「+」→ 微盘 → 搜索 objectName → 选中 → 发送。
 */
abstract class MicroDiskPushExecutor<I : CommandIntent>(
    private val commandTypeValue: Int,
    private val kindLabel: String,
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules,
    private val timeouts: StepTimeouts,
) : TypedCommandExecutor<I>() {

    final override val commandType: Int get() = commandTypeValue

    protected fun buildPlan(
        intent: I,
        targets: List<String>,
        objectName: String,
        extraText: String?,
        targetDescription: String,
    ): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = targetDescription,
            paramsSummary = "kind=$kindLabel, targets=${targets.size}, objectName=$objectName, " +
                "extraText.length=${extraText?.length ?: 0}",
            steps = targets.flatMap { target -> stepsPerTarget(flow, target, objectName) } + flow.emitStep(),
        )
    }

    private fun stepsPerTarget(flow: TargetFlow, target: String, objectName: String): List<PlanStep> =
        listOf(
            flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
                flow.beginTarget(target)
                flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
            },
            flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
                flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
            },
            flow.guardedStep("open-micro-disk-$target", "点击「+」面板并进入「微盘」") { driver ->
                val plus = driver.currentRoot()
                    ?.let { rules.find(it, WeworkSelectorIds.CHAT_PLUS_ENTRY) }
                    ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "「+」面板入口缺失")
                if (!driver.click(plus)) {
                    return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "点击「+」被界面拒绝")
                }
                val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「微盘」入口") { root ->
                    rules.find(root, WeworkSelectorIds.MICRO_DISK_ENTRY)
                } ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "「微盘」入口未出现")
                flow.record(CommonSteps.clickOrReject(driver, entry, "「微盘」入口"))
            },
            flow.guardedStep("pick-$target", "在微盘搜索并选中「$objectName」（$kindLabel）") { driver ->
                val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "微盘搜索框") { root ->
                    rules.find(root, WeworkSelectorIds.MICRO_DISK_SEARCH_INPUT)
                } ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "微盘搜索框缺失")
                if (!driver.click(input) || !driver.inputText(input, objectName)) {
                    return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "输入微盘搜索名被界面拒绝")
                }
                val item = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "微盘条目「$objectName」") { root ->
                    rules.findByText(root, objectName)
                } ?: return@guardedStep flow.failAsGuarded(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "微盘未见「$objectName」（前置依赖：微盘已上传同名$kindLabel）",
                )
                flow.record(CommonSteps.clickOrReject(driver, item, "微盘条目「$objectName」"))
            },
            flow.guardedStep("send-$target", "点击发送微盘$kindLabel") { driver ->
                val send = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "微盘「发送」按钮") { root ->
                    rules.find(root, WeworkSelectorIds.MICRO_DISK_SEND_BUTTON)
                } ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "微盘「发送」按钮缺失")
                flow.record(CommonSteps.clickOrReject(driver, send, "微盘「发送」"))
            },
            PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
                flow.finishTarget()
                cn.worktool.open.feature.executor.exec.StepOutcome.Success
            },
        )
}

/** 208 推微盘图片。 */
class PushMicroDiskImageExecutor(
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : MicroDiskPushExecutor<CommandIntent.PushMicroDiskImage>(
    commandTypeValue = MessageTypes.PUSH_MICRO_DISK_IMAGE,
    kindLabel = "图片",
    sink = sink,
    rules = rules,
    timeouts = timeouts,
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.PushMicroDiskImage): ExecutionPlan =
        buildPlan(intent, intent.targets, intent.objectName, intent.extraText, intent.targetDescription)
}

/** 209 推微盘文件。 */
class PushMicroDiskFileExecutor(
    sink: BatchOutcomeSink,
    rules: WeworkScreenRules = WeworkScreenRules(),
    timeouts: StepTimeouts = StepTimeouts(),
) : MicroDiskPushExecutor<CommandIntent.PushMicroDiskFile>(
    commandTypeValue = MessageTypes.PUSH_MICRO_DISK_FILE,
    kindLabel = "文件",
    sink = sink,
    rules = rules,
    timeouts = timeouts,
) {
    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.PushMicroDiskFile): ExecutionPlan =
        buildPlan(intent, intent.targets, intent.objectName, intent.extraText, intent.targetDescription)
}
