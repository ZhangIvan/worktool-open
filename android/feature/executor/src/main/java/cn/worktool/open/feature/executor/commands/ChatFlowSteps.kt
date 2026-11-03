/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.UiDriver
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 聊天面复用动作（对等计划 B2/B3 批消息族执行器共用）：
 * 文本输入+发送、气泡定位（按发送人+原文）、长按菜单、回首页。
 * 与 [CommonSteps] 同一 [PlanStep] 形态，保持干跑报告统一渲染。
 */
internal object ChatFlowSteps {

    /**
     * 聚焦输入框 → 输入文本 → 点发送（不含上屏校验；校验语义由调用方决定）。
     * 输入框/发送按钮缺失 → NODE_NOT_FOUND；动作被拒 → ACTION_REJECTED。
     */
    fun inputAndSend(
        driver: UiDriver,
        rules: WeworkScreenRules,
        text: String,
    ): StepOutcome {
        val root = driver.currentRoot()
            ?: return StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "界面根节点不可得")
        val input = rules.find(root, WeworkSelectorIds.CHAT_INPUT)
            ?: return StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "聊天页输入框缺失")
        if (!driver.click(input)) {
            return StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "聚焦输入框被界面拒绝")
        }
        if (!driver.inputText(input, text)) {
            return StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "输入文本被界面拒绝")
        }
        val send = driver.currentRoot()
            ?.let { rules.find(it, WeworkSelectorIds.CHAT_SEND) }
            ?: return StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "发送按钮缺失（输入后未出现）")
        if (!driver.click(send)) {
            return StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "点击发送按钮被界面拒绝")
        }
        return StepOutcome.Success
    }

    /**
     * 定位消息气泡（按原文精确匹配；发送人非空时校验同泡发送人位）。
     * 匹配优先级：动态文本精确匹配 → CHAT_MESSAGE_TEXT 选择器等值匹配。
     */
    fun locateBubble(
        root: UiNode,
        rules: WeworkScreenRules,
        sender: String?,
        content: String,
    ): UiNode? {
        val byDynamic = rules.findByText(root, content)
        val candidates = ((if (byDynamic != null) listOf(byDynamic) else emptyList()) +
            rules.findAll(root, WeworkSelectorIds.CHAT_MESSAGE_TEXT).filter { it.text == content })
            .distinct()
        return if (sender.isNullOrBlank()) {
            candidates.firstOrNull()
        } else {
            candidates.firstOrNull { siblingSenderText(it) == sender }
        }
    }

    /** 气泡发送人位：同父节点的相邻文本子节点（发送人昵称行）。 */
    fun siblingSenderText(node: UiNode): String? {
        val parent = node.parent ?: return null
        for (index in 0 until parent.childCount) {
            val child = parent.childAt(index) ?: continue
            if (child !== node && !child.text.isNullOrBlank() && child.text != node.text) {
                return child.text
            }
        }
        return null
    }

    /**
     * 长按气泡 → 点击菜单项（回复/转发/撤回/多选）。
     * 长按被拒 → ACTION_REJECTED；菜单项未出现 → STEP_TIMEOUT。
     */
    fun longPressMenu(
        driver: UiDriver,
        rules: WeworkScreenRules,
        timeouts: StepTimeouts,
        bubble: UiNode,
        menuSelector: String,
        menuLabel: String,
    ): StepOutcome {
        if (!driver.longClick(bubble)) {
            return StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "长按消息气泡被界面拒绝")
        }
        val menu = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「$menuLabel」菜单项") {
            rules.find(it, menuSelector)
        } ?: return StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "长按菜单「$menuLabel」未在 ${timeouts.awaitNodeMs}ms 内出现（消息状态不支持该操作？）",
        )
        return if (driver.click(menu)) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "点击「$menuLabel」被界面拒绝")
        }
    }

    /**
     * 回企微消息首页（201/283 共用）：已在首页（底部 Tab 可见）→ 空过；
     * 否则逐层返回（上限 [maxBacks]），首页仍不可见 → NODE_NOT_FOUND。
     */
    fun goHome(
        driver: UiDriver,
        rules: WeworkScreenRules,
        maxBacks: Int = 8,
    ): StepOutcome {
        fun atHome(): Boolean =
            driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.CONTACTS_TAB) != null } == true

        if (atHome()) return StepOutcome.Success
        var backs = 0
        while (backs < maxBacks && !atHome()) {
            if (!driver.goBack()) break
            backs++
        }
        return if (atHome()) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "返回后仍未到企微消息首页（$backs 次返回，主界面 Tab 不可见）",
            )
        }
    }

    /**
     * 多目标指令的会话定位（当前界面找不到时先回首页再定位——目标间不残留
     * 上一目标的聊天页）。单目标等价 [CommonSteps.openConversation]。
     */
    fun openConversationViaHome(
        driver: UiDriver,
        rules: WeworkScreenRules,
        conversationName: String,
    ): StepOutcome {
        val root = driver.currentRoot()
        val direct = root?.let { rules.findByText(it, conversationName) }
        if (direct != null && driver.click(direct)) {
            return StepOutcome.Success
        }
        val home = goHome(driver, rules)
        if (home is StepOutcome.Failure) return home
        return CommonSteps.openConversation(rules, conversationName).action(driver)
    }

    /** 分页滚动容器（逐群/逐成员读取复用；滚动被拒=末页安全收敛口径）。 */
    fun scrollOnce(
        driver: UiDriver,
        rules: WeworkScreenRules,
        containerSelector: String,
        what: String,
    ): Boolean {
        val container = driver.currentRoot()?.let { rules.find(it, containerSelector) } ?: return false
        return driver.scroll(container, ScrollDirection.FORWARD)
    }
}
