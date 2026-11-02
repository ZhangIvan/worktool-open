/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** 暂停管理器（FR-52：只入队 + 显式确认恢复 + 审计留痕）。 */
class PauseManagerTest {

    private var now = 1_000L
    private val clock = { now }
    private val audits = ArrayList<PauseManager.AuditEvent>()
    private val pm = PauseManager(clock) { audits.add(it) }

    private fun deferred(id: String) = PauseManager.DeferredCommand(
        commandId = id,
        commandType = 203,
        receivedAtMillis = now,
    )

    @Test
    fun `暂停 - 状态迁移与审计留痕（SR-12）`() {
        pm.pause(reason = "用户接管", actor = "user-1")
        assertEquals(PauseManager.State.PAUSED, pm.state)
        assertTrue(pm.isPaused)
        assertEquals(listOf("paused"), audits.map { it.type })
        assertEquals("用户接管", audits[0].reason)
    }

    @Test
    fun `暂停态在线指令只入队不执行（FR-52）`() {
        pm.pause()
        pm.enqueue(deferred("cmd-1"))
        now += 10
        pm.enqueue(deferred("cmd-2"))
        assertEquals(listOf("cmd-1", "cmd-2"), pm.deferred().map { it.commandId })
        assertEquals(2, audits.count { it.type == "command_deferred" })
    }

    @Test
    fun `RUNNING 态入队是调用方误用`() {
        assertThrows(IllegalStateException::class.java) { pm.enqueue(deferred("x")) }
    }

    @Test
    fun `恢复须两步显式确认 - PAUSED 直接 confirm 被拒绝`() {
        pm.pause()
        assertThrows(PauseManager.PauseConfirmationRequired::class.java) { pm.confirmResume() }
        assertEquals(PauseManager.State.PAUSED, pm.state) // 状态未变
    }

    @Test
    fun `requestResume + confirmResume - 冲洗暂存队列并恢复运行`() {
        pm.pause()
        pm.enqueue(deferred("cmd-1"))
        now += 100
        pm.requestResume(actor = "user-1")
        assertEquals(PauseManager.State.RESUME_PENDING, pm.state)
        assertTrue(pm.isPaused) // 确认前仍处于暂停语义

        now += 50
        val flushed = pm.confirmResume(actor = "user-1")
        assertEquals(PauseManager.State.RUNNING, pm.state)
        assertEquals(listOf("cmd-1"), flushed.map { it.commandId })
        assertTrue(pm.deferred().isEmpty())
        assertEquals(
            listOf("paused", "command_deferred", "resume_requested", "resume_confirmed"),
            audits.map { it.type },
        )
    }

    @Test
    fun `RESUME_PENDING 态继续入队仍有效`() {
        pm.pause()
        pm.requestResume()
        pm.enqueue(deferred("late-cmd")) // 确认前又来指令：仍只入队
        assertEquals(listOf("late-cmd"), pm.confirmResume().map { it.commandId })
    }

    @Test
    fun `撤回恢复请求回到 PAUSED`() {
        pm.pause()
        pm.requestResume()
        pm.cancelResumeRequest()
        assertEquals(PauseManager.State.PAUSED, pm.state)
        // 撤回后再走完整两步仍可恢复
        pm.requestResume()
        pm.confirmResume()
        assertEquals(PauseManager.State.RUNNING, pm.state)
    }

    @Test
    fun `确认等待期可再次暂停（用户改主意）`() {
        pm.pause()
        pm.requestResume()
        pm.pause(reason = "再想想")
        assertEquals(PauseManager.State.PAUSED, pm.state)
    }

    @Test
    fun `重复暂停幂等`() {
        pm.pause()
        pm.pause()
        assertEquals(2, audits.count { it.type == "paused" })
        assertEquals(PauseManager.State.PAUSED, pm.state)
    }

    @Test
    fun `pauseSource - 暂停期返回最近 actor 运行期为 null（T3·1 只读暂停门消费）`() {
        assertEquals(null, pm.pauseSource())
        pm.pause(actor = "floating-window")
        assertEquals("floating-window", pm.pauseSource())
        pm.requestResume()
        assertEquals("floating-window", pm.pauseSource()) // 确认前仍是暂停语义
        pm.confirmResume()
        assertEquals(null, pm.pauseSource())
    }
}
