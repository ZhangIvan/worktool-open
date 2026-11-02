/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.fake

import cn.worktool.open.core.accessibility.ops.ClipboardGateway
import cn.worktool.open.core.accessibility.ops.GestureDispatcher
import cn.worktool.open.core.accessibility.ops.ScreenshotApi
import cn.worktool.open.core.accessibility.ops.ScreenshotResult
import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.NodeRect
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * JVM 单测 Fake 节点（docs/03 T2.4：原子操作以接口 + Fake 节点单测语义）。
 *
 * 树构造：`node { child(...) }` 或 [add]；add 时自动回填 parent 引用。
 * 动作执行记录在 [performedActions]，可用 [supportedActions] 模拟「节点不支持某 ACTION」。
 */
class FakeUiNode(
    override val text: String? = null,
    override val contentDescription: String? = null,
    override val viewIdResourceName: String? = null,
    override val className: String? = null,
    override val isClickable: Boolean = false,
    override val isScrollable: Boolean = false,
    override val isEditable: Boolean = false,
    override val bounds: NodeRect = NodeRect(0, 0, 100, 100),
) : UiNode {

    val children = mutableListOf<FakeUiNode>()
    override val childCount: Int get() = children.size

    /** 各节点是否支持某 ACTION（默认全支持；删除元素=模拟不支持）。 */
    val supportedActions: MutableSet<Int> = mutableSetOf(
        NodeAction.CLICK,
        NodeAction.LONG_CLICK,
        NodeAction.SCROLL_FORWARD,
        NodeAction.SCROLL_BACKWARD,
        NodeAction.SET_TEXT,
        NodeAction.PASTE,
    )

    /** (action, textArgument) 记录，按调用顺序。 */
    val performedActions = mutableListOf<Pair<Int, String?>>()

    override fun childAt(index: Int): UiNode? = children.getOrNull(index)

    override var parent: UiNode? = null

    /** 挂子节点并回填 parent（链式构造）。 */
    fun add(child: FakeUiNode): FakeUiNode {
        children += child
        child.parent = this
        return this
    }

    override fun performAction(action: Int, textArgument: String?): Boolean {
        performedActions += action to textArgument
        return action in supportedActions
    }
}

/** 手势 Fake：记录调用，可配置派发结果。 */
class FakeGestureDispatcher(
    private val result: Boolean = true,
) : GestureDispatcher {
    data class Call(val kind: String, val x: Int, val y: Int)

    val calls = mutableListOf<Call>()

    override fun tap(x: Int, y: Int): Boolean {
        calls += Call("tap", x, y)
        return result
    }

    override fun longPress(x: Int, y: Int): Boolean {
        calls += Call("longPress", x, y)
        return result
    }
}

/** 剪贴板 Fake：记录写入，可配置结果。 */
class FakeClipboardGateway(
    private val result: Boolean = true,
) : ClipboardGateway {
    val written = mutableListOf<String>()

    override fun setText(text: String): Boolean {
        written += text
        return result
    }
}

/** 截图通道 Fake：可配置 available 与回调结果。 */
class FakeScreenshotApi(
    override val available: Boolean,
    private val result: ScreenshotResult,
) : ScreenshotApi {
    var takeCalls = 0
        private set

    override fun take(callback: (ScreenshotResult) -> Unit) {
        takeCalls++
        callback(result)
    }
}
