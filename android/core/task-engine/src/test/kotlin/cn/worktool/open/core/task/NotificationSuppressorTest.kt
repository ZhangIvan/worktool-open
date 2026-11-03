/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-58 自动关闭企微新消息通知——纯 JVM 状态机测试（不引 Robolectric）。
 *
 * 契约锚点：开关**默认关**（docs/01 FR-58）；幂等——**已关不重复操作** +
 * 会话级 DONE 短路；失败必走 navigateBack 补偿；选择器待真机校准（navigator 为端口）。
 */
class NotificationSuppressorTest {

    /** Fake 导航器：全部调用留痕，行为可编程（noticeOn 队列按读次弹出）。 */
    private class FakeNavigator(
        var openOk: Boolean = true,
        var noticeOnQueue: ArrayDeque<Boolean?> = ArrayDeque(listOf(true)),
        var clickOk: Boolean = true,
        var backOk: Boolean = true,
    ) : WeComNoticeNavigator {
        val calls = ArrayList<String>()
        override fun openNoticeSettings(): Boolean {
            calls.add("open")
            return openOk
        }
        override fun isNoticeOn(): Boolean? {
            calls.add("read")
            return if (noticeOnQueue.isEmpty()) null else noticeOnQueue.removeFirst()
        }
        override fun clickNoticeSwitch(): Boolean {
            calls.add("click")
            return clickOk
        }
        override fun navigateBack(): Boolean {
            calls.add("back")
            return backOk
        }
    }

    private val navigator = FakeNavigator()
    private val logs = ArrayList<String>()

    private fun suppressor(enabled: Boolean, maxAttempts: Int = NotificationSuppressor.DEFAULT_MAX_ATTEMPTS) =
        NotificationSuppressor(
            navigator = navigator,
            enabledProvider = { enabled },
            maxAttempts = maxAttempts,
            log = { logs.add(it) },
        )

    @Test
    fun `开关默认关 - SKIPPED_DISABLED 且零 UI 动作`() {
        // 构造器默认 enabledProvider = { false }（FR-58「开关默认关」）
        val def = NotificationSuppressor(navigator)
        assertEquals(NotificationSuppressor.Outcome.SKIPPED_DISABLED, def.suppressOnMainInit())
        assertEquals(NotificationSuppressor.State.IDLE, def.state)
        assertTrue(navigator.calls.isEmpty())
    }

    @Test
    fun `已关幂等 - 只导航退出不点击`() {
        navigator.noticeOnQueue = ArrayDeque(listOf(false))
        val s = suppressor(enabled = true)
        assertEquals(NotificationSuppressor.Outcome.DONE_ALREADY_OFF, s.suppressOnMainInit())
        assertEquals(listOf("open", "read", "back"), navigator.calls) // 无 click
        assertEquals(NotificationSuppressor.State.DONE, s.state)
    }

    @Test
    fun `开到关成功路径 - 点击复核退出并 DONE_TOGGLED_OFF`() {
        navigator.noticeOnQueue = ArrayDeque(listOf(true, false)) // 首读=开，点击后复核=关
        val s = suppressor(enabled = true)
        assertEquals(NotificationSuppressor.Outcome.DONE_TOGGLED_OFF, s.suppressOnMainInit())
        assertEquals(
            listOf("open", "read", "click", "read", "back"),
            navigator.calls,
        )
        assertEquals(NotificationSuppressor.State.DONE, s.state)
    }

    @Test
    fun `会话级幂等 - DONE 后重复初始化零 UI 动作`() {
        navigator.noticeOnQueue = ArrayDeque(listOf(false))
        val s = suppressor(enabled = true)
        s.suppressOnMainInit()
        val callsAfterFirst = navigator.calls.size
        assertEquals(NotificationSuppressor.Outcome.SKIPPED_ALREADY_DONE, s.suppressOnMainInit())
        assertEquals(callsAfterFirst, navigator.calls.size) // 主功能重复初始化不再操作
    }

    @Test
    fun `导航失败 - FAILED_NAVIGATION 且不读不点`() {
        navigator.openOk = false
        val s = suppressor(enabled = true)
        assertEquals(NotificationSuppressor.Outcome.FAILED_NAVIGATION, s.suppressOnMainInit())
        assertEquals(listOf("open"), navigator.calls)
        assertEquals(NotificationSuppressor.State.FAILED, s.state)
    }

    @Test
    fun `开关不可读 - FAILED_READ 并 back 补偿`() {
        navigator.noticeOnQueue = ArrayDeque(listOf(null))
        val s = suppressor(enabled = true)
        assertEquals(NotificationSuppressor.Outcome.FAILED_READ, s.suppressOnMainInit())
        assertEquals(listOf("open", "read", "back"), navigator.calls) // 补偿退出设置页
        assertEquals(NotificationSuppressor.State.FAILED, s.state)
    }

    @Test
    fun `点击未生效 - FAILED_TOGGLE 并 back 补偿`() {
        navigator.noticeOnQueue = ArrayDeque(listOf(true, true)) // 点击后复核仍为开
        navigator.clickOk = false // 点击动作失败
        val s = suppressor(enabled = true)
        assertEquals(NotificationSuppressor.Outcome.FAILED_TOGGLE, s.suppressOnMainInit())
        assertEquals(listOf("open", "read", "click", "back"), navigator.calls)
        assertEquals(NotificationSuppressor.State.FAILED, s.state)
    }

    @Test
    fun `尝试上限 - FAILED 后耗尽次数短路，reset 恢复`() {
        navigator.openOk = false
        val s = suppressor(enabled = true, maxAttempts = 2)
        assertEquals(NotificationSuppressor.Outcome.FAILED_NAVIGATION, s.suppressOnMainInit())
        assertEquals(NotificationSuppressor.Outcome.FAILED_NAVIGATION, s.suppressOnMainInit())
        assertEquals(NotificationSuppressor.Outcome.SKIPPED_ALREADY_DONE, s.suppressOnMainInit())
        assertEquals(2, s.attempts)
        s.reset()
        assertEquals(0, s.attempts)
        assertEquals(NotificationSuppressor.Outcome.FAILED_NAVIGATION, s.suppressOnMainInit()) // 重置后可重试
    }
}
