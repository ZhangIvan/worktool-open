/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fake

import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.NodeRect
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 可变假节点（T3.1 JVM 单测）：企微界面 FakeNode 夹具的构件。
 *
 * 与 core:accessibility 测试侧 FakeUiNode 同一思想（动作记录 + 支持集模拟），
 * 差异：text 可变（模拟 SET_TEXT 输入上屏），供执行器全步骤走通所需的界面态变迁。
 */
class FakeExecutorNode(
    initialText: String? = null,
    override val contentDescription: String? = null,
    override val viewIdResourceName: String? = null,
    private val cls: String = "android.view.View",
    override val isClickable: Boolean = false,
    override val isScrollable: Boolean = false,
    override val isEditable: Boolean = false,
    override val bounds: NodeRect = NodeRect(0, 0, 100, 100),
) : UiNode {

    override val className: String? = cls

    /** 当前文本（SET_TEXT 可变）。 */
    var textValue: String? = initialText
    override val text: String? get() = textValue

    val children = mutableListOf<FakeExecutorNode>()
    override val childCount: Int get() = children.size

    /** (action, textArgument) 调用记录（按顺序）。 */
    val performedActions = mutableListOf<Pair<Int, String?>>()

    override fun childAt(index: Int): UiNode? = children.getOrNull(index)

    override var parent: UiNode? = null

    /** 挂子节点并回填 parent（链式）。 */
    fun add(child: FakeExecutorNode): FakeExecutorNode {
        children += child
        child.parent = this
        return this
    }

    /** 从父节点摘除自身（好友请求行消失等界面态变迁）。 */
    fun detachFromParent() {
        (parent as? FakeExecutorNode)?.children?.remove(this)
        parent = null
    }

    override fun performAction(action: Int, textArgument: String?): Boolean {
        performedActions += action to textArgument
        return when (action) {
            NodeAction.SET_TEXT ->
                if (!isEditable || textArgument == null) {
                    false
                } else {
                    textValue = textArgument
                    true
                }

            NodeAction.CLICK, NodeAction.LONG_CLICK -> isClickable
            else -> true
        }
    }
}

/** 假节点构造 DSL：`fakeNode(text = "发送", cls = TEXT_VIEW, clickable = true) { +child }`。 */
fun fakeNode(
    text: String? = null,
    resId: String? = null,
    cls: String = TEXT_VIEW,
    clickable: Boolean = false,
    editable: Boolean = false,
    scrollable: Boolean = false,
    block: (FakeExecutorNode.() -> Unit)? = null,
): FakeExecutorNode = FakeExecutorNode(
    initialText = text,
    viewIdResourceName = resId,
    cls = cls,
    isClickable = clickable,
    isEditable = editable,
    isScrollable = scrollable,
).also { node -> block?.invoke(node) }

const val TEXT_VIEW = "android.widget.TextView"
const val EDIT_TEXT = "android.widget.EditText"
const val VIEW_GROUP = "android.view.ViewGroup"
