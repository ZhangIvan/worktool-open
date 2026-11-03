/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-49 运行日志上报管道——纯 JVM 测试（脱敏/批量/失败重试）。
 *
 * 契约锚点：301=常规 302=错误（parity/b3/301.md）；SR-06 默认脱敏不可绕过；
 * 可整体关闭；批量=一条 sink 调用携带 N bean（MessageReportSink 模式复用）。
 */
class LogReportPipelineTest {

    /** Fake 采样源：返回预置增量行。 */
    private class FakeSource(var lines: List<String> = emptyList()) : LogLineSource {
        var sampled = 0
        override fun sampleLines(): List<String> {
            sampled++
            return lines
        }
    }

    /** Fake 上报出口：留痕每批 bean，成败可编程（队列按调用次弹出，缺省=成功）。 */
    private class FakeSink(var deliverable: ArrayDeque<Boolean> = ArrayDeque(listOf(true))) :
        LogReportSink {
        val batches = ArrayList<List<cn.worktool.open.core.protocol.WeworkMessageBean>>()
        override fun sendReport(beans: List<cn.worktool.open.core.protocol.WeworkMessageBean>): Boolean {
            batches.add(beans)
            return if (deliverable.isEmpty()) true else deliverable.removeFirst()
        }
    }

    private val source = FakeSource()
    private val sink = FakeSink()
    private val logs = ArrayList<String>()

    private fun pipeline(
        enabled: Boolean = true,
        batchSize: Int = LogReportPipeline.DEFAULT_BATCH_SIZE,
        maxPending: Int = LogReportPipeline.DEFAULT_MAX_PENDING,
    ) = LogReportPipeline(
        sink = sink,
        source = source,
        enabledProvider = { enabled },
        batchSize = batchSize,
        maxPending = maxPending,
        log = { logs.add(it) },
    )

    @Test
    fun `SR-06 脱敏 - 手机号数字串hex token 敏感词全遮蔽`() {
        val p = pipeline()
        val raw = "11-03 10:00:00.000 I/WT( 123): user 13812345678 id 0012345678 auth aabbccdd00112233aabbccdd00112233e5 password=hunter2"
        val line = p.parseLine(raw)
        // 管道出站必经脱敏：collect→flush 后检查 sink 实收 extraText
        source.lines = listOf(raw)
        p.collect()
        p.flush()
        val out = sink.batches.single().single()
        assertEquals(MessageTypes.ROBOT_LOG, out.type)
        val text = out.extraText!!
        assertFalse(text.contains("13812345678")) // 手机号
        assertFalse(text.contains("aabbccdd00112233aabbccdd00112233e5")) // hex token
        assertFalse(text.contains("0012345678")) // 数字长串
        assertFalse(text.lowercase().contains("password")) // 词表
        assertFalse(text.contains("hunter2"))
        assertTrue(text.contains("***")) // 词表命中遮蔽
        // 解析面同样已脱敏（sanitizeLine 在入队前执行）
        assertFalse(line.message.contains("13812345678"))
        // 座机/证件片段兜底
        assertFalse(LogRedactor.sanitize("座机 057188123456").contains("057188123456"))
    }

    @Test
    fun `批量 - 攒满阈值一次 sink 调用携带 N bean 且 301 与 302 分流`() {
        val p = pipeline(batchSize = 3)
        source.lines = listOf(
            "11-03 10:00:00.000 I/WT( 1): ok line",
            "11-03 10:00:01.000 E/WT( 1): crash detail",
            "11-03 10:00:02.000 W/WT( 1): warn line",
        )
        assertEquals(3, p.collect()) // 满 3 自动 flush
        assertEquals(1, sink.batches.size) // 单次批量调用
        val batch = sink.batches[0]
        assertEquals(3, batch.size)
        assertEquals(MessageTypes.ROBOT_LOG, batch[0].type) // 301
        assertEquals(MessageTypes.ROBOT_ERROR_LOG, batch[1].type) // 302（ERROR 级行）
        assertEquals(MessageTypes.ROBOT_LOG, batch[2].type)
        assertTrue(batch[1].extraText!!.startsWith("[ERROR/WT]"))
        assertEquals(0, p.bufferedCount())
    }

    @Test
    fun `失败重试 - sink 不可投递入暂存且 flushPending 保序重投`() {
        val p = pipeline(batchSize = 10)
        source.lines = (1..3).map { "11-03 10:00:0$it.000 I/WT( 1): msg$it" }
        sink.deliverable = ArrayDeque(listOf(false))
        p.collect()
        assertFalse(p.flush()) // 不可投递
        assertEquals(3, p.pendingCount())

        sink.deliverable = ArrayDeque(listOf(true))
        assertEquals(3, p.flushPending())
        assertEquals(0, p.pendingCount())
        // 保序：逐条重投，msg1..msg3 顺序不变
        val retried = sink.batches.drop(1).map { it[0].extraText!! }
        assertEquals(3, retried.size)
        assertTrue(retried[0].contains("msg1") && retried[2].contains("msg3"))
    }

    @Test
    fun `可整体关闭 - 关闭态零采样零发送零重试`() {
        val p = pipeline(enabled = false)
        source.lines = listOf("11-03 10:00:00.000 E/WT( 1): should not be sampled")
        assertEquals(0, p.collect())
        assertEquals(0, source.sampled) // 不采样
        assertEquals(0, p.flushPending())
        assertTrue(sink.batches.isEmpty())
    }

    @Test
    fun `logcat 行解析 - time 与 brief 格式及畸形行兜底`() {
        val p = pipeline()
        val timeStyle = p.parseLine("11-03 10:00:00.000 E/WT-Crash( 42): boom")
        assertEquals(LogLevel.ERROR, timeStyle.level)
        assertEquals("WT-Crash", timeStyle.tag)
        assertEquals("boom", timeStyle.message)
        val briefStyle = p.parseLine("D/WT( 7): detail msg")
        assertEquals(LogLevel.DEBUG, briefStyle.level)
        val malformed = p.parseLine("not a logcat line at all")
        assertEquals(LogLevel.INFO, malformed.level)
        assertEquals("raw", malformed.tag)
        assertEquals("not a logcat line at all", malformed.message)
    }

    @Test
    fun `暂存上限 - FIFO 淘汰最旧且留痕`() {
        val p = pipeline(batchSize = 10, maxPending = 2)
        source.lines = (1..5).map { "11-03 10:00:0$it.000 I/WT( 1): m$it" }
        sink.deliverable = ArrayDeque(listOf(false))
        p.collect()
        assertFalse(p.flush())
        assertEquals(2, p.pendingCount()) // 5 → 上限 2，最旧 3 条被淘汰
        val kept = p.pendingSnapshotForTest().map { p.toBean(it).extraText!! }
        assertTrue(kept[0].contains("m4")) // 队首=次新（保序）
        assertTrue(kept[1].contains("m5")) // 队尾=最新
        assertTrue(logs.any { it.contains("evict") }) // 淘汰留痕
    }

    /** 测试辅助：取暂存快照（pending 为私有，反射直读给本测试断言队列序）。 */
    private fun LogReportPipeline.pendingSnapshotForTest(): List<cn.worktool.open.core.task.LogLine> {
        val f = LogReportPipeline::class.java.getDeclaredField("pending")
        f.isAccessible = true
        @Suppress("UNCHECKED_CAST")
        return (f.get(this) as ArrayDeque<cn.worktool.open.core.task.LogLine>).toList()
    }
}
