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
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 240 ADD_GROUP_BOARD_WEBPAGE 添加群看板网页（docs/10 B3，L1：
 * 群公告网页板式，**仅内部群**；字段名未冻结=WorkTool-Open 自拟）：
 * 进群 → 群管理 →「群看板」→「管理群看板」→「添加」→ 添加网页页输入链接 →
 * 确定 → 返回看板页。
 */
class AddGroupBoardWebpageExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.AddGroupBoardWebpage>() {

    override val commandType: Int get() = ExtendedCommandTypes.ADD_GROUP_BOARD_WEBPAGE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.AddGroupBoardWebpage): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}, url.length=${intent.url.length}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                openBoardStep(),
                openAddStep(),
                inputUrlStep(intent),
                submitStep(),
            ),
        )

    private fun openBoardStep(): PlanStep = PlanStep(
        id = "open-group-board",
        description = "在群管理页点击「群看板」入口（仅内部群支持）",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「群看板」入口") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_BOARD_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「群看板」入口未出现（群看板仅支持内部群）",
        )
        CommonSteps.clickOrReject(driver, entry, "「群看板」入口")
    }

    private fun openAddStep(): PlanStep = PlanStep(
        id = "open-add-webpage",
        description = "在管理群看板页点击「添加」进入添加网页页",
    ) { driver ->
        val add = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「添加」按钮") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_BOARD_ADD)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「添加」按钮未出现（未进入管理群看板页族）",
        )
        CommonSteps.clickOrReject(driver, add, "「添加」")
    }

    private fun inputUrlStep(intent: CommandIntent.AddGroupBoardWebpage): PlanStep = PlanStep(
        id = "input-url",
        description = "在添加网页页输入链接（长度=${intent.url.length}）",
    ) { driver ->
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "网页链接输入框") { root ->
            rules.find(root, WeworkSelectorIds.BOARD_URL_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未进入添加网页页面（网页链接输入框缺失）",
        )
        if (!driver.click(input) || !driver.inputText(input, intent.url)) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "网页链接输入失败")
        }
        StepOutcome.Success
    }

    private fun submitStep(): PlanStep = PlanStep(
        id = "submit-webpage",
        description = "点击确定提交并返回群看板页",
    ) { driver ->
        val submit = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.BOARD_SUBMIT) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "确定按钮缺失")
        CommonSteps.clickOrReject(driver, submit, "确定")
    }
}
