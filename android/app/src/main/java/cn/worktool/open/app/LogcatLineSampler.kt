/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.LogLineSource

/**
 * FR-49 logcat 采样源（[LogLineSource] 生产实现，纯 app 侧薄壳——解析/脱敏/批量/
 * 重试全部在纯 JVM LogReportPipeline）。
 *
 * 采样方式：dump-and-clear——`logcat -d`（导出主缓冲全部）后 `logcat -c`（清空），
 * 天然增量（下次 dump 只拿新行）。app 进程读自身 logcat 缓冲无需额外权限。
 *
 * 边界：仅采样本进程缓冲（logcat 主 buffer）；崩溃快照/系统缓冲不属 FR-49 采样面
 * （302 崩溃日志通道由 crashsdk 出站面另行任务）。行格式解析归 LogReportPipeline.parseLine。
 */
class LogcatLineSampler(
    /** dump 超时（防 logcat 挂起拖死采样线程）。 */
    private val timeoutMillis: Long = DEFAULT_TIMEOUT_MS,
) : LogLineSource {

    override fun sampleLines(): List<String> {
        val dump = exec("logcat -d -v time") ?: return emptyList()
        if (dump.isNotBlank()) exec("logcat -c") // 清空已采走行（失败则下轮重复，去重归上游）
        return dump.lineSequence()
            .filter { it.isNotBlank() }
            .toList()
    }

    /** 进程内执行命令并取 stdout（超时杀进程；IO 异常收敛为 null）。 */
    private fun exec(command: String): String? {
        return try {
            val proc = ProcessBuilder("sh", "-c", command).redirectErrorStream(false).start()
            val out = proc.inputStream.use { it.readBytes().toString(Charsets.UTF_8) }
            if (!proc.waitFor(timeoutMillis, java.util.concurrent.TimeUnit.MILLISECONDS)) {
                proc.destroyForcibly()
                null
            } else {
                out
            }
        } catch (_: Exception) {
            null
        }
    }

    companion object {
        const val DEFAULT_TIMEOUT_MS: Long = 3_000
    }
}
