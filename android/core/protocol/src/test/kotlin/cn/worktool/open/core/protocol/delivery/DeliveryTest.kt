/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.delivery

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * AtLeastOnce 队列 + journal 语义测试（任务卡 T2.2 ④：先落盘后副作用）：
 * 持久先于发送、发送成功即 DONE（docs/16 H1，ack 仍兼容）、崩溃恢复只重放未完成
 * 条目、入向门禁（E09 guard）与 epoch 拒旧。
 */
class DeliveryTest {

    private class FakeSink(var available: Boolean = true) : OutboundSink {
        val sent = mutableListOf<String>()
        override fun send(frameText: String): Boolean {
            if (!available) return false
            sent.add(frameText)
            return true
        }
    }

    @Test
    fun `先落盘后副作用-发送抛异常条目仍在 journal`() {
        val store = InMemoryJournalStore()
        val sink = object : OutboundSink {
            override fun send(frameText: String): Boolean = throw java.io.IOException("断线")
        }
        val queue = AtLeastOnceQueue(store, sink)
        val entry = queue.enqueue("evt-1", """{"a":1}""", now = 100L)
        assertEquals(JournalState.INFLIGHT, entry.state) // 尝试过投递（副作用已发生）
        assertEquals(1, entry.attempts)
        assertEquals(1, store.entries().size) // 且条目已持久（不丢）
    }

    @Test
    fun `未连接时入队-重放补发`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink(available = false)
        val queue = AtLeastOnceQueue(store, sink)
        queue.enqueue("evt-1", "f1", now = 100L)
        queue.enqueue("evt-2", "f2", now = 101L)
        assertEquals(2, queue.pendingCount())
        assertTrue(sink.sent.isEmpty())

