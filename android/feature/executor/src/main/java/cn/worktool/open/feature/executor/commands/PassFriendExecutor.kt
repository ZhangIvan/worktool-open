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
 * 指令 212 PASS_ALL_FRIEND_REQUEST（FR-15 新好友自动通过/批量通过）：
 * 通讯录 Tab → 新的朋友 → 逐个点击「接受」（单轮上限 [CommandIntent.PassFriend.limit]）
 * → 校验无剩余待处理请求。
 *
 * 粒度（CR-t31-T31-R2，对齐 InitGroupExecutor 每成员一步）：**每个「接受」点击一步**
 * （[ACCEPT_STEP_PREFIX]`-N`，N=1..limit）——每次接受都是不可逆副作用，拆到步边界后
 * FR-52 暂停门在**每次接受前**重查（CommandRunner 步间安全断点），满足
 * 「暂停生效后 ≤5s 无新副作用」（docs/01 FR-52）。待处理数运行期才发现，故按上限
 * 铺步：待处理已清空的步**空过**（无「接受」按钮 = 无对象，零副作用成功）。
 *
 * 风险级（FR-53）：L0（通过好友未列入 L1/L2 清单；分级策略可配置）。
 *
 * spec §4.2 注：212 在上游开源版为**空实现**；本执行器是我方语义落地
 * （docs/01 FR-15 端侧规则）。
 */
class PassFriendExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.PassFriend>() {

    override val commandType: Int get() = MessageTypes.PASS_ALL_FRIEND_REQUEST

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.PassFriend): ExecutionPlan {
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "acceptLimit=${intent.limit}",
            steps = listOf(
                openContactsTabStep(),
                openNewFriendsStep(),
            ) + (1..intent.limit).map { acceptRequestStep(it) } + listOf(
                verifyNoPendingStep(intent.limit),
            ),
        )
    }

    private fun openContactsTabStep(): PlanStep = PlanStep(
        id = "open-contacts-tab",
        description = "切换到底部「通讯录」Tab",
    ) { driver ->
        val tab = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.CONTACTS_TAB) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "「通讯录」Tab 缺失（当前不在主界面？）",
            )
        clickOrReject(driver, tab, "「通讯录」Tab")
    }

    private fun openNewFriendsStep(): PlanStep = PlanStep(
        id = "open-new-friends",
        description = "进入「新的朋友」待处理请求列表",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「新的朋友」入口") { root ->
            rules.find(root, WeworkSelectorIds.NEW_FRIENDS_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「新的朋友」入口未在 ${timeouts.awaitNodeMs}ms 内出现（通讯录页未就绪？）",
        )
        clickOrReject(driver, entry, "「新的朋友」入口")
    }

    /** 接受步骤 ID 前缀（`accept-request-N`，N=1..limit；报告/测试引用）。 */
    private fun acceptRequestStep(index: Int): PlanStep = PlanStep(
        id = "$ACCEPT_STEP_PREFIX-$index",
        description = "点击第 $index 个待处理请求的「接受」按钮（列表已清空则空过）",
    ) { driver ->
        val acceptButton = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.FRIEND_ACCEPT_BUTTON) }
            ?: return@PlanStep StepOutcome.Success // 无剩余待处理请求：空过（零副作用）
        if (!driver.click(acceptButton)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击第 $index 个「接受」按钮被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    private fun verifyNoPendingStep(limit: Int): PlanStep = PlanStep(
        id = "verify-no-pending",
        description = "校验待处理好友请求已清空（上限 $limit）",
    ) { driver ->
        val remaining = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.FRIEND_ACCEPT_BUTTON) }
        if (remaining != null) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "通过后仍存在待处理请求（超出单轮上限 $limit 或界面未清空，需分批重试）",
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

    private companion object {
        /** 接受步骤 ID 前缀（公开常量供测试/报告消费方引用，避免裸字符串漂移）。 */
        const val ACCEPT_STEP_PREFIX = "accept-request"
    }
}
