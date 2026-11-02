/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import java.time.Instant
import java.time.LocalDate
import java.time.LocalDateTime
import java.time.ZoneId

/**
 * 标准 5 字段 cron 表达式（分 时 日 月 周；FR-38 模式A 本地 cron）。
 *
 * 支持：`*` 通配、逗号列表 `1,15`、闭区间 `1-5`、步进（`*` 加斜杠加步长，如每 15 分钟；
 * 区间亦可带步长）。周字段 0 与 7 均为周日（Vixie 约定）。分钟粒度。
 *
 * 日/周语义遵循 Vixie cron：两字段**都受限**时按 OR（任一命中）；否则 AND。
 */
class CronExpression private constructor(
    val expression: String,
    private val minutes: IntArray,
    private val hours: IntArray,
    private val daysOfMonth: IntArray,
    private val months: IntArray,
    private val daysOfWeek: IntArray,
    private val domRestricted: Boolean,
    private val dowRestricted: Boolean,
) {

    companion object {
        private val FIELD_RANGES = arrayOf(0..59, 0..23, 1..31, 1..12, 0..7)

        /** 解析并校验；非法表达式抛 [RuleConfigException]。 */
        fun parse(expression: String): CronExpression {
            val fields = expression.trim().split(Regex("\\s+"))
            if (fields.size != 5) {
                throw RuleConfigException("cron 表达式须为 5 字段（分 时 日 月 周）: '$expression'")
            }
            val parsed = fields.mapIndexed { i, f -> parseField(f, i, FIELD_RANGES[i]) }
            // 周字段 7 → 0（周日）
            val dow = parsed[4].let { if (it.contains(7)) (it.filter { v -> v != 7 } + 0).sorted().toIntArray() else it }
            return CronExpression(
                expression = expression,
                minutes = parsed[0],
                hours = parsed[1],
                daysOfMonth = parsed[2],
                months = parsed[3],
                daysOfWeek = dow,
                domRestricted = fields[2] != "*",
                dowRestricted = fields[4] != "*",
            )
        }

        private fun parseField(field: String, index: Int, range: IntRange): IntArray {
            val values = sortedSetOf<Int>()
            for (part in field.split(',')) {
                val (rangePart, step) = if (part.contains('/')) {
                    val seg = part.split('/', limit = 2)
                    seg[0] to (seg[1].toIntOrNull() ?: throw RuleConfigException(
                        "cron 第${index + 1}字段步进非法: '$part'",
                    ))
                } else {
                    part to 1
                }
                if (step <= 0) throw RuleConfigException("cron 第${index + 1}字段步进须为正: '$part'")
                val (from, to) = if (rangePart == "*") {
                    range.first to range.last
                } else if (rangePart.contains('-')) {
                    val seg = rangePart.split('-', limit = 2)
                    val a = seg[0].toIntOrNull() ?: throw RuleConfigException("cron 第${index + 1}字段区间非法: '$part'")
                    val b = seg[1].toIntOrNull() ?: throw RuleConfigException("cron 第${index + 1}字段区间非法: '$part'")
                    a to b
                } else {
                    val v = rangePart.toIntOrNull() ?: throw RuleConfigException("cron 第${index + 1}字段值非法: '$part'")
                    v to v
                }
                if (from < range.first || to > range.last || from > to) {
                    throw RuleConfigException(
                        "cron 第${index + 1}字段越界（${range.first}~${range.last}）: '$part'",
                    )
                }
                var v = from
                while (v <= to) {
                    values.add(v)
                    v += step
                }
            }
            if (values.isEmpty()) throw RuleConfigException("cron 第${index + 1}字段为空集: '$field'")
            return values.toIntArray()
        }
    }

    /**
     * `after` 之后（严格大于，分钟粒度）的下一次触发时刻（epoch 毫秒）；
     * 5 年内无命中返回 null（如 2 月 30 日）。
     */
    fun nextFire(afterEpochMillis: Long, zone: ZoneId): Long? {
        val after = Instant.ofEpochMilli(afterEpochMillis).atZone(zone)
        var date = after.toLocalDate()
        val bound = date.plusYears(5)
        while (!date.isAfter(bound)) {
            if (date.monthValue in months && dayMatches(date)) {
                for (h in hours) {
                    for (m in minutes) {
                        val candidate = date.atTime(h, m).atZone(zone)
                        if (candidate.toInstant().toEpochMilli() > afterEpochMillis) {
                            return candidate.toInstant().toEpochMilli()
                        }
                    }
                }
            }
            date = date.plusDays(1)
        }
        return null
    }

    private fun dayMatches(date: LocalDate): Boolean {
        val domOk = date.dayOfMonth in daysOfMonth
        val dowOk = (date.dayOfWeek.value % 7) in daysOfWeek // ISO Monday=1..7 → 周日 7%7=0
        return if (domRestricted && dowRestricted) domOk || dowOk else domOk && dowOk
    }
}
