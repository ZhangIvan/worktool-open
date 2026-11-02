/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.ops

import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 手势回退通道（节点 ACTION 不可用时按 bounds 中心注入手势）。
 * 真机实现见 `android/ServiceGestureDispatcher`；JVM 单测用 Fake 验证调度语义。
 */
interface GestureDispatcher {
    /** 在屏幕坐标 (x,y) 注入点按（短按）。 */
    fun tap(x: Int, y: Int): Boolean

    /** 在屏幕坐标 (x,y) 注入长按。 */
    fun longPress(x: Int, y: Int): Boolean
}

/** 剪贴板通道（inputText 的 PASTE 回退用）。 */
interface ClipboardGateway {
    fun setText(text: String): Boolean
}

/** 原子操作执行方式（结果审计用）。 */
enum class OpMethod { NODE_ACTION, GESTURE, PASTE }

/** 失败原因（稳定字符串，供上层映射错误码 10xxx/20xxx，见 protocol/spec.md §4.3）。 */
enum class FailureReason {
    /** 节点不支持该 ACTION 且无可用回退通道。 */
    UNSUPPORTED_BY_NODE,

    /** 手势回退不可用（未注入 GestureDispatcher）。 */
    GESTURE_UNAVAILABLE,

    /** 手势派发失败（系统拒绝）。 */
    GESTURE_FAILED,

    /** 节点 bounds 为空，无法计算手势坐标。 */
    INVALID_BOUNDS,

    /** 剪贴板回退不可用（未注入 ClipboardGateway 或写入失败）。 */
    CLIPBOARD_UNAVAILABLE,

    /** 入参文本为空。 */
    EMPTY_TEXT,
}

/** 原子操作统一结果。 */
sealed class OpResult {
    data class Ok(val method: OpMethod) : OpResult()
    data class Failed(val reason: FailureReason, val detail: String? = null) : OpResult()
}

/**
 * 原子操作库（T2.4）—— click / longClick / scroll / inputText 四项；
 * 第 5 项 screenshot 见 [ScreenshotOperations]（版本门控独立，见该文件）。
 *
 * 设计：全部依赖以接口注入（[GestureDispatcher]/[ClipboardGateway] 可为 null = 关闭对应回退），
 * 使语义可在 JVM 单测以 FakeUiNode 断言（真机执行链路 T2.5 后真机回归，当前 NOT_RUN 如实标注）。
 *
 * 回退策略（与真机行为一一对应）：
 * - click/longClick：先 ACTION_CLICK/ACTION_LONG_CLICK；失败且注入了手势通道 → bounds 中心点按/长按。
 * - inputText：先 ACTION_SET_TEXT（框架直接设值）；失败且注入剪贴板 → 写剪贴板 + ACTION_PASTE。
 * - scroll：仅 ACTION_SCROLL_FORWARD/BACKWARD（无手势回退——坐标手势滑动需按容器方向计算，
 *   留待 T3.x 按指令需要补充，避免无据的猜测实现）。
 */
class AtomicOperations(
    private val gestures: GestureDispatcher? = null,
    private val clipboard: ClipboardGateway? = null,
) {

    /** 原子操作①：点击。 */
    fun click(node: UiNode): OpResult {
        if (node.performAction(NodeAction.CLICK)) return OpResult.Ok(OpMethod.NODE_ACTION)
        return gestureFallback(node, FailureReason.UNSUPPORTED_BY_NODE) { g, x, y -> g.tap(x, y) }
    }

    /** 原子操作②：长按。 */
    fun longClick(node: UiNode): OpResult {
        if (node.performAction(NodeAction.LONG_CLICK)) return OpResult.Ok(OpMethod.NODE_ACTION)
        return gestureFallback(node, FailureReason.UNSUPPORTED_BY_NODE) { g, x, y -> g.longPress(x, y) }
    }

    /** 原子操作③：滚动（列表/滚动容器节点）。 */
    fun scroll(node: UiNode, direction: ScrollDirection): OpResult {
        val action = when (direction) {
            ScrollDirection.FORWARD -> NodeAction.SCROLL_FORWARD
            ScrollDirection.BACKWARD -> NodeAction.SCROLL_BACKWARD
        }
        return if (node.performAction(action)) {
            OpResult.Ok(OpMethod.NODE_ACTION)
        } else {
            OpResult.Failed(FailureReason.UNSUPPORTED_BY_NODE, "direction=$direction")
        }
    }

    /** 原子操作④：输入文本（整体替换语义，与 ACTION_SET_TEXT 一致）。 */
    fun inputText(node: UiNode, text: String): OpResult {
        if (text.isEmpty()) return OpResult.Failed(FailureReason.EMPTY_TEXT)
        if (node.performAction(NodeAction.SET_TEXT, text)) return OpResult.Ok(OpMethod.NODE_ACTION)
        val clip = clipboard ?: return OpResult.Failed(
            FailureReason.CLIPBOARD_UNAVAILABLE,
            "SET_TEXT 不被支持且未注入剪贴板回退",
        )
        if (!clip.setText(text)) {
            return OpResult.Failed(FailureReason.CLIPBOARD_UNAVAILABLE, "剪贴板写入失败")
        }
        return if (node.performAction(NodeAction.PASTE)) {
            OpResult.Ok(OpMethod.PASTE)
        } else {
            OpResult.Failed(FailureReason.UNSUPPORTED_BY_NODE, "SET_TEXT 与 PASTE 均不被支持")
        }
    }

    private inline fun gestureFallback(
        node: UiNode,
        actionReason: FailureReason,
        dispatch: (GestureDispatcher, Int, Int) -> Boolean,
    ): OpResult {
        val g = gestures ?: return OpResult.Failed(
            FailureReason.GESTURE_UNAVAILABLE,
            "$actionReason 且未注入手势回退",
        )
        val rect = node.bounds
        if (rect.isEmpty) return OpResult.Failed(FailureReason.INVALID_BOUNDS, rect.toString())
        return if (dispatch(g, rect.centerX, rect.centerY)) {
            OpResult.Ok(OpMethod.GESTURE)
        } else {
            OpResult.Failed(FailureReason.GESTURE_FAILED, "at (${rect.centerX},${rect.centerY})")
        }
    }
}
