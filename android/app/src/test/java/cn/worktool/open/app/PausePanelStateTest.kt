/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 暂停/恢复按钮 UI 映射与分派（FR-52 两步恢复不可绕过）。 */
class PausePanelStateTest {

    private var now = 0L
    private val manager = PauseManager(clock = { now })

    @Test
    fun `RUNNING态 - 主按钮=一键暂停`() {
        val p = PausePanelState.of(manager.state)
        assertEquals(PausePanelState.Action.PAUSE, p.action)
        assertEquals(PausePanelState.STATE_RUNNING, p.stateCode)
        assertTrue(!p.showCancelResume)
    }

    @Test
    fun `分派pause后 - PAUSED态主按钮=发起恢复`() {
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.PAUSE, now, actor = "app-main")
        assertEquals(PauseManager.State.PAUSED, manager.state)
        val p = PausePanelState.of(manager.state)
        assertEquals(PausePanelState.Action.REQUEST_RESUME, p.action)
        assertEquals(PausePanelState.STATE_PAUSED, p.stateCode)
    }

    @Test
    fun `两步恢复 - 发起后RESUME_PENDING且次按钮出现`() {
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.PAUSE, now, actor = "app-main")
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.REQUEST_RESUME, now, actor = "app-main")
        assertEquals(PauseManager.State.RESUME_PENDING, manager.state)
        val p = PausePanelState.of(manager.state)
        assertEquals(PausePanelState.Action.CONFIRM_RESUME, p.action)
        assertTrue(p.showCancelResume)
    }

    @Test
    fun `两步恢复 - 确认后回RUNNING且审计留痕`() {
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.PAUSE, now, actor = "app-main")
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.REQUEST_RESUME, now, actor = "app-main")
        now += 100
        PausePanelState.dispatchPrimary(manager, PausePanelState.Action.CONFIRM_RESUME, now, actor = "app-main")
        assertEquals(PauseManager.State.RUNNING, manager.state)
        val types = manager.auditTrail().map { it.type }
        assertTrue(types.containsAll(listOf("paused", "resume_requested", "resume_confirmed")))
    }

    @Test(expected = PauseManager.PauseConfirmationRequired::class)
    fun `门禁 - 从PAUSED直接确认恢复被task-engine拒绝（UI映射永不发起该调用）`() {
        manager.pause(actor = "test")
        // 非法路径的对照证明：直接 confirmResume 抛 PauseConfirmationRequired；
        // PausePanelState.of(PAUSED) 只产出 REQUEST_RESUME，UI 无法绕过两步确认
        manager.confirmResume()
    }
}
