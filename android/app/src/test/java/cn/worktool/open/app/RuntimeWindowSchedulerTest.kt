/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager
import cn.worktool.open.core.task.RuntimeWindowConfig
import cn.worktool.open.core.task.RuntimeWindowTracker
import cn.worktool.open.core.task.WindowVerdict
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * U1 运行时段调度器测试（docs/16 U1；纯 JVM——evaluateTick 时钟与分钟注入）。
 * 覆盖：出窗 → pause(reason=runtime-window, actor=runtime-window) / 入窗 → 预授权
 * 两步恢复（requestResume+confirmResume）/ 用户手动暂停不被自动恢复（接管优先）/
 * 未启用不动作 / rebase 后不误判迁移 / 休眠提示回调。
 */
class RuntimeWindowSchedulerTest {

    private var now = 1_793_664_000_000L // 2026-11-03 附近（数值仅作时钟注入）
    private var config = RuntimeWindowConfig(enabled = true, startMinuteOfDay = 8 * 60, endMinuteOfDay = 22 * 60)
    private val audits = mutableListOf<PauseManager.AuditEvent>()
    private val manager = PauseManager(clock = { now }, audit = { audits.add(it) })
    private val events = mutableListOf<String>()
    private val notices = mutableListOf<Boolean>()

    /** 可变分钟注入（同一调度器实例跨 tick 换时间——迁移语义要求状态连续）。 */
    private var minute = 12 * 60

    private val scheduler = RuntimeWindowScheduler(
        configProvider = { config },
        pauseManager = manager,
        minuteOfDay = { minute },
        onSleepNotice = { notices.add(it) },
        onEvent = { events.add(it) },
    )

    @Test
    fun `出窗迁移-pause 联动 reason 与 actor 为 runtime-window`() {
        minute = 23 * 60 // 23:00 出窗
        val t = scheduler.evaluateTick(now)
        assertEquals(RuntimeWindowTracker.Transition.EXIT, t)
        assertEquals(PauseManager.State.PAUSED, manager.state)
        val paused = audits.last { it.type == "paused" }
        assertEquals("runtime-window", paused.reason)
        assertEquals("runtime-window", paused.actor)
        assertEquals("runtime-window", manager.pauseSource())
        assertTrue(notices.contains(false)) // 休眠提示
    }

    @Test
    fun `入窗迁移-预授权两步恢复-冲刷暂存指令`() {
        minute = 23 * 60
        scheduler.evaluateTick(now) // 出窗 → 暂停
        manager.enqueue(PauseManager.DeferredCommand("c1", 203, receivedAtMillis = now), now = now)

        minute = 8 * 60 // 次日 08:00 入窗
        val t = scheduler.evaluateTick(now + 9 * 3_600_000L)

        assertEquals(RuntimeWindowTracker.Transition.ENTER, t)
        assertEquals(PauseManager.State.RUNNING, manager.state)
        assertTrue("恢复须冲刷暂存指令", audits.any { it.type == "resume_confirmed" })
        assertTrue(notices.contains(true))
    }

    @Test
    fun `用户手动暂停-入窗不自动恢复（接管优先于时段）`() {
        manager.pause(reason = "user-takeover", actor = "app-main", now = now)
        // 出窗 tick：不覆盖用户暂停来源（只从 RUNNING 进入休眠）
        minute = 23 * 60
        scheduler.evaluateTick(now)
        assertEquals("app-main", manager.pauseSource())

        // 入窗 tick：来源非 runtime-window → 不自动恢复
        minute = 8 * 60
        scheduler.evaluateTick(now + 60_000L)
        assertEquals("用户接管不被时段自动恢复", PauseManager.State.PAUSED, manager.state)
        assertTrue(events.any { it.contains("不自动恢复") })
    }

    @Test
    fun `未启用-恒无动作（稳态 NONE）`() {
        config = RuntimeWindowConfig(enabled = false)
        minute = 23 * 60
        assertEquals(RuntimeWindowTracker.Transition.NONE, scheduler.evaluateTick(now))
        assertEquals(PauseManager.State.RUNNING, manager.state)
        assertTrue(notices.isEmpty())
    }

    @Test
    fun `稳态 tick 不重复触发（幂等）`() {
        minute = 23 * 60
        scheduler.evaluateTick(now)
        val pausedCount = audits.count { it.type == "paused" }
        assertEquals(RuntimeWindowTracker.Transition.NONE, scheduler.evaluateTick(now + 60_000L))
        assertEquals("稳态不重复 pause", pausedCount, audits.count { it.type == "paused" })
    }

    @Test
    fun `rebase 重置基线-配置变更后首 tick 不误判迁移`() {
        minute = 23 * 60
        scheduler.evaluateTick(now) // EXIT → pause
        assertEquals(PauseManager.State.PAUSED, manager.state)

        // 用户改配置（关掉运行时段）并 rebase：下一 tick 不产生 ENTER（未启用恒在窗，
        // 但 rebase 已把基线重置为 IN_WINDOW → 无迁移）；暂停不被自动解除。
        config = RuntimeWindowConfig(enabled = false)
        scheduler.rebase(now)
        assertEquals(RuntimeWindowTracker.Transition.NONE, scheduler.evaluateTick(now + 60_000L))
        assertEquals("关闭时段不自动解除既有休眠（须用户手动恢复）", PauseManager.State.PAUSED, manager.state)
    }

    @Test
    fun `currentVerdict 反映当前窗口判定`() {
        minute = 12 * 60
        assertEquals(WindowVerdict.IN_WINDOW, scheduler.currentVerdict(now))
        minute = 23 * 60
        assertEquals(WindowVerdict.OUT_OF_WINDOW, scheduler.currentVerdict(now))
    }
}
