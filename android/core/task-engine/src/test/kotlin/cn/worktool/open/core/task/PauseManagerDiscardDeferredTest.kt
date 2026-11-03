/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * U3 待执行队列清空测试（docs/16 U3 / FR-37 设备侧面；PauseManager.discardDeferred）。
 * 覆盖：FIFO 返回与清空 / 审计留痕（type/commandCount/reason/actor）/ 不改变暂停态 /
 * 空队列幂等 / 丢弃后入队恢复积累 / RUNNING 态调用无害。
 */
class PauseManagerDiscardDeferredTest {

    private var now = 1_000L
    private val audits = mutableListOf<PauseManager.AuditEvent>()
    private val manager = PauseManager(clock = { now }, audit = { audits.add(it) })

    private fun deferred(id: String, type: Int = 203, at: Long = now) =
        PauseManager.DeferredCommand(commandId = id, commandType = type, receivedAtMillis = at)

    @Test
    fun `暂停态入队后 discard-FIFO 返回并清空`() {
        manager.pause(reason = "test", actor = "tester", now = now)
        manager.enqueue(deferred("c1"), now = now)
        now += 10
        manager.enqueue(deferred("c2"), now = now)

        val discarded = manager.discardDeferred(reason = "user-clear", actor = "app-main", now = now)

        assertEquals(listOf("c1", "c2"), discarded.map { it.commandId })
        assertTrue("清空后暂存队列须为空", manager.deferred().isEmpty())
    }

    @Test
    fun `discard 全量落审计-SR12`() {
        manager.pause(now = now)
        manager.enqueue(deferred("c1"), now = now)
        manager.enqueue(deferred("c2"), now = now)
        now += 5
        manager.discardDeferred(reason = "user-clear", actor = "app-main", now = now)

        val event = audits.last()
        assertEquals("deferred_discarded", event.type)
        assertEquals(2, event.commandCount)
        assertEquals("user-clear", event.reason)
        assertEquals("app-main", event.actor)
        assertEquals(now, event.at)
    }

    @Test
    fun `discard 不改变暂停态-清空不等于恢复`() {
        manager.pause(now = now)
        manager.enqueue(deferred("c1"), now = now)
        manager.discardDeferred(now = now)
        assertEquals(PauseManager.State.PAUSED, manager.state)

        // 恢复仍须两步显式确认（FR-52 不因清空而绕过）
        manager.requestResume(now = now)
        assertEquals(PauseManager.State.RESUME_PENDING, manager.state)
        assertTrue(manager.confirmResume(now = now).isEmpty()) // 队列已清 → 冲刷为空
        assertEquals(PauseManager.State.RUNNING, manager.state)
    }

    @Test
    fun `空队列 discard 幂等无害`() {
        manager.pause(now = now)
        assertTrue(manager.discardDeferred(reason = "user-clear", now = now).isEmpty())
        assertEquals("deferred_discarded", audits.last().type)
        assertEquals(0, audits.last().commandCount)
    }

    @Test
    fun `丢弃后可继续入队积累-恢复冲刷只含新指令`() {
        manager.pause(now = now)
        manager.enqueue(deferred("c1"), now = now)
        manager.discardDeferred(reason = "user-clear", now = now)
        manager.enqueue(deferred("c2"), now = now)

        manager.requestResume(now = now)
        val flushed = manager.confirmResume(now = now)
        assertEquals(listOf("c2"), flushed.map { it.commandId }) // c1 已丢弃不复活
    }

    @Test
    fun `RUNNING 态 discard 返回空清单-无异常`() {
        assertEquals(PauseManager.State.RUNNING, manager.state)
        assertTrue(manager.discardDeferred(reason = "user-clear", now = now).isEmpty())
        assertEquals(PauseManager.State.RUNNING, manager.state)
    }
}
