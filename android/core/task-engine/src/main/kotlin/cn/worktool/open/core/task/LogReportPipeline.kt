/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.WeworkMessageBean

/**
 * FR-49 运行日志上报管道（docs/01 FR-49 行「默认脱敏、可关闭」；契约
 * local-private/parity/b3/301.md：type=301 常规日志 / 302 崩溃错误日志，载荷=自由文本）。
 *
 * 管道：logcat 采样（[LogLineSource]）→ SR-06 脱敏（[LogRedactor] 敏感词表+结构遮蔽，
 * 强制不可绕过）→ 批量上报（[LogReportSink]，复用 feature:executor MessageReportSink
 * 模式：fun interface + Boolean 语义 + 内存暂存兜底重试）。
 *
 * - 301/302 分流：ERROR 级行 → 302（ROBOT_ERROR_LOG），其余 → 301（ROBOT_LOG，
 *   MessageTypes 常量）；
 * - **批量**：缓冲满 [batchSize] 条自动 flush（一条 sink 调用携带 N 个 bean）；
 * - **失败重试**：sink 返回 false → 内存暂存（上限 [maxPending]，FIFO 淘汰），
 *   `flushPending()` 保序重投——**持久化**暂存归协议层 AtLeastOnce journal（端口实现方，
 *   MessageReportSink 同契约）；
 * - **可关闭**：enabledProvider=false 时不采样不发送不重试（FR-40「日志上报默认脱敏、
 *   可整体关闭」；默认开，app 装配层接设置开关）；
 * - SR-06（docs/01 SR-06 行）：日志面**默认不落**消息正文/手机号/token——本管道出站
 *   与日志行全部经 [LogRedactor.sanitize]（词表+手机号+数字长串+hex token 遮蔽）。
 */
class LogReportPipeline(
    private val sink: LogReportSink,
    private val source: LogLineSource,
    /** 总开关（默认开；「可关闭」由 app 设置注入）。 */
    private val enabledProvider: () -> Boolean = { true },
    /** 批量阈值：缓冲满即自动 flush。 */
    private val batchSize: Int = DEFAULT_BATCH_SIZE,
    /** 内存暂存上限（sink 不可投递期；超出 FIFO 淘汰最旧并记日志）。 */
    private val maxPending: Int = DEFAULT_MAX_PENDING,
    /** 单行出站截断长度（301 载荷为全文不截断口径的例外收敛——防超长行撑爆 WS 帧）。 */
    private val maxLineLength: Int = DEFAULT_MAX_LINE_LENGTH,
    private val clock: () -> Long = { 0L },
    private val log: (String) -> Unit = {},
) {

    /** 待批量缓冲（已脱敏）。 */
    private val buffered = ArrayDeque<LogLine>()

    /** 不可投递暂存（FIFO，保序重试）。 */
    private val pending = ArrayDeque<LogLine>()

    /** logcat 采样入口：拉增量行 → 解析（边界即脱敏）→ 入批（满批自动 flush）。 */
    fun collect(): Int {
        if (!enabledProvider()) return 0
        val lines = source.sampleLines()
        for (raw in lines) {
            buffered.addLast(parseLine(raw))
        }
        if (buffered.size >= batchSize) flush()
        return lines.size
    }

    /**
     * 批量投递：缓冲全量以**一条** sink 调用发出（301/302 混编，bean.type 分流）。
     * false = 本轮不可投递 → 全部入暂存。返回 true = 全部送达。
     */
    fun flush(): Boolean {
        if (!enabledProvider() || buffered.isEmpty()) return buffered.isEmpty()
        val batch = List(buffered.size) { buffered.removeFirst() }
        if (sink.sendReport(batch.map { toBean(it) })) {
            log("log-report ok batch=${batch.size}")
            return true
        }
        for (line in batch) {
            pending.addLast(line)
            while (pending.size > maxPending) {
                val evicted = pending.removeFirst()
                log("log-report-pending-evict ${evicted.tag}")
            }
        }
        log("log-report pending batch=${batch.size} pending=${pending.size}")
        return false
    }

    /** 暂存重投（连接恢复后调用）：保序逐条批投，遇不可投递即停。返回本轮送达条数。 */
    fun flushPending(): Int {
        if (!enabledProvider()) return 0
        var delivered = 0
        while (pending.isNotEmpty()) {
            if (!sink.sendReport(listOf(toBean(pending.first())))) break
            pending.removeFirst()
            delivered++
        }
        if (delivered > 0) log("log-report-flush-ok delivered=$delivered")
        return delivered
    }

    fun bufferedCount(): Int = buffered.size

    fun pendingCount(): Int = pending.size

    /**
     * logcat 行解析（宽松）：兼容 `-v time`（`11-03 10:00:00.000 D/Tag( 123): msg`）与
     * `-v brief`（`D/Tag( 123): msg`）；不匹配整行归 INFO/tag=raw（采样不丢行）。
     * **出站即脱敏**：SR-06 在解析边界强制执行（任何调用方拿不到未脱敏文本）。
     */
    fun parseLine(raw: String): LogLine {
        val trimmed = raw.trim()
        val parts = LOGLINE.matchEntire(trimmed)?.destructured?.let {
            listOf(it.component1(), it.component2(), it.component3())
        } ?: listOf("I", "raw", trimmed)
        val sanitized = LogRedactor.sanitize(parts[2]).let {
            if (it.length > maxLineLength) it.take(maxLineLength) + "…" else it
        }
        return LogLine(
            level = when (parts[0]) {
                "W" -> LogLevel.WARN
                "E" -> LogLevel.ERROR
                "D" -> LogLevel.DEBUG
                "V" -> LogLevel.VERBOSE
                else -> LogLevel.INFO
            },
            tag = parts[1].trim(),
            message = sanitized,
        )
    }

    /** 脱敏行 → 出站 bean（301/302 分流；载荷入 extraText，spec §4.1 端→云日志通道）。 */
    internal fun toBean(line: LogLine): WeworkMessageBean = WeworkMessageBean(
        type = if (line.level == LogLevel.ERROR) ROBOT_ERROR_LOG else ROBOT_LOG,
        extraText = "[${line.level.name}/${line.tag}] ${line.message}",
    )

    companion object {
        const val DEFAULT_BATCH_SIZE: Int = 10
        const val DEFAULT_MAX_PENDING: Int = 200
        const val DEFAULT_MAX_LINE_LENGTH: Int = 1000

        /** MessageTypes.ROBOT_LOG / ROBOT_ERROR_LOG（301/302，自持常量免引协议类名噪音）。 */
        const val ROBOT_LOG: Int = 301
        const val ROBOT_ERROR_LOG: Int = 302

        private val LOGLINE = Regex("""(?:\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\.\d{3}\s+)?([VDIWEF])/([^(]+)\(\s*\d+\):\s*(.*)""")
    }
}