        sink.available = true
        queue.flushPending()
        assertEquals(listOf("f1", "f2"), sink.sent)
    }

    @Test
    fun `ack 后条目完成不再重放`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink()
        val queue = AtLeastOnceQueue(store, sink)
        queue.enqueue("evt-1", "f1", now = 100L)
        queue.onAck("evt-1")
        assertEquals(0, queue.pendingCount())
        sink.sent.clear()
        queue.flushPending()
        assertTrue(sink.sent.isEmpty())
    }

    @Test
    fun `崩溃恢复-journal 快照重放未完成条目（同 eventId 幂等）`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink(available = false)
        val queue = AtLeastOnceQueue(store, sink)
        queue.enqueue("evt-1", "f1", now = 100L) // 断线期发送失败 → 未完成，待重放
        sink.available = true
        queue.enqueue("evt-2", "f2", now = 101L) // 发送成功即 DONE（H1）→ 崩溃恢复不重放

        // 模拟崩溃：内存对象全丢，仅 journal 快照（= SQLite 落盘内容）幸存
        val recovered = store.snapshot()
        val sink2 = FakeSink()
        val queue2 = AtLeastOnceQueue(recovered, sink2)
        assertEquals(1, queue2.pendingCount())
        queue2.flushPending()
        assertEquals(listOf("f1"), sink2.sent) // 只重发未完成的 evt-1
    }

    @Test
    fun `发送成功即 DONE-不再重放-超窗清理保留最近 N 条`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink()
        val queue = AtLeastOnceQueue(store, sink, doneRetention = 32)

        // H1：发送成功（send=true）即标 DONE——journal 不再依赖 ack 完成路径
        val entry = queue.enqueue("evt-1", "f1", now = 100L)
        assertEquals(JournalState.DONE, entry.state)
        assertEquals(0, queue.pendingCount())

        // 重连 flushPending 不重放已发送条目（重放本就幂等，服务端 eventId 去重兜底）
        sink.sent.clear()
        queue.flushPending()
        assertTrue(sink.sent.isEmpty())

        // 超过保留窗口的 DONE 条目在下次 enqueue 时清理，保留最近 32 条（防回退窗口）
        repeat(40) { i -> queue.enqueue("evt-bulk-$i", "fb-$i", now = 1_000L + i) }
        val doneIds = store.entries().filter { it.state == JournalState.DONE }.map { it.eventId }
        assertEquals(32, doneIds.size)
        assertTrue("最旧 DONE 须被清理", doneIds.none { it == "evt-1" })
        assertEquals("evt-bulk-39", doneIds.last()) // 保留的是最近条目

        // 清理只删 DONE：不产生任何重放帧
        sink.sent.clear()
        queue.flushPending()
        assertTrue(sink.sent.isEmpty())
        assertEquals(0, queue.pendingCount())
    }

    @Test
    fun `flushPending 注入 refresh 时重放帧按同 eventId 重建-首投不重建`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink()
        val queue = AtLeastOnceQueue(store, sink) { eventId, text -> "$text#rebuilt:$eventId" }

        // 首投（enqueue 直发）：帧刚构造即新鲜，不重建（重建只会白耗帧序）
        queue.enqueue("evt-1", "f1", now = 100L)
        assertEquals(listOf("f1"), sink.sent)

        // 断线期入队：发送失败 → 条目保持待发（H1：发送成功即 DONE，只有失败条目进重放）
        sink.available = false
        queue.enqueue("evt-2", "f2", now = 101L)
        sink.available = true

        // 重放（重连/崩溃恢复）：按同 eventId 重建信封（新 nonce/ts/seq + 重算 HMAC
        // 由注入方实现；冻结入站规则下原样重发必 4403/4402/4405，CR T34-01）
        // 已 DONE 的 evt-1 不重放，只重放待发的 evt-2
        sink.sent.clear()
        queue.flushPending()
        assertEquals(listOf("f2#rebuilt:evt-2"), sink.sent)

        // 无 refresh 钩子 = 原样重放（既有语义保持）
        val store2 = InMemoryJournalStore()
        val sink2 = FakeSink(available = false)
        AtLeastOnceQueue(store2, sink2).enqueue("evt-2", "f2", now = 100L) // 断线：保持待发
        sink2.available = true
        store2.entries().forEach { store2.update(it.eventId, JournalState.PENDING, it.attempts) }
        AtLeastOnceQueue(store2, sink2).flushPending()
        assertEquals(listOf("f2"), sink2.sent)
    }

    @Test
    fun `入向门禁-去重先于副作用-同 commandId 不双执行`() {
        val store = InMemoryJournalStore()
        val executed = mutableListOf<String>()
        val gate = InboundCommandGate(store) { executed.add(it.commandId) }

        val c1 = InboundCommandGate.InboundCommand("cmd-1", type = 203, epoch = 10)
        assertEquals(InboundCommandGate.Decision.Applied, gate.apply(c1))
        assertEquals(listOf("cmd-1"), executed)

        // 重复下发同 commandId（重连补发）→ AlreadyApplied，不重复副作用
        assertEquals(InboundCommandGate.Decision.AlreadyApplied, gate.apply(c1))
        assertEquals(listOf("cmd-1"), executed)
    }

    @Test
    fun `入向门禁-epoch 拒旧`() {
        val store = InMemoryJournalStore()
        val gate = InboundCommandGate(store) { }
        assertEquals(InboundCommandGate.Decision.Applied, gate.apply(InboundCommandGate.InboundCommand("cmd-1", 203, epoch = 10)))
        val rejected = gate.apply(InboundCommandGate.InboundCommand("cmd-2", 203, epoch = 9))
        assertTrue(rejected is InboundCommandGate.Decision.RejectedOld)
        assertEquals(10L, (rejected as InboundCommandGate.Decision.RejectedOld).appliedEpoch)
    }

    @Test
    fun `journal appliedEpoch 水位推进并供 hello 上报`() {
        val store = InMemoryJournalStore()
        val gate = InboundCommandGate(store) { }
        gate.apply(InboundCommandGate.InboundCommand("cmd-1", 203, epoch = 10))
        gate.apply(InboundCommandGate.InboundCommand("cmd-2", 203, epoch = 12))
        assertEquals(12L, store.appliedEpoch())
    }

    @Test
    fun `队列与存储边界分支`() {
        val store = InMemoryJournalStore()
        val sink = FakeSink()
        val queue = AtLeastOnceQueue(store, sink)

        // onAck 未知 eventId：无副作用
        queue.onAck("nope")
        assertTrue(store.entries().isEmpty())

        // append 同 eventId 幂等（保留原状态）
        queue.enqueue("e1", "f1", now = 1L)
        store.append(cn.worktool.open.core.protocol.delivery.JournalEntry("e1", "f1x", 2L))
        assertEquals("f1", store.entries().single { it.eventId == "e1" }.frameText)

        // update 未知 eventId：忽略
        store.update("missing", JournalState.DONE, 9)
        assertTrue(store.entries().none { it.eventId == "missing" })

        // flushPending 空队列 no-op
        sink.sent.clear()
        queue.onAck("e1")
        queue.flushPending()
        assertTrue(sink.sent.isEmpty())
    }

    @Test
    fun `入向门禁 epoch 等于水位时正常执行且水位不变`() {
        val store = InMemoryJournalStore()
        val executed = mutableListOf<String>()
        val gate = InboundCommandGate(store) { executed.add(it.commandId) }
        gate.apply(InboundCommandGate.InboundCommand("cmd-1", 203, epoch = 10))
        assertEquals(InboundCommandGate.Decision.Applied, gate.apply(InboundCommandGate.InboundCommand("cmd-2", 203, epoch = 10)))
        assertEquals(10L, store.appliedEpoch())
        assertEquals(listOf("cmd-1", "cmd-2"), executed)
    }
}
