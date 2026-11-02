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
 * 指令 206 INIT_GROUP（FR-09 创建外部群）：
 * 主界面「+」→ 发起群聊 → 输入群名 → 选择成员 → 创建 → 群聊页校验。
 *
 * 风险级（FR-53）：**L1（建群）**——FR-53 L1 清单明列「建群」。
 */
class InitGroupExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.InitGroup>() {

    override val commandType: Int get() = MessageTypes.INIT_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.InitGroup): ExecutionPlan {
        val steps = buildList {
            add(openCreateMenuStep())
            add(chooseInitGroupStep())
            add(inputGroupNameStep(intent.groupName))
            intent.members.forEach { member -> add(selectMemberStep(member)) }
            if (intent.announcement != null) {
                add(inputAnnouncementStep(intent.announcement))
            }
            add(confirmCreateStep(intent.groupName))
            add(verifyCreatedStep(intent.groupName))
        }
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.groupName,
            paramsSummary = "members=${intent.members.size}${intent.members.take(5).joinToString(prefix = "（", separator = "、", postfix = "）")}" +
                if (intent.announcement != null) "，announcement.length=${intent.announcement.length}" else "",
            steps = steps,
        )
    }

    private fun openCreateMenuStep(): PlanStep = PlanStep(
        id = "open-create-menu",
        description = "点击主界面「+」打开发起聊天菜单",
    ) { driver ->
        val plus = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.HOME_CREATE_PLUS) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "主界面「+」入口缺失（当前不在主界面？）",
            )
        clickOrReject(driver, plus, "「+」入口")
    }

    private fun chooseInitGroupStep(): PlanStep = PlanStep(
        id = "choose-init-group",
        description = "在菜单中选择「发起群聊」",
    ) { driver ->
        val menu = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "发起聊天菜单") { root ->
            rules.find(root, WeworkSelectorIds.INIT_GROUP_MENU)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「发起群聊」菜单项未出现（${timeouts.awaitNodeMs}ms）",
        )
        clickOrReject(driver, menu, "「发起群聊」菜单项")
    }

    private fun inputGroupNameStep(groupName: String): PlanStep = PlanStep(
        id = "input-group-name",
        description = "在建群弹窗输入群名「$groupName」",
    ) { driver ->
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群名输入框") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_NAME_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "群名输入框未出现（${timeouts.awaitNodeMs}ms）",
        )
        if (!driver.inputText(input, groupName)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入群名被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 逐成员选择步骤（每人一步：干跑报告逐条呈现拉人对象）。 */
    private fun selectMemberStep(member: String): PlanStep = PlanStep(
        id = "select-member:$member",
        description = "在成员选择列表勾选成员「$member」",
    ) { driver ->
        val row = driver.currentRoot()
            ?.let { rules.findByText(it, member) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "成员选择列表未找到「$member」",
            )
        clickOrReject(driver, row, "成员「$member」选项")
    }

    /** 建群公告（可选项：仅当载荷带公告）。 */
    private fun inputAnnouncementStep(announcement: String): PlanStep = PlanStep(
        id = "input-group-announcement",
        description = "填写建群公告（${announcement.length} 字）",
    ) { driver ->
        val input = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_INPUT) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "建群公告输入框缺失",
            )
        if (!driver.inputText(input, announcement)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入公告被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    private fun confirmCreateStep(groupName: String): PlanStep = PlanStep(
        id = "confirm-create",
        description = "点击「完成」创建群「$groupName」",
    ) { driver ->
        val confirm = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.GROUP_CREATE_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "建群「完成」按钮缺失",
            )
        clickOrReject(driver, confirm, "「完成」按钮")
    }

    private fun verifyCreatedStep(groupName: String): PlanStep = PlanStep(
        id = "verify-group-created",
        description = "校验已进入新群聊天页（标题=「$groupName」）",
    ) { driver ->
        val title = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "新群聊天页") { root ->
            rules.find(root, WeworkSelectorIds.CHAT_TITLE)
                ?.takeIf { it.text == groupName }
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.VERIFY_FAILED,
            "建群后未见群「$groupName」聊天页",
        )
        StepOutcome.Success
    }

    private fun clickOrReject(driver: UiDriver, node: UiNode, what: String): StepOutcome =
        if (driver.click(node)) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "点击$what 被界面拒绝")
        }
}
