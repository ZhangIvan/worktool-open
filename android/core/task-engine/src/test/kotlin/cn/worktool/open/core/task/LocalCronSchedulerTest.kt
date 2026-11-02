/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDateTime
import java.time.ZoneId

/** 本地 cron 调度器（FR-38 模式A：due/水位推进/补跑合并）。 */
class LocalCronSchedulerTest {

    private val zone = ZoneId.of("Asia/Shanghai")
    private var now = LocalDateTime.of(2026, 11, 2, 8, 0).atZone(zone).toInstant().toEpochMilli()

    private fun at(h: Int, m: Int): Long =
        LocalDateTime.of(2026, 11, 2, h, m).atZone(zone).toInstant().toEpochMilli()

    private fun job(id: String = "c1", expr: String = "0 9 * * *", enabled: Boolean = true) =
        CronJob(jobId = id, expression = expr, enabled = enabled, action = SendMessageAction(content = "早"))

    @Test
    fun `到期任务触发一次并推进水位`() {
        val s = LocalCronScheduler(listOf(job()), zone) { now }
        assertTrue(s.due(now).isEmpty()) // 08:00 未到 09:00
        now = at(9, 0)
        val due = s.due(now)
        assertEquals(1, due.size)
        assertEquals("c1", due[0].job.jobId)
        assertEquals(at(9, 0), due[0].scheduledForMillis)
        assertTrue(s.due(now).isEmpty()) // 同一时刻不重复触发
        now = at(9, 1)
        assertTrue(s.due(now).isEmpty()) // 已过 09:00，下一次在明天
    }

    @Test
    fun `停机错过多次只补一次（catch-up 合并）`() {
        val s = LocalCronScheduler(listOf(job(expr = "* * * * *")), zone) { now } // 每分钟
        now += 10 * 60_000 // 推进 10 分钟（错过 10 次）
        val due = s.due(now)
        assertEquals(1, due.size) // 只补一次
        assertTrue(s.due(now).isEmpty())
    }

    @Test
    fun `enabled=false 任务永不触发且不占用唤醒`() {
        val s = LocalCronScheduler(listOf(job(enabled = false)), zone) { now }
        now = at(9, 0)
        assertTrue(s.due(now).isEmpty())
        assertNull(s.nextWakeMillis(now))
    }

    @Test
    fun `nextWakeMillis 给出最近到期时刻（含已到期未消费水位）`() {
        val s = LocalCronScheduler(listOf(job()), zone) { now } // 08:00 → 下次 09:00
        assertEquals(at(9, 0), s.nextWakeMillis(now))
        // CR-t33-R-02：到期未消费的水位也返回（门禁挡住 due 时水位不推进，
        // 驱动环靠它区分「立即消费/短轮询」——过滤掉它会让循环自灭）
        assertEquals(at(9, 0), s.nextWakeMillis(at(9, 0)))
        s.due(at(9, 0)) // 消费 → 水位推进到明日 09:00
        assertEquals(at(9, 0) + 24 * 60 * 60_000L, s.nextWakeMillis(at(9, 0))!!)
    }

    @Test
    fun `多任务按计划时刻升序输出`() {
        val s = LocalCronScheduler(
            listOf(job(id = "late", expr = "30 9 * * *"), job(id = "early", expr = "0 9 * * *")),
            zone,
        ) { now }
        now = at(10, 0)
        val due = s.due(now)
        assertEquals(listOf("early", "late"), due.map { it.job.jobId })
    }
}
