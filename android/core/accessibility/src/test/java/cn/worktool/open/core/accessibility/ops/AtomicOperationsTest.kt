/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.ops

import cn.worktool.open.core.accessibility.fake.FakeClipboardGateway
import cn.worktool.open.core.accessibility.fake.FakeGestureDispatcher
import cn.worktool.open.core.accessibility.fake.FakeUiNode
import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.NodeRect
import cn.worktool.open.core.accessibility.tree.ScrollDirection
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 原子操作语义单测（docs/03 T2.4：接口 + Fake 节点；真机执行链路 NOT_RUN，T2.5 后真机回归）。
 *
 * 覆盖：click/longClick（ACTION 主路 + 手势回退 + 回退不可用 + 空 bounds）、
 * scroll（两方向）、inputText（SET_TEXT 主路 + PASTE 回退 + 空文本 + 无剪贴板）。
 */
class AtomicOperationsTest {

    // ---------- click ----------

    @Test
    fun clickViaNodeAction() {
        val ops = AtomicOperations() // 无手势通道
        val node = FakeUiNode(isClickable = true)
        val r = ops.click(node)
        assertEquals(OpResult.Ok(OpMethod.NODE_ACTION), r)
        assertEquals(listOf(NodeAction.CLICK to null), node.performedActions)
    }

    @Test
    fun clickFallsBackToGestureAtCenter() {
        val gestures = FakeGestureDispatcher()
        val ops = AtomicOperations(gestures = gestures)
        val node = FakeUiNode(isClickable = true, bounds = NodeRect(100, 200, 300, 400))
        node.supportedActions.clear() // 模拟节点不支持 ACTION_CLICK
        val r = ops.click(node)
        assertEquals(OpResult.Ok(OpMethod.GESTURE), r)
        // bounds 中心 (200,300)
        assertEquals(listOf(FakeGestureDispatcher.Call("tap", 200, 300)), gestures.calls)
        // 仍先尝试过 ACTION_CLICK
        assertEquals(listOf(NodeAction.CLICK to null), node.performedActions)
    }

    @Test
    fun clickFailsWhenNoGestureChannel() {
        val ops = AtomicOperations()
        val node = FakeUiNode()
        node.supportedActions.clear()
        val r = ops.click(node)
        assertEquals(FailureReason.GESTURE_UNAVAILABLE, (r as OpResult.Failed).reason)
    }

    @Test
    fun clickFailsOnEmptyBounds() {
        val ops = AtomicOperations(gestures = FakeGestureDispatcher())
        val node = FakeUiNode(bounds = NodeRect(10, 10, 10, 10)) // 宽 0 = 空
        node.supportedActions.clear()
        val r = ops.click(node)
        assertEquals(FailureReason.INVALID_BOUNDS, (r as OpResult.Failed).reason)
    }

    @Test
    fun clickGestureDispatchFailure() {
        val ops = AtomicOperations(gestures = FakeGestureDispatcher(result = false))
        val node = FakeUiNode(bounds = NodeRect(0, 0, 10, 10))
        node.supportedActions.clear()
        val r = ops.click(node)
        assertEquals(FailureReason.GESTURE_FAILED, (r as OpResult.Failed).reason)
    }

    // ---------- longClick ----------

    @Test
    fun longClickViaNodeAction() {
        val ops = AtomicOperations()
        val node = FakeUiNode()
        val r = ops.longClick(node)
        assertEquals(OpResult.Ok(OpMethod.NODE_ACTION), r)
        assertEquals(listOf(NodeAction.LONG_CLICK to null), node.performedActions)
    }

    @Test
    fun longClickFallsBackToGesture() {
        val gestures = FakeGestureDispatcher()
        val ops = AtomicOperations(gestures = gestures)
        val node = FakeUiNode(bounds = NodeRect(0, 0, 100, 100))
        node.supportedActions.remove(NodeAction.LONG_CLICK)
        val r = ops.longClick(node)
        assertEquals(OpResult.Ok(OpMethod.GESTURE), r)
        assertEquals(listOf(FakeGestureDispatcher.Call("longPress", 50, 50)), gestures.calls)
    }

