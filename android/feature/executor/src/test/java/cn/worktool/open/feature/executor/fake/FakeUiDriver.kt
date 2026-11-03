/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fake

import cn.worktool.open.core.accessibility.ops.AtomicOperations
import cn.worktool.open.core.accessibility.ops.OpResult
import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.feature.executor.runtime.PauseManager
import cn.worktool.open.feature.executor.runtime.UiDriver

/** 假时钟：sleep 即推进（超时场景确定性复现，不依赖真实时间）。 */
class FakeClock(var now: Long = 0L) {
    fun advance(ms: Long) {
        now += ms
    }
}

/** 一屏假节点树（具名，供导航与断言引用）。 */
class FakeScreen(val name: String, val root: FakeExecutorNode)

/** FR-52 暂停门假实现：测试随时翻转暂停态。 */
class FakePauseGate(
    var paused: Boolean = false,
    var source: String? = null,
) : PauseManager {
    override fun isPaused(): Boolean = paused
    override fun pauseSource(): String? = source.takeIf { paused }
}

/**
 * 假驱动（T3.1 JVM 单测）：UiDriver 的界面态机实现。
 *
 * - 屏幕栈：[show] 置根屏；[push] 前进（goBack 可回退）；[pop] 回退；
 * - 界面跳转 = 注册在 (屏, 节点) 上的效果闭包（`onClick(screen, node) { ... }`），
 *   未注册跳转的点击走 [AtomicOperations]（与真机同一原子操作语义：节点不支持
 *   且无回退通道 = 失败），用于模拟「点击被界面拒绝」；
 * - 全部动作记入 [actionLog]（干跑断言「零动作」、暂停断言「零副作用」的数据源）；
 * - [onAfterAction] 钩子供测试在特定动作后翻转暂停门（FR-52 步间断点场景）。
 */
class FakeUiDriver(
    private val clock: FakeClock = FakeClock(),
) : UiDriver {

    private val atomic = AtomicOperations(gestures = null, clipboard = null)

    private val screens = LinkedHashMap<String, FakeScreen>()
    private val backStack = ArrayDeque<String>()

    private data class Registered(val screen: String, val node: FakeExecutorNode, val effect: FakeUiDriver.() -> Unit)

    private val transitions = mutableListOf<Registered>()

    /** 滚动效果注册（分页夹具用：滚动容器追加下一页条目，模拟懒加载）。 */
    private data class RegisteredScroll(val screen: String, val node: FakeExecutorNode, val effect: FakeUiDriver.() -> Unit)

    private val scrollTransitions = mutableListOf<RegisteredScroll>()

    /** 动作日志：`click(...)` / `input(...)` / `scroll(...)` / `back` / `sleep(ms)`。 */
    val actionLog = mutableListOf<String>()

    /** 每个动作后回调（测试注入；返回 Unit，可翻转外部状态如暂停门）。 */
    var onAfterAction: (() -> Unit)? = null

    var current: FakeScreen? = null
        private set

    // ---- 导航 ----

    /** 置根屏（清空回退栈）。 */
    fun show(screen: FakeScreen) {
        screens[screen.name] = screen
        backStack.clear()
        backStack.addLast(screen.name)
        current = screen
    }

    /** 前进到新屏（保留回退栈，goBack 可回）。 */
    fun push(screen: FakeScreen) {
        screens[screen.name] = screen
        backStack.addLast(screen.name)
        current = screen
    }

    /** 回退一屏；已在根屏返回 false。 */
    fun pop(): Boolean {
        if (backStack.size <= 1) return false
        backStack.removeLast()
        current = screens[backStack.last()]
        return true
    }

    /** 注册点击跳转：在 [screen] 屏点击 [node] 触发 [effect]（导航/界面态变迁）。 */
    fun onClick(screen: FakeScreen, node: FakeExecutorNode, effect: FakeUiDriver.() -> Unit) {
        transitions += Registered(screen.name, node, effect)
    }

    /** 注册滚动效果：在 [screen] 屏滚动 [node]（容器）触发 [effect]（如追加下一页）。 */
    fun onScroll(screen: FakeScreen, node: FakeExecutorNode, effect: FakeUiDriver.() -> Unit) {
        scrollTransitions += RegisteredScroll(screen.name, node, effect)
    }

    // ---- UiDriver ----

    override fun currentRoot(): UiNode? = current?.root

    override fun click(node: UiNode): Boolean {
        actionLog += "click(${describe(node)})"
        // 跳转仅在节点可点击时生效（不可点击=按钮置灰/禁用，模拟 ACTION_REJECTED）
        val hit = transitions.firstOrNull { it.screen == current?.name && it.node === node }
        val result = if (hit != null && node.isClickable) {
            hit.effect(this)
            true
        } else {
            atomic.click(node) is OpResult.Ok
        }
        onAfterAction?.invoke()
        return result
    }

    /** 长按：与 click 同一跳转注册表（长按菜单：回复/转发/撤回/多选），否则走原子长按。 */
    override fun longClick(node: UiNode): Boolean {
        actionLog += "longclick(${describe(node)})"
        val hit = transitions.firstOrNull { it.screen == current?.name && it.node === node }
        val result = if (hit != null && node.isClickable) {
            hit.effect(this)
            true
        } else {
            atomic.longClick(node) is OpResult.Ok
        }
        onAfterAction?.invoke()
        return result
    }

    override fun inputText(node: UiNode, text: String): Boolean {
        actionLog += "input(${describe(node)}, \"${text.take(24)}\")"
        val ok = atomic.inputText(node, text) is OpResult.Ok
        onAfterAction?.invoke()
        return ok
    }

    override fun scroll(node: UiNode, direction: ScrollDirection): Boolean {
        actionLog += "scroll(${describe(node)}, $direction)"
        // 懒加载效果仅在容器可滚动时生效（与点击跳转同口径：不可滚动=真机末页拒滚）
        val hit = scrollTransitions.firstOrNull { it.screen == current?.name && it.node === node }
        val result = if (hit != null && node.isScrollable) {
            hit.effect(this)
            true
        } else {
            atomic.scroll(node, direction) is OpResult.Ok
        }
        onAfterAction?.invoke()
        return result
    }

    override fun goBack(): Boolean {
        actionLog += "back"
        val ok = pop()
        onAfterAction?.invoke()
        return ok
    }

    override fun sleep(ms: Long) {
        actionLog += "sleep($ms)"
        clock.advance(ms)
    }

    override fun nowMillis(): Long = clock.now

    private fun describe(node: UiNode): String = buildString {
        append(node.viewIdResourceName ?: node.className ?: "node")
        node.text?.let { append(" text=\"").append(it.take(16)).append('"') }
    }
}
