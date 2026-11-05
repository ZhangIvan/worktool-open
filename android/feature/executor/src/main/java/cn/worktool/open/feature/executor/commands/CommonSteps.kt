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
     * 会话列表定位目标会话（203/207/225/501 入口）：
     * 首屏未命中 → **滚动搜索兜底**（对齐原版 intoRoom 列表扫描；W-M6 会话 10b
     * 实证：列表滚动位置致目标行不可见时 201001——findByText 只扫活动根可见节点）。
     * rc1 遗留硬化收口（2026-11-05）：①方向双向——正向下滚 [maxScrolls] 页耗尽后
     * 反向上滚再搜 [maxScrolls] 页（残留屏态：目标行在视口上方，真机会话 10 首发
     * 201002 即此类环境性失败）；②容器定向——优先取「可滚动且含会话项」的容器，
     * 防先序更前的横幅/页签等可滚动容器劫持。正反向均耗尽 → NODE_NOT_FOUND。
     */
    fun openConversation(
        rules: WeworkScreenRules,
        conversationName: String,
        stepId: String = "open-conversation",
        maxScrolls: Int = 8,
    ): PlanStep = PlanStep(
        id = stepId,
        description = "在会话列表定位会话「$conversationName」并点击进入聊天页（含正反向滚动搜索）",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "界面根节点不可得（无障碍服务未连接或前台无窗）",
            )

        fun findTarget(r: UiNode?) = r?.let {
            rules.findByText(it, conversationName)
                ?: rules.find(it, WeworkSelectorIds.CONVERSATION_ITEM_NAME)
                    ?.takeIf { n -> n.text == conversationName }
        }

        var node = findTarget(root)
        if (node == null) {
            val container = findConversationListContainer(root, rules)
            if (container != null) {
                fun scrollSearch(direction: cn.worktool.open.core.accessibility.tree.ScrollDirection) {
                    var scrolls = 0
                    while (node == null && scrolls < maxScrolls) {
                        if (!driver.scroll(container, direction)) break
                        scrolls += 1
                        node = findTarget(driver.currentRoot())
                    }
                }
                // 先下滚（对齐原版 intoRoom 列表扫描），耗尽后上滚兜底（视口上方目标行）
                scrollSearch(cn.worktool.open.core.accessibility.tree.ScrollDirection.FORWARD)
                if (node == null) {
                    scrollSearch(cn.worktool.open.core.accessibility.tree.ScrollDirection.BACKWARD)
                }
            }
            if (node == null) {
                return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "会话列表未找到目标会话「$conversationName」（含正反向各 ${maxScrolls} 页滚动搜索）",
                )
            }
        }
        if (!driver.click(requireNotNull(node))) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击会话项「$conversationName」被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /**
     * 会话列表容器选择（rc1 遗留硬化收口）：先序找首个「可滚动**且含会话项**」的
     * 容器（rules.find 按 CONVERSATION_ITEM_NAME 探测子树）；无会话项命中时退回
     * 首个可滚动节点（旧口径兜底）。防先序更前的横幅/页签等无关可滚动容器劫持。
     */
    private fun findConversationListContainer(node: UiNode, rules: WeworkScreenRules): UiNode? {
        var firstScrollable: UiNode? = null
        fun visit(current: UiNode): UiNode? {
            if (current.isScrollable) {
                if (firstScrollable == null) firstScrollable = current
                if (rules.find(current, WeworkSelectorIds.CONVERSATION_ITEM_NAME) != null) return current
            }
            for (i in 0 until current.childCount) {
                current.childAt(i)?.let { child -> visit(child)?.let { return it } }
            }
            return null
        }
        return visit(node) ?: firstScrollable
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

    /**
     * 进入群设置页（207/225）。真机校准（2026-11-04）：企微当前版本点聊天页
     * 标题**不进**设置页，唯一入口是右上「更多」按钮（desc=「更多，按钮」）；
     * 「更多」未命中再回退点标题（旧版本语义兜底）。
     */
    fun openGroupSettings(
        rules: WeworkScreenRules,
        timeouts: StepTimeouts,
    ): PlanStep = PlanStep(
        id = "open-group-settings",
        description = "点击聊天页「更多」（回退：群名标题）进入群设置页",
    ) { driver ->
        val entry = driver.awaitNode(
            timeouts.awaitNodeMs, timeouts.pollMs, "聊天页群设置入口（更多/群名标题）",
        ) { root ->
            rules.find(root, WeworkSelectorIds.CHAT_MORE_ENTRY)
                ?: rules.find(root, WeworkSelectorIds.CHAT_TITLE)
        }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "聊天页群设置入口未出现（更多按钮与群名标题均缺失）",
            )
        if (!driver.click(entry)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击群设置入口被界面拒绝",
            )
        }
        StepOutcome.Success
    }

    /** 返回上一级界面（driver.goBack 真机=系统返回）。 */
    fun goBack(stepId: String = "go-back", what: String = "返回上一级"): PlanStep = PlanStep(
        id = stepId,
        description = what,
    ) { driver ->
        if (driver.goBack()) StepOutcome.Success
        else StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "系统返回被拒绝（$what）")
    }

    /** 切换到底部「消息」Tab（501 会话定位入口，会话 10 对齐原版 goHome）；Tab 缺失 → NODE_NOT_FOUND。 */
    fun openMessageTab(rules: WeworkScreenRules): PlanStep = PlanStep(
        id = "open-message-tab",
        description = "切换到底部「消息」Tab",
    ) { driver ->
        val root = driver.currentRoot()
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "界面根节点不可得（无障碍服务未连接或前台无窗）",
            )
        val tab = rules.find(root, WeworkSelectorIds.MESSAGE_TAB)
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "「消息」Tab 缺失（当前不在主界面？）",
            )
        clickOrReject(driver, tab, "「消息」Tab")
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