    // ---------- scroll ----------

    @Test
    fun scrollForwardAndBackward() {
        val ops = AtomicOperations()
        val forward = FakeUiNode(isScrollable = true)
        val backward = FakeUiNode(isScrollable = true)
        assertEquals(OpResult.Ok(OpMethod.NODE_ACTION), ops.scroll(forward, ScrollDirection.FORWARD))
        assertEquals(OpResult.Ok(OpMethod.NODE_ACTION), ops.scroll(backward, ScrollDirection.BACKWARD))
        assertEquals(listOf(NodeAction.SCROLL_FORWARD to null), forward.performedActions)
        assertEquals(listOf(NodeAction.SCROLL_BACKWARD to null), backward.performedActions)
    }

    @Test
    fun scrollUnsupportedFailsWithoutGestureFallback() {
        val gestures = FakeGestureDispatcher()
        val ops = AtomicOperations(gestures = gestures)
        val node = FakeUiNode()
        node.supportedActions.clear()
        val r = ops.scroll(node, ScrollDirection.FORWARD)
        assertEquals(FailureReason.UNSUPPORTED_BY_NODE, (r as OpResult.Failed).reason)
        // 语义约定：scroll 不做手势回退（见 AtomicOperations KDoc）
        assertTrue(gestures.calls.isEmpty())
    }

    // ---------- inputText ----------

    @Test
    fun inputTextViaSetText() {
        val ops = AtomicOperations()
        val node = FakeUiNode(isEditable = true)
        val r = ops.inputText(node, "你好")
        assertEquals(OpResult.Ok(OpMethod.NODE_ACTION), r)
        assertEquals(listOf(NodeAction.SET_TEXT to "你好"), node.performedActions)
    }

    @Test
    fun inputTextFallsBackToClipboardPaste() {
        val clip = FakeClipboardGateway()
        val ops = AtomicOperations(clipboard = clip)
        val node = FakeUiNode(isEditable = true)
        node.supportedActions.remove(NodeAction.SET_TEXT)
        val r = ops.inputText(node, "你好")
        assertEquals(OpResult.Ok(OpMethod.PASTE), r)
        assertEquals(listOf("你好"), clip.written)
        assertEquals(
            listOf(NodeAction.SET_TEXT to "你好", NodeAction.PASTE to null),
            node.performedActions,
        )
    }

    @Test
    fun inputTextEmptyRejected() {
        val ops = AtomicOperations(clipboard = FakeClipboardGateway())
        val node = FakeUiNode()
        assertEquals(FailureReason.EMPTY_TEXT, (ops.inputText(node, "") as OpResult.Failed).reason)
        assertTrue(node.performedActions.isEmpty()) // 不触碰节点
    }

    @Test
    fun inputTextNoClipboardChannelFails() {
        val ops = AtomicOperations()
        val node = FakeUiNode()
        node.supportedActions.remove(NodeAction.SET_TEXT)
        val r = ops.inputText(node, "x")
        assertEquals(FailureReason.CLIPBOARD_UNAVAILABLE, (r as OpResult.Failed).reason)
    }

    @Test
    fun inputTextClipboardWriteFailed() {
        val ops = AtomicOperations(clipboard = FakeClipboardGateway(result = false))
        val node = FakeUiNode()
        node.supportedActions.remove(NodeAction.SET_TEXT)
        val r = ops.inputText(node, "x")
        assertEquals(FailureReason.CLIPBOARD_UNAVAILABLE, (r as OpResult.Failed).reason)
        // 剪贴板失败后不应尝试 PASTE
        assertEquals(listOf(NodeAction.SET_TEXT to "x"), node.performedActions)
    }

    @Test
    fun inputTextAllChannelsUnsupported() {
        val ops = AtomicOperations(clipboard = FakeClipboardGateway())
        val node = FakeUiNode()
        node.supportedActions.remove(NodeAction.SET_TEXT)
        node.supportedActions.remove(NodeAction.PASTE)
        val r = ops.inputText(node, "x")
        assertEquals(FailureReason.UNSUPPORTED_BY_NODE, (r as OpResult.Failed).reason)
    }
}
