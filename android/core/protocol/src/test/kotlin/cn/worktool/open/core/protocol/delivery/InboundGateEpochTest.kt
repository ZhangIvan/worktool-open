/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.delivery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 会话21 回归（2026-11-04 深查）：epoch 缺失（=0）不得拒旧。
 *
 * 契约（DeviceSessionLoop.epochOf 注释 / docs/02 §5.1 语义）：dispatch 载荷缺 epoch
 * 按 0 =「不拒旧仅去重」；服务端按 spec §5.7.2 注入 epoch（int ≥1），0 不会是合法
 * 分配值（AllocateEpochs 自 1 起）。实现此前 `epoch < watermark` 一刀切 → 回执帧
 * 重写/无 epoch 载荷全被拒旧（真机实锤：`epoch=0 < 水位 1002 → 拒旧`）。
 */
class InboundGateEpochTest {

    private fun gate(store: InMemoryJournalStore, executed: MutableList<String> = mutableListOf()) =
        InboundCommandGate(
            store = store,
            effect = { executed.add(it.commandId) },
        )

    @Test
    fun `缺 epoch-0-不拒旧仅去重-高水位下照常执行`() {
        val store = InMemoryJournalStore().apply { setAppliedEpoch(1002) }
        val executed = mutableListOf<String>()
        val decision = gate(store, executed).apply(
            InboundCommandGate.InboundCommand("11111111-1111-4111-8111-111111111111", 502, epoch = 0),
        )
        assertEquals("缺 epoch（0）不得拒旧（契约：不拒旧仅去重）", InboundCommandGate.Decision.Applied, decision)
        assertEquals(listOf("11111111-1111-4111-8111-111111111111"), executed)
    }

    @Test
    fun `缺 epoch-0-不推进拒旧水位`() {
        val store = InMemoryJournalStore().apply { setAppliedEpoch(1002) }
        gate(store).apply(
            InboundCommandGate.InboundCommand("11111111-2222-4222-8222-222222222222", 502, epoch = 0),
        )
        assertEquals("0 不得回退/扰动水位", 1002L, store.appliedEpoch())
    }

    @Test
    fun `缺 epoch-0-重复 commandId 仍去重（AlreadyApplied）`() {
        val store = InMemoryJournalStore().apply { setAppliedEpoch(1002) }
        val g = gate(store)
        val id = "11111111-3333-4333-8333-333333333333"
        assertEquals(InboundCommandGate.Decision.Applied, g.apply(InboundCommandGate.InboundCommand(id, 502, epoch = 0)))
        assertEquals(InboundCommandGate.Decision.AlreadyApplied, g.apply(InboundCommandGate.InboundCommand(id, 502, epoch = 0)))
    }

    @Test
    fun `显式旧 epoch-1-高水位 1002-仍拒旧（防回退语义不变）`() {
        val store = InMemoryJournalStore().apply { setAppliedEpoch(1002) }
        val executed = mutableListOf<String>()
        val decision = gate(store, executed).apply(
            InboundCommandGate.InboundCommand("11111111-4444-4444-8444-444444444444", 203, epoch = 1),
        )
        assertTrue("合法分配值（≥1）低于水位仍须拒旧", decision is InboundCommandGate.Decision.RejectedOld)
        assertTrue(executed.isEmpty())
    }

    @Test
    fun `新鲜 epoch-推进水位（既有行为保持）`() {
        val store = InMemoryJournalStore().apply { setAppliedEpoch(1002) }
        gate(store).apply(
            InboundCommandGate.InboundCommand("11111111-5555-4555-8555-555555555555", 502, epoch = 1003),
        )
        assertEquals(1003L, store.appliedEpoch())
    }
}
