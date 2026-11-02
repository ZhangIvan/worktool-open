/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test
import java.time.LocalDateTime
import java.time.ZoneId

/** 5 字段 cron 表达式（FR-38 模式A 本地 cron 的时刻计算核心）。 */
class CronExpressionTest {

    private val zone = ZoneId.of("Asia/Shanghai")

    private fun millisOf(y: Int, mo: Int, d: Int, h: Int, mi: Int): Long =
        LocalDateTime.of(y, mo, d, h, mi, 0).atZone(zone).toInstant().toEpochMilli()

    @Test
    fun `每分钟通配 - 下一分钟触发`() {
        val e = CronExpression.parse("* * * * *")
        val after = millisOf(2026, 11, 2, 10, 0)
        assertEquals(millisOf(2026, 11, 2, 10, 1), e.nextFire(after, zone))
    }

    @Test
    fun `工作日早九 - 跳过周末到周一`() {
        // 2026-10-30 周五 → 下一命中 2026-10-30 09:00 之后的下一次 = 周一 2026-11-02 09:00
        val e = CronExpression.parse("0 9 * * 1-5")
        val after = millisOf(2026, 10, 30, 12, 0) // 周五中午已过 9 点
        assertEquals(millisOf(2026, 11, 2, 9, 0), e.nextFire(after, zone))
    }

    @Test
    fun `步进分钟 - 15 分钟粒度`() {
        val e = CronExpression.parse("*/15 * * * *")
        val after = millisOf(2026, 11, 2, 10, 7)
        assertEquals(millisOf(2026, 11, 2, 10, 15), e.nextFire(after, zone))
    }

    @Test
    fun `列表与区间混合`() {
        val e = CronExpression.parse("5,35 8-10 * * *")
        val after = millisOf(2026, 11, 2, 9, 6)
        assertEquals(millisOf(2026, 11, 2, 9, 35), e.nextFire(after, zone))
    }

    @Test
    fun `周日 0 与 7 等价`() {
        val e0 = CronExpression.parse("30 8 * * 0")
        val e7 = CronExpression.parse("30 8 * * 7")
        val after = millisOf(2026, 11, 2, 0, 0) // 2026-11-02 周一
        assertEquals(e0.nextFire(after, zone), e7.nextFire(after, zone))
        assertEquals(millisOf(2026, 11, 8, 8, 30), e0.nextFire(after, zone)) // 当周日
    }

    @Test
    fun `日与周同时受限 - Vixie OR 语义`() {
        // 每月 1 号 或 每周一 8:30
        val e = CronExpression.parse("30 8 1 * 1")
        val after = millisOf(2026, 11, 2, 0, 0) // 周一（非 1 号）
        assertEquals(millisOf(2026, 11, 2, 8, 30), e.nextFire(after, zone)) // 周一命中（OR）
    }

    @Test
    fun `永不命中的日期返回 null`() {
        val e = CronExpression.parse("0 0 31 2 *") // 2 月 31 日不存在
        assertNull(e.nextFire(millisOf(2026, 11, 2, 0, 0), zone))
    }

    @Test
    fun `非法表达式解析失败`() {
        assertThrows(RuleConfigException::class.java) { CronExpression.parse("* * * *") }
        assertThrows(RuleConfigException::class.java) { CronExpression.parse("60 * * * *") }
        assertThrows(RuleConfigException::class.java) { CronExpression.parse("a * * * *") }
        assertThrows(RuleConfigException::class.java) { CronExpression.parse("*/0 * * * *") }
        assertNotNull(CronExpression.parse("0 9 * * 1-5"))
    }
}
