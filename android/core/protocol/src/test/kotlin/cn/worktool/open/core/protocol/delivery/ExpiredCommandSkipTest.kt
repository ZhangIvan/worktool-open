/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.delivery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * U4 跳过过期指令测试（docs/16 U4，TTL 积压门；纯 JVM）。
 * 覆盖：过期 → Expired 不下发副作用 / 新鲜 → Applied 执行 / TTL 未启用 → 不拦截 /
 * 无时间戳 → 不拦截 / 重复投递过期指令 → AlreadyApplied（防回放复活）/ 边界
 * age==TTL 不算过期 / onExpired 回调携带 age。
 */
class ExpiredCommandSkipTest {

    private var now = 1_793_664_000_000L
    private val effects = mutableListOf<String>()
    private val expired = mutableListOf<Pair<String, Long>>()

    private val store = InMemoryJournalStore()

    private fun gate(ttl: Long?) = InboundCommandGate(
        store = store,
        effect = { effects.add(it.commandId) },
        clock = { now },
        expiredTtlMillis = { ttl },
        onExpired = { c, age -> expired.add(c.commandId to age) },
    )

    private fun command(id: String, receivedAt: Long, epoch: Long = 1) =
        InboundCommandGate.InboundCommand(id, 203, epoch, receivedAt)

    @Test
    fun `过期指令-Expired 不下发副作用`() {
        val g = gate(ttl = 2 * 3_600_000L)
        val decision = g.apply(command("c1", receivedAt = now - 3 * 3_600_000L))
        assertTrue(decision is InboundCommandGate.Decision.Expired)
        assertTrue("副作用不得执行", effects.isEmpty())
        assertEquals("c1", expired.single().first)
    }

    @Test
    fun `新鲜指令-Applied 正常执行`() {
        val g = gate(ttl = 2 * 3_600_000L)
        val decision = g.apply(command("c2", receivedAt = now - 60_000L))
        assertEquals(InboundCommandGate.Decision.Applied, decision)
        assertEquals(listOf("c2"), effects)
        assertTrue(expired.isEmpty())
    }

    @Test
    fun `TTL 未启用-过期指令照常执行（历史行为）`() {
        val g = gate(ttl = null)
        val decision = g.apply(command("c3", receivedAt = now - 24 * 3_600_000L))
        assertEquals(InboundCommandGate.Decision.Applied, decision)
        assertEquals(listOf("c3"), effects)
    }

    @Test
    fun `无接收时间戳-不拦截（不可判定 fail-open 至旧行为）`() {
        val g = gate(ttl = 2 * 3_600_000L)
        val decision = g.apply(command("c4", receivedAt = -1L))
        assertEquals(InboundCommandGate.Decision.Applied, decision)
        assertEquals(listOf("c4"), effects)
    }

    @Test
    fun `重复投递过期指令-AlreadyApplied 幂等（防回放旧指令复活）`() {
        val g = gate(ttl = 2 * 3_600_000L)
        g.apply(command("c5", receivedAt = now - 3 * 3_600_000L))
        val again = g.apply(command("c5", receivedAt = now - 3 * 3_600_000L))
        assertTrue("第二次须走 journal 去重", again is InboundCommandGate.Decision.AlreadyApplied)
        assertTrue(expired.size == 1) // 过期回调只首次
    }

    @Test
    fun `边界-age 恰等于 TTL 不算过期（超过才跳过）`() {
        val g = gate(ttl = 2 * 3_600_000L)
        val decision = g.apply(command("c6", receivedAt = now - 2 * 3_600_000L))
        assertEquals(InboundCommandGate.Decision.Applied, decision)
        assertEquals(listOf("c6"), effects)
    }

    @Test
    fun `Expired 携带 age 与 ttl 值（回执与日志数据源）`() {
        val g = gate(ttl = 7_200_000L)
        val decision = g.apply(command("c7", receivedAt = now - 7_200_001L))
        decision as InboundCommandGate.Decision.Expired
        assertEquals(7_200_001L, decision.ageMillis)
        assertEquals(7_200_000L, decision.ttlMillis)
        assertEquals(7_200_001L, expired.single().second)
    }

    @Test
    fun `epoch 拒旧优先于 TTL（更旧命令不进 TTL 判定）`() {
        store.setAppliedEpoch(5)
        val g = gate(ttl = 2 * 3_600_000L)
        val decision = g.apply(command("c8", receivedAt = now - 9 * 3_600_000L, epoch = 3))
        assertTrue(decision is InboundCommandGate.Decision.RejectedOld)
        assertTrue(effects.isEmpty())
        assertTrue(expired.isEmpty())
    }
}
