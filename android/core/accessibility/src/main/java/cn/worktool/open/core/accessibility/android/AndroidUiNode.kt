/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.graphics.Rect
import android.os.Bundle
import android.view.accessibility.AccessibilityNodeInfo
import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.NodeRect
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * [UiNode] 的真机实现：包装框架 `AccessibilityNodeInfo`。
 *
 * 仅做字段/动作映射，无业务逻辑——语义逻辑全部在 JVM 可测的
 * 规则引擎（`rules/`）与原子操作库（`ops/`）中。
 * 注意：每次 [childAt]/[parent] 返回新包装实例，节点身份比较（`===`）不可跨调用；
 * 规则引擎的 sibling-by-match 语义依赖 `!==`，在单次解析路径内成立。
 */
class AndroidUiNode(private val node: AccessibilityNodeInfo) : UiNode {

    override val text: String? get() = node.text?.toString()
    override val contentDescription: String? get() = node.contentDescription?.toString()
    override val viewIdResourceName: String? get() = node.viewIdResourceName
    override val className: String? get() = node.className?.toString()
    override val isClickable: Boolean get() = node.isClickable
    override val isScrollable: Boolean get() = node.isScrollable
    override val isEditable: Boolean get() = node.isEditable
    override val childCount: Int get() = node.childCount

    override val bounds: NodeRect
        get() {
            val r = Rect()
            node.getBoundsInScreen(r)
            return NodeRect(r.left, r.top, r.right, r.bottom)
        }

    override fun childAt(index: Int): UiNode? = node.getChild(index)?.let(::AndroidUiNode)

    override val parent: UiNode? get() = node.parent?.let(::AndroidUiNode)

    override fun performAction(action: Int, textArgument: String?): Boolean = when (action) {
        NodeAction.CLICK -> node.performAction(AccessibilityNodeInfo.ACTION_CLICK)
        NodeAction.LONG_CLICK -> node.performAction(AccessibilityNodeInfo.ACTION_LONG_CLICK)
        NodeAction.SCROLL_FORWARD -> node.performAction(AccessibilityNodeInfo.ACTION_SCROLL_FORWARD)
        NodeAction.SCROLL_BACKWARD -> node.performAction(AccessibilityNodeInfo.ACTION_SCROLL_BACKWARD)
        NodeAction.SET_TEXT -> node.performAction(
            AccessibilityNodeInfo.ACTION_SET_TEXT,
            Bundle().apply {
                putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, textArgument)
            },
        )
        NodeAction.PASTE -> node.performAction(AccessibilityNodeInfo.ACTION_PASTE)
        else -> false
    }
}
