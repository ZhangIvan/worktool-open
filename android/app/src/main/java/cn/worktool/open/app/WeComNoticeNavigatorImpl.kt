/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.view.accessibility.AccessibilityNodeInfo
import cn.worktool.open.core.accessibility.WorkToolAccessibilityService
import cn.worktool.open.core.task.WeComNoticeNavigator

/**
 * FR-58 企微「新消息通知」导航器（[WeComNoticeNavigator] 生产实现，纯 app 侧薄壳——
 * 状态机/幂等逻辑在纯 JVM NotificationSuppressor，Fake 注入测试）。
 *
 * 导航流（APK j2/o.java:1524-1550「主功能初始化自动流程: 尝试关闭新消息通知」同位）：
 * 我 → 设置 → 新消息通知 →（读开关 → 必要时点关）→ 返回。
 *
 * 选择器口径：引用 core/accessibility 规则命名空间（WeComRulePack.SELECTOR_NAMESPACE
 * = `wework.`，SelectorEngine text/className 匹配语义），本文件自持文案选择器——
 * **选择器待真机校准**（W-M6 占位：企微版本间文案/层级存在分形，校准结果迁移回
 * WeComRulePack.selectors.json 另行任务，届时本类改消费规则包 ID）。
 *
 * 失败语义：服务未连接/节点找不到/点击被拒一律 false 或 null（超时收敛，不抛异常——
 * 崩溃面收敛与 AccessibilityUiDriver 同口径）。
 */
class WeComNoticeNavigatorImpl(
    /** 单步等待节点出现超时（企微页面切换的现实耗时；待真机校准）。 */
    private val stepTimeoutMillis: Long = DEFAULT_STEP_TIMEOUT_MS,
    private val pollIntervalMillis: Long = DEFAULT_POLL_INTERVAL_MS,
) : WeComNoticeNavigator {

    private val service: WorkToolAccessibilityService?
        get() = WorkToolAccessibilityService.instance

    private fun root(): AccessibilityNodeInfo? = service?.rootInActiveWindow

    override fun openNoticeSettings(): Boolean {
        // 选择器待真机校准：wework.nav.me_tab → wework.settings.entry →
        // wework.settings.new_message_notice（文案三连导航）
        val steps = listOf(SELECTOR_ME_TAB, SELECTOR_SETTINGS_ENTRY, SELECTOR_NEW_MESSAGE_NOTICE)
        for (text in steps) {
            val node = awaitNode { findByText(text) } ?: return false
            if (!click(node)) return false
            awaitSettle()
        }
        return true
    }

    override fun isNoticeOn(): Boolean? {
        val end = System.currentTimeMillis() + stepTimeoutMillis
        while (System.currentTimeMillis() < end) {
            // 选择器待真机校准：wework.settings.new_message_notice_switch
            // （企微通知开关为 Switch 形态；LinearLayout 行内开关经父链找）
            val switch = findSwitch(root())
            if (switch != null) return switch.isChecked
            sleep(pollIntervalMillis)
        }
        return null // 找不到/读不出 → 调用方按失败处理
    }

    override fun clickNoticeSwitch(): Boolean {
        val switch = findSwitch(root()) ?: return false
        return click(switch)
    }

    override fun navigateBack(): Boolean =
        service?.performGlobalAction(GLOBAL_ACTION_BACK) == true

    // ---- 内部工具（无障碍树 BFS；accessibility 模块不动，选择器校准后迁移消费规则包） ----

    /** 文案精确匹配节点（BFS 全树）。 */
    private fun findByText(text: String): AccessibilityNodeInfo? {
        val root = root() ?: return null
        val queue = ArrayDeque<AccessibilityNodeInfo>()
        queue.addLast(root)
        while (queue.isNotEmpty()) {
            val n = queue.removeFirst()
            if (n.text?.toString() == text) return n
            for (i in 0 until n.childCount) n.getChild(i)?.let { queue.addLast(it) }
        }
        return null
    }

    /** 找通知开关（Switch 形态；不可点击则上溯最近可点击父节点由 [click] 处理）。 */
    private fun findSwitch(root: AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        if (root == null) return null
        val queue = ArrayDeque<AccessibilityNodeInfo>()
        queue.addLast(root)
        while (queue.isNotEmpty()) {
            val n = queue.removeFirst()
            if (n.className?.toString() == SWITCH_CLASS) return n
            for (i in 0 until n.childCount) n.getChild(i)?.let { queue.addLast(it) }
        }
        return null
    }

    /** 点击（节点不可点击则上溯可点击祖先；ACTION_CLICK 失败返回 false——手势补点归校准任务）。 */
    private fun click(node: AccessibilityNodeInfo): Boolean {
        var n: AccessibilityNodeInfo? = node
        var depth = 0
        while (n != null && depth++ < MAX_PARENT_DEPTH) {
            if (n.isClickable && n.performAction(AccessibilityNodeInfo.ACTION_CLICK)) return true
            n = n.parent
        }
        return node.performAction(AccessibilityNodeInfo.ACTION_CLICK)
    }

    /** 等待节点出现（轮询至超时；页面切换异步收敛）。 */
    private fun awaitNode(find: () -> AccessibilityNodeInfo?): AccessibilityNodeInfo? {
        val end = System.currentTimeMillis() + stepTimeoutMillis
        while (System.currentTimeMillis() < end) {
            find()?.let { return it }
            sleep(pollIntervalMillis)
        }
        return null
    }

    private fun awaitSettle() = sleep(pollIntervalMillis)

    private fun sleep(ms: Long) {
        try {
            Thread.sleep(ms)
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }

    companion object {
        /** 选择器待真机校准（W-M6 占位文案；校准后迁移 WeComRulePack.selectors.json）。 */
        const val SELECTOR_ME_TAB = "我" // wework.nav.me_tab
        const val SELECTOR_SETTINGS_ENTRY = "设置" // wework.settings.entry
        const val SELECTOR_NEW_MESSAGE_NOTICE = "新消息通知" // wework.settings.new_message_notice

        /** 企微通知开关控件形态（待真机校准：wework.settings.new_message_notice_switch）。 */
        const val SWITCH_CLASS = "android.widget.Switch"

        const val DEFAULT_STEP_TIMEOUT_MS: Long = 5_000
        const val DEFAULT_POLL_INTERVAL_MS: Long = 300
        const val MAX_PARENT_DEPTH: Int = 6

        /** 框架常量隔离（AccessibilityService.GLOBAL_ACTION_BACK，同 AccessibilityUiDriver 手法）。 */
        const val GLOBAL_ACTION_BACK: Int = 1 // android.accessibilityservice.AccessibilityService.GLOBAL_ACTION_BACK
    }
}
