/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.UiDriver
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 225 MODIFY_GROUP_MEMBER_INFO（FR-10 修改群成员备注/标签）：
 * 会话列表进入群聊 → 群设置 → 成员列表定位成员 → 成员详情改备注/标签 → 返回。
 *
 * 风险级（FR-53）：L0（改备注/标签未列入 L1/L2 清单；分级策略可配置）。
 */
class ModifyGroupMemberInfoExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ModifyGroupMemberInfo>() {

    override val commandType: Int get() = MessageTypes.MODIFY_GROUP_MEMBER_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.ModifyGroupMemberInfo): ExecutionPlan {
        val steps = buildList {
            add(CommonSteps.openConversation(rules, intent.targetGroup))
            add(CommonSteps.awaitChatReady(rules, timeouts))
            add(CommonSteps.openGroupSettings(rules, timeouts))
            add(openMemberStep(intent.memberName))
            if (intent.markName != null) {
                add(editMarkStep(intent.memberName, intent.markName))
            }
            if (intent.tagList.isNotEmpty()) {
                add(setTagsStep(intent.memberName, intent.tagList))
            }
            add(confirmMemberEditStep())
            add(backToChatStep())
        }
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = buildString {
                append("member=${intent.memberName}")
                append(", markName=${intent.markName ?: "无"}")
                append(", tags=${intent.tagList.size}")
                if (intent.tagList.isNotEmpty()) {
                    append(intent.tagList.joinToString(prefix = "（", separator = "、", postfix = "）"))
                }
            },
            steps = steps,
        )
    }

    private fun openMemberStep(memberName: String): PlanStep = PlanStep(
        id = "open-member",
        description = "在群设置成员列表定位成员「$memberName」并进入其详情页",
    ) { driver ->
        val member = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "成员「$memberName」") { root ->
            rules.findByText(root, memberName)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "群设置成员列表未找到「$memberName」",
        )
        clickOrReject(driver, member, "成员「$memberName」")
    }

    private fun editMarkStep(memberName: String, markName: String): PlanStep = PlanStep(
        id = "edit-member-mark",
        description = "将成员「$memberName」备注改为「$markName」",
    ) { driver ->
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "备注输入框") { root ->
            rules.find(root, WeworkSelectorIds.MEMBER_MARK_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "成员详情备注输入框未出现（${timeouts.awaitNodeMs}ms）",
        )
        if (!driver.inputText(input, markName)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入成员备注被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 标签设置（成员详情页标签输入框，P0 简化为一次整体写入）。 */
    private fun setTagsStep(memberName: String, tags: List<String>): PlanStep = PlanStep(
        id = "set-member-tags",
        description = "为成员「$memberName」设置标签 ${tags.joinToString("、")}",
    ) { driver ->
        val input = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.MEMBER_TAG_INPUT) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "成员详情标签输入框缺失（无法设置标签）",
            )
        if (!driver.inputText(input, tags.joinToString(","))) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "写入标签被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    private fun confirmMemberEditStep(): PlanStep = PlanStep(
        id = "confirm-member-edit",
        description = "确认成员信息修改",
    ) { driver ->
        val confirm = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.MEMBER_MARK_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "成员详情「确定」按钮缺失",
            )
        clickOrReject(driver, confirm, "成员信息「确定」按钮")
    }

    private fun backToChatStep(): PlanStep = PlanStep(
        id = "back-to-chat",
        description = "返回群聊天页（变更完成）",
    ) { driver ->
        if (!driver.goBack()) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "系统返回被拒绝（可能已在目标页）",
            )
        }
        StepOutcome.Success
    }

    private fun clickOrReject(driver: UiDriver, node: UiNode, what: String): StepOutcome =
        if (driver.click(node)) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "点击$what 被界面拒绝")
        }
}