/**
 * logcat 采样源端口（FR-49 接缝——JVM 测试注入 Fake 行序列）。
 * 生产实现（app 装配层 LogcatLineSampler，本车道交付）：`logcat -d`（dump 后 `-c` 清）
 * 增量采样；服务端进程 logcat 权限由 app 进程天然具备（自身进程缓冲区）。
 */
interface LogLineSource {

    /** 拉取自上次调用以来的新行（增量，由实现方维护游标）；空 = 无新行。 */
    fun sampleLines(): List<String>
}

/** 日志上报出口端口（复用 MessageReportSink 模式：Boolean 语义 + 列表批量）。 */
fun interface LogReportSink {

    /** 投递一批日志载荷；false = 本轮不可投递（调用方入暂存重试）。 */
    fun sendReport(beans: List<WeworkMessageBean>): Boolean
}

/** 日志级别（301/302 分流依据：ERROR → 302）。 */
enum class LogLevel { DEBUG, VERBOSE, INFO, WARN, ERROR }

/** 单条日志行（message 恒为已脱敏态——[LogReportPipeline.parseLine] 边界强制）。 */
data class LogLine(val level: LogLevel, val tag: String, val message: String)

/**
 * SR-06 日志脱敏（task-engine 自持实现；口径与 docs/01 SR-06 行、feature:executor
 * SensitiveRedactor 同规——feature 车道文件不动，本对象服务 FR-49 日志面）：
 * - 手机号：11 位大陆号段整体遮蔽（留前 3 后 2）；
 * - 数字长串：连续 ≥7 位（座机/证件片段兜底，留首 2 末 1）；
 * - token：≥32 位十六进制长串（遮中段，留首 6 末 4）；
 * - **敏感词表**：词表命中处遮蔽为 `***`（默认词表内置，可注入扩充——密码/凭据类）。
 */
object LogRedactor {

    /** 默认敏感词表（SR-06 敏感词类目；全小写，匹配忽略大小写）。 */
    val DEFAULT_SENSITIVE_WORDS: List<String> = listOf(
        "password", "passwd", "pwd", "token", "secret", "credential", "authorization", "cookie",
    )

    private val PHONE = Regex("""1[3-9]\d{9}""")
    private val DIGIT_RUN = Regex("""\d{7,}""")
    private val HEX_TOKEN = Regex("""[0-9a-fA-F]{32,}""")

    /** 手机号遮蔽：保留前 3 后 2（审计可区分、不可还原）。 */
    fun maskPhones(text: String): String =
        text.replace(PHONE) { it.value.take(3) + "****" + it.value.takeLast(2) }

    /** 数字长串遮蔽：保留首 2 末 1。 */
    fun maskDigitRuns(text: String): String =
        maskPhones(text).replace(DIGIT_RUN) { it.value.take(2) + "*" + it.value.takeLast(1) }

    /** token 长串遮蔽：保留首 6 末 4。 */
    fun maskTokens(text: String): String =
        text.replace(HEX_TOKEN) { it.value.take(6) + "…" + it.value.takeLast(4) }

    /** 词表遮蔽：命中词整词替换为 `***`（忽略大小写，先于结构遮蔽防词表词内嵌敏感串）。 */
    fun maskWords(text: String, words: List<String> = DEFAULT_SENSITIVE_WORDS): String {
        var out = text
        for (w in words) {
            if (w.isBlank()) continue
            val esc = Regex.escape(w)
            // 赋值形态（password=hunter2 / token: abc）整段遮蔽——凭据值不落日志
            out = out.replace(Regex("(?i)$esc\\s*[=:：]\\s*[^\\s&;\"',]+"), "***")
            // 裸词形态（词本身出现即遮蔽）
            out = out.replace(Regex("(?i)$esc"), "***")
        }
        return out
    }

    /** 组合脱敏（FR-49 日志面统一入口；词表 → token → 数字串级联）。 */
    fun sanitize(text: String, words: List<String> = DEFAULT_SENSITIVE_WORDS): String =
        maskTokens(maskDigitRuns(maskWords(text, words)))
}
