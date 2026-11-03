/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * U2 悬浮窗控制状态机测试（docs/16 U2 / FR-50 最小集；纯 JVM）。
 * 覆盖：RUNNING 单击暂停（actor=floating-window）/ PAUSED 首击武装二击确认 /
 * 3s 窗口超时重新武装 / RESUME_PENDING 单击即确认 / 按钮动作映射 /
 * 确认后回 RUNNING 并冲刷暂存指令。
 */
class FloatControlUiTest {

    private var now = 10_000L
    private val manager = PauseManager(clock = { now })

    private fun confirm() = TwoClickConfirm()

    @Test
    fun `RUNNING 单击-立即暂停 actor=floating-window`() {
        val c = confirm()
        val outcome = FloatControlUi.onPrimary(manager, c, now)
        assertEquals(FloatControlUi.Outcome.PAUSED_NOW, outcome)
        assertEquals(PauseManager.State.PAUSED, manager.state)
        assertEquals("floating-window", manager.pauseSource())
    }

    @Test
    fun `PAUSED 首击-武装确认不恢复`() {
        manager.pause(now = now)
        val c = confirm()
        val outcome = FloatControlUi.onPrimary(manager, c, now)
        assertEquals(FloatControlUi.Outcome.RESUME_ASK_CONFIRM, outcome)
        assertEquals("首击不得恢复执行", PauseManager.State.PAUSED, manager.state)
        assertTrue("武装态置位", c.isArmed(now))
    }

    @Test
    fun `PAUSED 二击-3s 内确认恢复-两步语义不绕过`() {
        manager.pause(now = now)
        manager.enqueue(PauseManager.DeferredCommand("c1", 203, receivedAtMillis = now), now = now)
        val c = confirm()
        FloatControlUi.onPrimary(manager, c, now) // 首击武装
        now += 1_000 // 窗口内
        val outcome = FloatControlUi.onPrimary(manager, c, now)
        assertEquals(FloatControlUi.Outcome.RESUMED_NOW, outcome)
        assertEquals(PauseManager.State.RUNNING, manager.state)
        assertTrue("恢复须冲刷暂存指令（confirmResume 语义）", manager.deferred().isEmpty())
    }

    @Test
    fun `武装超时-二击重新武装不恢复`() {
        manager.pause(now = now)
        val c = confirm()
        FloatControlUi.onPrimary(manager, c, now) // 武装 @10_000
        now += FloatControlUi.CONFIRM_WINDOW_MILLIS + 1 // 超时
        val outcome = FloatControlUi.onPrimary(manager, c, now)
        assertEquals(FloatControlUi.Outcome.RESUME_ASK_CONFIRM, outcome) // 重新武装
        assertEquals(PauseManager.State.PAUSED, manager.state)
    }

    @Test
    fun `RESUME_PENDING 单击-即第二步显式确认`() {
        manager.pause(now = now)
        manager.requestResume(now = now) // 他处（主界面）已发起
        assertEquals(PauseManager.State.RESUME_PENDING, manager.state)
        val c = confirm()
        val outcome = FloatControlUi.onPrimary(manager, c, now)
        assertEquals(FloatControlUi.Outcome.RESUMED_NOW, outcome)
        assertEquals(PauseManager.State.RUNNING, manager.state)
        assertFalse("确认后解除武装", c.isArmed(now))
    }

    @Test
    fun `按钮动作映射-随暂停态与武装位变化`() {
        assertEquals(FloatControlUi.Action.PAUSE, FloatControlUi.primaryAction(PauseManager.State.RUNNING, false))
        assertEquals(FloatControlUi.Action.RESUME, FloatControlUi.primaryAction(PauseManager.State.PAUSED, false))
        assertEquals(FloatControlUi.Action.CONFIRM_RESUME, FloatControlUi.primaryAction(PauseManager.State.PAUSED, true))
        assertEquals(FloatControlUi.Action.CONFIRM_RESUME, FloatControlUi.primaryAction(PauseManager.State.RESUME_PENDING, false))
    }

    @Test
    fun `TwoClickConfirm 独立语义-武装过期查询不复活`() {
        val c = TwoClickConfirm()
        assertFalse(c.isArmed(now))
        assertFalse("首击 = 武装并返回 false", c.click(now))
        assertTrue(c.isArmed(now))
        now += FloatControlUi.CONFIRM_WINDOW_MILLIS + 1
        assertFalse("超时后 isArmed 为 false", c.isArmed(now))
    }
}
