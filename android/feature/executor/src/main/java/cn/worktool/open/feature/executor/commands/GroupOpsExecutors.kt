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
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 219 DISMISS_GROUP 解散群（docs/10 B3，**L2**：需群主身份、解散不可逆；
 * FR-53 独立 approver 由服务端治理，端侧执行前复检）：
 * 进群 → 群设置 →「解散群聊」→ 确认弹窗 → 校验群会话消失。
 */
class DismissGroupExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.DismissGroup>() {

    override val commandType: Int get() = MessageTypes.DISMISS_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L2

    override fun buildTypedPlan(intent: CommandIntent.DismissGroup): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                openDismissStep(),
                confirmDismissStep(),
                verifyDismissedStep(intent),
            ),
        )

    private fun openDismissStep(): PlanStep = PlanStep(
        id = "open-dismiss",
        description = "在群设置页点击「解散群聊」入口",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「解散群聊」入口") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_DISMISS_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「解散群聊」入口未出现（非群主身份族？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「解散群聊」入口")
    }

    private fun confirmDismissStep(): PlanStep = PlanStep(
        id = "confirm-dismiss",
        description = "在「解散后不可恢复」确认弹窗点击「确定」（不可逆操作）",
    ) { driver ->
        val confirm = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "解散确认弹窗") { root ->
            rules.find(root, WeworkSelectorIds.DIALOG_CONFIRM)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "解散确认弹窗未出现（界面拒绝解散入口）",
        )
        CommonSteps.clickOrReject(driver, confirm, "解散确认「确定」")
    }

    private fun verifyDismissedStep(intent: CommandIntent.DismissGroup): PlanStep = PlanStep(
        id = "verify-dismissed",
        description = "返回会话列表校验群「${intent.groupName}」已消失",
    ) { driver ->
        driver.goBack()
        val stillThere = driver.currentRoot()?.let { rules.findByText(it, intent.groupName) }
        if (stillThere != null) {
            StepOutcome.Failure(ExecutorErrorCodes.VERIFY_FAILED, "解散后会话列表仍可见「${intent.groupName}」")
        } else {
            StepOutcome.Success
        }
    }
}

/**
 * 指令 244 EXIT_GROUP 退出群聊（docs/10 B3，L1；**异常不重试**语义在回执/调度面）：
 * 进群 → 群设置 →「退出群聊」→（群主身份被拦截 → ILLEGAL_OPERATION 不退出）→
 * 确认弹窗「退出」→ 校验退出成功。
 */
class ExitGroupExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ExitGroup>() {

    override val commandType: Int get() = ExtendedCommandTypes.EXIT_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.ExitGroup): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                openExitStep(),
                confirmExitStep(),
                verifyExitedStep(intent),
            ),
        )

    private fun openExitStep(): PlanStep = PlanStep(
        id = "open-exit",
        description = "在群设置页点击「退出群聊」入口",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「退出群聊」入口") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_EXIT_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「退出群聊」入口未出现（目标不是群聊？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「退出群聊」入口")
    }

    private fun confirmExitStep(): PlanStep = PlanStep(
        id = "confirm-exit",
        description = "识别退出拦截（群主身份）或确认弹窗并点击「退出」",
    ) { driver ->
        // 群主拦截：页面提示「当前身份为群主 / 请转让群主后退出」→ 拦截退出
        val ownerBlock = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "退出确认态") { root ->
            rules.findTextContains(root, "群主") ?: rules.find(root, WeworkSelectorIds.DIALOG_CONFIRM)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到退群确认框，未确认退出",
        )
        val blocked = ownerBlock.text?.let { it.contains("群主") } == true
        if (blocked) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ILLEGAL_OPERATION,
                "当前身份为群主，请转让群主后退出（拦截退出）",
            )
        }
        CommonSteps.clickOrReject(driver, ownerBlock, "退群确认「确定」")
    }

    private fun verifyExitedStep(intent: CommandIntent.ExitGroup): PlanStep = PlanStep(
        id = "verify-exited",
        description = "返回会话列表校验已退出并清空「${intent.groupName}」",
    ) { driver ->
        driver.goBack()
        val stillThere = driver.currentRoot()?.let { rules.findByText(it, intent.groupName) }
        if (stillThere != null) {
            StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "已提交退出但会话仍可见（请核实群状态）",
            )
        } else {
            StepOutcome.Success
        }
    }
}

/**
 * 指令 214 SHOW_GROUP_INFO 展示/读取群信息（docs/10 B3，L1）：
 * 进群 → 群设置读取群名/群主/公告/成员列表 → 结构化推送（501 形态）。
 * 「已退出群」标记 → GROUP_EXITED 失败（对照 515 同态=成功）。
 */
class ShowGroupInfoExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ShowGroupInfo>() {

    override val commandType: Int get() = MessageTypes.SHOW_GROUP_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.ShowGroupInfo): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                readAndPushStep(intent),
            ),
        )

    private fun readAndPushStep(intent: CommandIntent.ShowGroupInfo): PlanStep = PlanStep(
        id = "read-and-push",
        description = "读取群名/群主/公告/成员列表并结构化推送",
    ) { driver ->
        val root = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群设置页") {
            if (rules.find(it, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE) != null) it else null
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "群设置页未就绪（未进入群成员列表页族）",
        )
        if (rules.findTextContains(root, "无法在已退出的群聊中发送消息") != null) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.GROUP_EXITED, "检测到已退出的群聊：${intent.groupName}")
        }
        val groupName = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE)?.text.orEmpty()
        val owner = rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_OWNER)?.text.orEmpty()
        val announcement = rules.find(root, WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_VALUE)?.text.orEmpty()
        val members = rules.findAll(root, WeworkSelectorIds.GROUP_MEMBER_LIST_ITEM)
            .mapNotNull { it.text?.trim() }
            .filter { it.isNotEmpty() }
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", groupName)
                put("groupOwner", owner)
                put("groupAnnouncement", announcement)
                put("memberList", JsonArray(members.map { JsonPrimitive(it) }))
                put("memberCount", members.size)
            },
        )
        StepOutcome.Success
    }
}
