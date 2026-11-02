/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.UiDriver
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 跨执行器复用步骤（T3.1）：会话定位/聊天页等待/群设置入口等公共界面动作。
 * 保持与单指令专属步骤同一 [PlanStep] 形态，干跑报告统一渲染。
 */
internal object CommonSteps {

    /**
     * 会话列表定位目标会话（203/207/225 入口）：
     * 当前界面根缺失或找不到目标会话名 → NODE_NOT_FOUND。
     */
    fun openConversation(
        rules: WeworkScreenRules,
        conversationName: String,
        stepId: String = "open-conversation",
    ): PlanStep = PlanStep(
        id = stepId,
        description = "在会话列表定位会话「$conversationName」并点击进入聊天页",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "界面根节点不可得（无障碍服务未连接或前台无窗）",
            )
        val node = rules.findByText(root, conversationName)
            ?: rules.find(root, WeworkSelectorIds.CONVERSATION_ITEM_NAME)
                ?.takeIf { it.text == conversationName }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "会话列表未找到目标会话「$conversationName」",
            )
        if (!driver.click(node)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击会话项「$conversationName」被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 等待聊天页就绪（输入框出现）；超时 → STEP_TIMEOUT。 */
    fun awaitChatReady(
        rules: WeworkScreenRules,
        timeouts: StepTimeouts,
        what: String = "聊天页输入框",
    ): PlanStep = PlanStep(
        id = "await-chat-ready",
        description = "等待聊天页就绪（$what 出现，超时 ${timeouts.awaitNodeMs}ms）",
    ) { driver ->
        awaitSelector(driver, rules, timeouts, WeworkSelectorIds.CHAT_INPUT, what)
    }

    /** 点击聊天页标题进入群设置页（207/225）；标题缺失 → NODE_NOT_FOUND。 */
    fun openGroupSettings(
        rules: WeworkScreenRules,
        timeouts: StepTimeouts,
    ): PlanStep = PlanStep(
        id = "open-group-settings",
        description = "点击聊天页群名进入群设置页",
    ) { driver ->
        val title = driver.awaitNode(
            timeouts.awaitNodeMs, timeouts.pollMs, "聊天页群名标题",
        ) { rules.find(it, WeworkSelectorIds.CHAT_TITLE) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "聊天页群名标题未出现（无法进入群设置）",
            )
        if (!driver.click(title)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击群名标题被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 切换到底部「通讯录」Tab（212/501/502 入口）；Tab 缺失 → NODE_NOT_FOUND。 */
    fun openContactsTab(
        rules: WeworkScreenRules,
    ): PlanStep = PlanStep(
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

    /** 通用：等待某选择器命中（null 返回即超时）。 */
    fun awaitSelector(
        driver: UiDriver,
        rules: WeworkScreenRules,
        timeouts: StepTimeouts,
        selectorId: String,
        what: String,
        onHit: (UiNode) -> StepOutcome = { StepOutcome.Success },
    ): StepOutcome {
        val node = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, what) {
            rules.find(it, selectorId)
        } ?: return StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "等待「$what」超时（${timeouts.awaitNodeMs}ms）",
        )
        return onHit(node)
    }

    /** 点击或报 ACTION_REJECTED（多执行器共用的小工具）。 */
    fun clickOrReject(driver: UiDriver, node: UiNode, what: String): StepOutcome =
        if (driver.click(node)) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "点击$what 被界面拒绝")
        }
}
