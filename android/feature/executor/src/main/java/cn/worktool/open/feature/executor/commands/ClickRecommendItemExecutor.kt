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
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 235 CLICK_RECOMMEND_ITEM 点击聊天推荐条（docs/10 B3，L1；四形态：
 * 发起收款（extraText=金额）/ 企业名片 / 推荐客服 / 商品图册（objectName=商品名））：
 * 逐目标会话：点击底部推荐条「itemName」→ 收款表单输金额点「发起收款」，
 * 或图册选商品点「发送」/名片直接「发送」。金额 ≤2 位小数校验在规范化侧。
 */
class ClickRecommendItemExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ClickRecommendItem>() {

    override val commandType: Int get() = ExtendedCommandTypes.CLICK_RECOMMEND_ITEM

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.ClickRecommendItem): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, item=${intent.itemName}, " +
                "amount=${intent.amount ?: "无"}, product=${intent.productName ?: "无"}",
            steps = intent.targets.flatMap { target -> stepsPerTarget(flow, intent, target) } +
                flow.emitStep(),
        )
    }

    private fun stepsPerTarget(
        flow: TargetFlow,
        intent: CommandIntent.ClickRecommendItem,
        target: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
        },
        flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
            flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
        },
        flow.guardedStep("click-recommend-$target", "点击底部推荐条「${intent.itemName}」") { driver ->
            val item = driver.currentRoot()?.let { rules.findByText(it, intent.itemName) }
                ?: return@guardedStep flow.failAsGuarded(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "未找到或无法点击推荐条（「${intent.itemName}」缺失）",
                )
            flow.record(CommonSteps.clickOrReject(driver, item, "推荐条「${intent.itemName}」"))
        },
        flow.guardedStep("fill-form-$target", "完成推荐后续表单（收款金额/商品选择/直接发送）") { driver ->
            when {
                intent.itemName == COLLECT -> {
                    val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "收款金额输入框") { root ->
                        rules.find(root, WeworkSelectorIds.RECOMMEND_AMOUNT_INPUT)
                    } ?: return@guardedStep flow.failAsGuarded(
                        ExecutorErrorCodes.NODE_NOT_FOUND, "未找到发起收款按钮（金额输入缺失）",
                    )
                    if (!driver.click(input) || !driver.inputText(input, intent.amount.orEmpty())) {
                        return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "填写收款金额失败")
                    }
                    val confirm = driver.currentRoot()
                        ?.let { rules.find(it, WeworkSelectorIds.RECOMMEND_COLLECT_CONFIRM) }
                        ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "点击发起收款按钮失败（按钮缺失）")
                    flow.record(CommonSteps.clickOrReject(driver, confirm, "「发起收款」"))
                }
                intent.productName != null -> {
                    val product = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "商品「${intent.productName}」") { root ->
                        rules.findByText(root, intent.productName!!)
                    } ?: return@guardedStep flow.failAsGuarded(
                        ExecutorErrorCodes.NODE_NOT_FOUND, "未找到商品图册条目（「${intent.productName}」）",
                    )
                    flow.record(CommonSteps.clickOrReject(driver, product, "商品「${intent.productName}」"))
                    val send = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.RECOMMEND_SEND_BUTTON) }
                        ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到推荐按钮（发送缺失）")
                    flow.record(CommonSteps.clickOrReject(driver, send, "「发送」"))
                }
                else -> {
                    val send = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.RECOMMEND_SEND_BUTTON) }
                        ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到推荐按钮（发送缺失）")
                    flow.record(CommonSteps.clickOrReject(driver, send, "「发送」"))
                }
            }
            StepOutcome.Success
        },
        PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )

    private companion object {
        const val COLLECT = "发起收款"
    }
}
