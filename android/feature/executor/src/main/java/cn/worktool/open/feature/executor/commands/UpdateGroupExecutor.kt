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
 * 指令 207 UPDATE_GROUP（FR-10 修改群信息，P0 子集：公告/备注/拉人）：
 * 会话列表进入群聊 → 群设置 →（按载荷）改公告 / 改备注 / 拉人 → 返回群聊。
 *
 * 风险级（FR-53）：**含拉人（addMembers 非空）→ L1**（FR-53 L1 清单明列「拉人」）；
 * 仅公告/备注变更 → L0。
 */
class UpdateGroupExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.UpdateGroup>() {

    override val commandType: Int get() = MessageTypes.UPDATE_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel =
        if (intent is CommandIntent.UpdateGroup && intent.addMembers.isNotEmpty()) {
            RiskLevel.L1
        } else {
            RiskLevel.L0
        }

    override fun buildTypedPlan(intent: CommandIntent.UpdateGroup): ExecutionPlan {
        val steps = buildList {
            add(CommonSteps.openConversation(rules, intent.targetGroup))
            add(CommonSteps.awaitChatReady(rules, timeouts))
            add(CommonSteps.openGroupSettings(rules, timeouts))
            if (intent.announcement != null) {
                add(editAnnouncementStep(intent.announcement))
                add(verifyAnnouncementSavedStep(intent.announcement))
            }
            if (intent.groupRemark != null) {
                add(editRemarkStep(intent.groupRemark))
                add(verifyRemarkSavedStep(intent.groupRemark))
            }
            if (intent.addMembers.isNotEmpty()) {
                add(openAddMembersStep())
                intent.addMembers.forEach { member -> add(selectAddMemberStep(member)) }
                add(confirmAddMembersStep(intent.addMembers))
                add(verifyMembersAddedStep(intent.addMembers))
            }
            add(backToChatStep())
        }
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetGroup,
            paramsSummary = buildString {
                append("announcement=${intent.announcement != null}")
                append(", remark=${intent.groupRemark != null}")
                append(", addMembers=${intent.addMembers.size}")
                if (intent.addMembers.isNotEmpty()) {
                    append(intent.addMembers.take(5).joinToString(prefix = "（", separator = "、", postfix = "）"))
                }
            },
            steps = steps,
        )
    }

    private fun editAnnouncementStep(announcement: String): PlanStep = PlanStep(
        id = "edit-announcement",
        description = "进入群公告并修改为 ${announcement.length} 字新公告",
    ) { driver ->
        val entry = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_ENTRY) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "群设置页「群公告」入口缺失",
            )
        clickOrReject(driver, entry, "「群公告」入口")
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "公告编辑输入框") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "公告编辑输入框未出现（${timeouts.awaitNodeMs}ms）",
        )
        if (!driver.inputText(input, announcement)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入公告被界面拒绝",
            )
        }
        val confirm = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "公告编辑「确定」按钮缺失",
            )
        clickOrReject(driver, confirm, "公告「确定」按钮")
    }

    private fun editRemarkStep(remark: String): PlanStep = PlanStep(
        id = "edit-group-remark",
        description = "修改群备注为「$remark」",
    ) { driver ->
        val entry = driver.currentRoot()
            ?.let { rules.findByText(it, "群备注") }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "群设置页「群备注」入口缺失",
            )
        clickOrReject(driver, entry, "「群备注」入口")
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "备注编辑输入框") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "备注编辑输入框未出现（${timeouts.awaitNodeMs}ms）",
        )
        if (!driver.inputText(input, remark)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入群备注被界面拒绝",
            )
        }
        // CR-t31-T31-R1：确定按钮缺失 = 变更未保存，必须失败上报
        // （对齐 editAnnouncementStep，绝不可静默 Success——否则备注未保存却回 SUCCEEDED）。
        val confirm = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "备注编辑「确定」按钮缺失（备注未保存）",
            )
        if (!driver.click(confirm)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击备注「确定」按钮被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 变更后校验（对齐 203/206/212 的 verify 步骤，CR-t31-T31-R1）：设置页预览已含新公告。 */
    private fun verifyAnnouncementSavedStep(announcement: String): PlanStep = PlanStep(
        id = "verify-announcement-saved",
        description = "校验群设置页公告预览已更新（含新公告文本）",
    ) { driver ->
        verifyPreviewContains(driver, "公告", announcement)
    }

    /** 变更后校验：设置页备注预览已含新备注。 */
    private fun verifyRemarkSavedStep(remark: String): PlanStep = PlanStep(
        id = "verify-group-remark-saved",
        description = "校验群设置页备注预览已更新（含新备注文本）",
    ) { driver ->
        verifyPreviewContains(driver, "群备注", remark)
    }

    private fun verifyPreviewContains(driver: UiDriver, what: String, newContent: String): StepOutcome {
        val appeared = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "$what 新预览") { root ->
            rules.findTextContains(root, newContent)
        }
        return if (appeared == null) {
            StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "$what 变更后设置页预览未见新文本（可能未保存，length=${newContent.length}）",
            )
        } else {
            StepOutcome.Success
        }
    }

    /** 拉人入口（FR-53 L1）。 */
    private fun openAddMembersStep(): PlanStep = PlanStep(
        id = "open-add-members",
        description = "进入群设置「添加成员」选择页",
    ) { driver ->
        val entry = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ADD_MEMBER_ENTRY) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "群设置页「添加成员」入口缺失",
            )
        clickOrReject(driver, entry, "「添加成员」入口")
    }

    /**
     * 逐成员勾选（CR-t31-T31-R2，对齐 InitGroupExecutor.selectMemberStep 每人一步）：
     * 每次勾选/确定都是不可逆副作用的组成步，拆到步边界后 FR-52 暂停门在
     * 每个成员前重查（CommandRunner 步间断点），暂停生效后不再新增勾选。
     */
    private fun selectAddMemberStep(member: String): PlanStep = PlanStep(
        id = "select-add-member:$member",
        description = "在成员选择列表勾选成员「$member」",
    ) { driver ->
        val row = driver.currentRoot()
            ?.let { rules.findByText(it, member) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "成员选择列表未找到「$member」（已中断，未确认添加）",
            )
        clickOrReject(driver, row, "成员「$member」选项")
    }

    /** 拉人确定（最后一步不可逆动作；缺失即中断，不产生拉人副作用）。 */
    private fun confirmAddMembersStep(members: List<String>): PlanStep = PlanStep(
        id = "confirm-add-members",
        description = "确认添加成员（拉人 ${members.size} 人：${members.joinToString("、")}）",
    ) { driver ->
        val confirm = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.SETTINGS_ADD_MEMBER_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "添加成员「确定」按钮缺失（未确认添加）",
            )
        clickOrReject(driver, confirm, "添加成员「确定」按钮")
    }

    /** 变更后校验：群成员列表已含全部新增成员（拉人生效证据）。 */
    private fun verifyMembersAddedStep(members: List<String>): PlanStep = PlanStep(
        id = "verify-members-added",
        description = "校验群成员列表已含新增成员（${members.joinToString("、")}）",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "界面根节点不可得（无法校验成员列表）",
            )
        for (member in members) {
            if (rules.findByText(root, member) == null) {
                return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.VERIFY_FAILED,
                    "群成员列表未见「$member」（拉人可能未生效）",
                )
            }
        }
        StepOutcome.Success
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
