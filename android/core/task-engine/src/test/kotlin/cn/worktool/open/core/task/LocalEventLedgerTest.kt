/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 本地事件台账与 (ruleId,eventHash) 去重（docs/02 §5.2 / spec §5.4.5）。 */
class LocalEventLedgerTest {

    private fun event(ruleId: String = "kr-1", hash: String = "h1", at: Long = 100L) =
        LocalExecEvent(
            ruleId = ruleId,
            eventHash = hash,
            commandType = MessageTypes.SEND_MESSAGE,
            targetRoom = "会话A",
            content = "回复",
            executedAtMillis = at,
        )

    @Test
    fun `同 ruleId 同 eventHash 重复记账被拒绝（端侧幂等）`() {
        val ledger = LocalEventLedger(InMemoryLocalEventStore())
        assertTrue(ledger.recordExecuted(event()))
        assertFalse(ledger.recordExecuted(event())) // 重复观测
        assertEquals(1, ledger.all().size)
    }

    @Test
    fun `不同 ruleId 或不同 hash 均为独立事件`() {
        val ledger = LocalEventLedger(InMemoryLocalEventStore())
        assertTrue(ledger.recordExecuted(event(ruleId = "kr-1", hash = "h1")))
        assertTrue(ledger.recordExecuted(event(ruleId = "kr-2", hash = "h1"))) // 同 hash 不同规则
        assertTrue(ledger.recordExecuted(event(ruleId = "kr-1", hash = "h2"))) // 同规则不同事件
        assertEquals(3, ledger.all().size)
    }

    @Test
    fun `pendingReports 只含未上报 - 上报成功后标记清空`() {
        val ledger = LocalEventLedger(InMemoryLocalEventStore())
        ledger.recordExecuted(event(ruleId = "kr-1", hash = "h1", at = 100))
        ledger.recordExecuted(event(ruleId = "kr-2", hash = "h2", at = 50))
        assertEquals(listOf("kr-2", "kr-1"), ledger.pendingReports().map { it.ruleId }) // 按时间升序

        ledger.markReported(ledger.pendingReports().first())
        assertEquals(listOf("kr-1"), ledger.pendingReports().map { it.ruleId })
    }

    @Test
    fun `eventHash - 同观测稳定，不同发生互异`() {
        val h1 = EventHashes.compute("kr-1", 203, "会话A", listOf("张三"), "价格", 1_000L)
        assertEquals(h1, EventHashes.compute("kr-1", 203, "会话A", listOf("张三"), "价格", 1_000L))
        assertNotEquals(h1, EventHashes.compute("kr-1", 203, "会话A", listOf("张三"), "价格", 2_000L)) // 不同发生
        assertNotEquals(h1, EventHashes.compute("kr-2", 203, "会话A", listOf("张三"), "价格", 1_000L)) // 不同规则
        assertEquals(64, h1.length) // SHA-256 hex
        assertTrue(h1.all { it in '0'..'9' || it in 'a'..'f' }) // 小写 hex
    }

    @Test
    fun `canonical 串不用常见字符做分隔 - 名单含竖线不产生碰撞`() {
        // senderNames 以 U+001F 拼接：["a","b"] 与单元素 "a|b"（竖线常见于昵称/文本）不同
        val c1 = EventHashes.canonicalOf("r", 1, null, listOf("a", "b"), "", 0)
        val c2 = EventHashes.canonicalOf("r", 1, null, listOf("a|b"), "", 0)
        assertNotEquals(c1, c2)
    }

    @Test
    fun `sha256Hex 金标准 - 空串常量对齐 spec §5·2·2`() {
        // spec：空载荷 SHA-256 = e3b0c442...b855（protocol/fixtures/hmac-vector.json 同源常量）
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            EventHashes.sha256Hex(""),
        )
    }
}
