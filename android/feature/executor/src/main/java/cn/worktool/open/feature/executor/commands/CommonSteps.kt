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
     * 201002 即此类环境性失败）；②容器定向——优先取「可滚动**且含会话项**」的容器，
     * 防先序更前的横幅/页签等可滚动容器劫持。
     * 滚动兜底耗尽 → **搜索框兜底** [openConversationViaSearch]（原版 intoRoom 搜索
     * 语义：回消息主页 → 搜索入口 → 输入会话名 → 结果命中进入）。挂载于本步动作内
     * （计划「首错即停」，独立列表步在滚动失败后不可达）；**禁高频：单发不重试**，
     * 单指令至多一次搜索兜底。兜底每环节失败如实透出错误码（入口缺失=NODE_NOT_FOUND、
     * 输入/点击被拒=ACTION_REJECTED、结果未命中=STEP_TIMEOUT）。
     */
    fun openConversation(
        rules: WeworkScreenRules,
        conversationName: String,
        stepId: String = "open-conversation",
        maxScrolls: Int = 8,
        timeouts: StepTimeouts = StepTimeouts(),
    ): PlanStep = PlanStep(
        id = stepId,
        description = "在会话列表定位会话「$conversationName」并点击进入聊天页（含正反向滚动搜索与搜索框兜底）",
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
                // 滚动兜底耗尽 → 搜索框兜底（单发禁高频）；失败如实透出兜底环节错误码
                val viaSearch = openConversationViaSearch(
                    rules, conversationName, timeouts, stepId = "$stepId-search-fallback",
                ).action(driver)
                return@PlanStep when (viaSearch) {
                    is StepOutcome.Success -> viaSearch
                    is StepOutcome.Failure -> StepOutcome.Failure(
                        viaSearch.errorCode,
                        "滚动定位未命中（正反向各 ${maxScrolls} 页）→ 搜索兜底：${viaSearch.reason}",
                    )
                }
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
     * 搜索框兜底（原版 intoRoom 搜索语义，2026-11-05）：会话列表滚尽未中时的
     * 第二段兜底——回消息主页（[openMessageTab]，对齐原版 goHome）→ 点搜索入口
     * （HOME_SEARCH_ENTRY，真机校准 desc=「搜索，按钮」/ `o7a`）→ 搜索页输入框
     * （SEARCH_INPUT，真机校准 `m0f`，点入聚焦后输入会话名）→ 等待搜索结果
     * **精确命中**会话名（SEARCH_RESULT_ITEM；「全部」页签默认态，过滤 tab
     * 全部/联系人/群聊见 dump）→ 点击进入聊天页。
     *
     * 每环节失败如实错误码：消息 Tab 缺失/搜索入口缺失=NODE_NOT_FOUND；
     * 搜索页未打开（输入框未现）/结果未命中=STEP_TIMEOUT；点击/输入被拒=ACTION_REJECTED。
     * **单发语义**：无重试循环，单次调用至多一次搜索动线（禁高频护栏由调用方
     * [openConversation] 的单步动作结构保证——单指令至多一次搜索兜底）。
     * 选择器族真机证据：local-private/evidence/session21/uitree-search-page.xml、
     * uitree-msg-home.xml（o7a）。
     */
    fun openConversationViaSearch(
        rules: WeworkScreenRules,
        conversationName: String,
        timeouts: StepTimeouts = StepTimeouts(),
        stepId: String = "open-conversation-via-search",
    ): PlanStep = PlanStep(
        id = stepId,
        description = "搜索兜底：回消息主页搜索「$conversationName」并点击结果进入聊天页",
    ) { driver ->
        val home = openMessageTab(rules).action(driver)
        if (home is StepOutcome.Failure) return@PlanStep home
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "消息页搜索入口") {
            rules.find(it, WeworkSelectorIds.HOME_SEARCH_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "消息页搜索入口未出现（desc=「搜索，按钮」缺失）",
        )
        if (!driver.click(entry)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击搜索入口被界面拒绝",
            )
        }
        val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "搜索页输入框") {
            rules.find(it, WeworkSelectorIds.SEARCH_INPUT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "搜索页输入框未在 ${timeouts.awaitNodeMs}ms 内出现（搜索页未打开？）",
        )
        if (!driver.click(input)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "聚焦搜索输入框被界面拒绝",
            )
        }
        if (!driver.inputText(input, conversationName)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "输入会话名被界面拒绝",
            )
        }
        val hit = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "搜索结果「$conversationName」") { root ->
            rules.findAll(root, WeworkSelectorIds.SEARCH_RESULT_ITEM)
                .firstOrNull { it.text == conversationName }
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "搜索结果未在 ${timeouts.awaitNodeMs}ms 内出现「$conversationName」（无结果或结果形态漂移）",
        )
        if (!driver.click(hit)) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "点击搜索结果被界面拒绝",
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
