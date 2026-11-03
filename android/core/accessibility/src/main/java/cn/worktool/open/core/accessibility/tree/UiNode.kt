/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.tree

/**
 * 无障碍节点抽象（T2.4）。
 *
 * 设计目的：让节点定位规则引擎与原子操作库可以脱离 Android 框架在 JVM 单测中验证语义
 * （docs/03 T2.4：规则引擎 JVM 单测 + 原子操作以接口 + Fake 节点单测）。
 * 真机侧由 `android/AndroidUiNode` 包装 `AccessibilityNodeInfo` 实现；
 * 测试侧由 `src/test/.../fake/FakeUiNode` 实现。
 *
 * 本接口不引用任何 android.* 类型；动作以 [NodeAction] 常量表达，
 * 避免测试对 `AccessibilityNodeInfo.ACTION_*` 框架常量的依赖。
 */
interface UiNode {

    /** 节点文本（`AccessibilityNodeInfo.getText`），无则为 null。 */
    val text: String?

    /** 内容描述（contentDescription），无则为 null。 */
    val contentDescription: String?

    /** 资源 ID 全名（如 `com.tencent.wework:id/chat_send`），无则为 null。
     *  真机侧需服务配置 `flagReportViewIds` 才会被框架填充。 */
    val viewIdResourceName: String?

    /** 完整类名（如 `android.widget.TextView`），可为 null。 */
    val className: String?

    val isClickable: Boolean
    val isScrollable: Boolean
    val isEditable: Boolean

    val childCount: Int

    /** 屏幕坐标边界（规则引擎仅用于展示与手势回退取中心点）。 */
    val bounds: NodeRect

    /** 返回指定下标子节点；越界或获取失败返回 null。 */
    fun childAt(index: Int): UiNode?

    /** 父节点；根节点或不可得时为 null（sibling 关系解析依赖）。 */
    val parent: UiNode?

    /**
     * 在该节点上执行动作。
     *
     * @param action [NodeAction] 常量
     * @param textArgument 仅 [NodeAction.SET_TEXT] 使用：要设置的文本
     * @return 动作是否被节点接受并执行成功（框架语义：不支持的动作返回 false）
     */
    fun performAction(action: Int, textArgument: String? = null): Boolean

    /**
     * 节点稳定身份键（T4.4 同一性检查，docs/11 B1：同底层节点不同 wrapper 不得误判为不同目标）。
     *
     * 值语义：`className|viewIdResourceName|text|contentDescription|bounds` 拼接。
     * 真机侧同一 `AccessibilityNodeInfo` 经多次查询会得到**不同 wrapper 对象**（引用比较
     * 失效），但上述值域在同帧内一致 → 值键即同一性。Android 实现可在框架暴露更强键
     * （如稳定的 entry/hash）时覆写本方法；测试 Fake 不需覆写（默认实现即正确）。
     */
    fun identityKey(): String =
        listOf(className, viewIdResourceName, text, contentDescription, bounds.toString())
            .joinToString("|")
}

/** 纯 Kotlin 矩形（避免依赖 android.graphics.Rect，保持 JVM 可测）。 */
data class NodeRect(
    val left: Int,
    val top: Int,
    val right: Int,
    val bottom: Int,
) {
    val width: Int get() = right - left
    val height: Int get() = bottom - top
    val centerX: Int get() = left + width / 2
    val centerY: Int get() = top + height / 2
    val isEmpty: Boolean get() = width <= 0 || height <= 0

    override fun toString(): String = "[$left,$top,$right,$bottom]"
}

/**
 * 语义动作常量（与框架 `AccessibilityNodeInfo.ACTION_*` 一一映射，见 `android/AndroidUiNode`）。
 * 使用自持常量而非框架值，使规则引擎/操作库可在 JVM 单测中断言。
 */
object NodeAction {
    const val CLICK = 1
    const val LONG_CLICK = 2
    const val SCROLL_FORWARD = 3
    const val SCROLL_BACKWARD = 4
    const val SET_TEXT = 5
    const val PASTE = 6
}

/** 滚动方向（scroll 原子操作入参）。 */
enum class ScrollDirection {
    /** 向前（列表向下滚动、内容向后翻页，框架 ACTION_SCROLL_FORWARD）。 */
    FORWARD,

    /** 向后（列表向上回滚，框架 ACTION_SCROLL_BACKWARD）。 */
    BACKWARD,
}
