/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/**
 * FR-49 日期分区日志写入器（W-M6 落盘器，taskpack §3.2）：
 * - 文件名 `worktool_(yyyy_MM_dd).log`（**514 时间段模式的文件名正则依赖**——
 *   上游按 `…_(yyyy_MM_dd)_….log` 分区约定检索）；
 * - 30 天保留窗口（写入时惰性清理，无专职线程）；
 * - 供 [DevicePortImpls.Logs] 消费（301/302 尾读、510 打日包、514 双模式片段）。
 */
object LogStoreWriter {

    private const val RETAIN_DAYS = 30
    private const val MAX_FILE_BYTES = 8L * 1024 * 1024 // 单日护栏（尾部截断登记为截断场景）

    private fun fileNameFor(date: Date): String =
        SimpleDateFormat("worktool_(yyyy_MM_dd).log", Locale.US).format(date)

    fun fileFor(dir: File, date: Date = Date()): File = File(dir, fileNameFor(date))

    /** 追加一行；建目录 + 跨天自动切文件 + 惰性清理旧窗口。 */
    fun appendLine(dir: File, clock: () -> Long, line: String) {
        runCatching {
            dir.mkdirs()
            val f = fileFor(dir, Date(clock()))
            // 单日护栏：超限截半（保留最新）——登记：截断场景由 514 meta.truncated 语义承接
            if (f.isFile && f.length() > MAX_FILE_BYTES) {
                val keep = f.readLines().let { it.subList(it.size / 2, it.size) }
                f.writeText(keep.joinToString("\n", postfix = "\n"))
            }
            f.appendText(line + "\n")
            pruneOld(dir, clock)
        }
    }

    /** 保留窗口清理（失败静默——日志面不阻断主流程）。 */
    private fun pruneOld(dir: File, clock: () -> Long) {
        runCatching {
            val cutoff = clock() - RETAIN_DAYS * 24L * 3600 * 1000
            dir.listFiles { f -> f.name.startsWith("worktool_(2") }?.forEach { f ->
                if (f.lastModified() < cutoff) f.delete()
            }
        }
    }

    /** 按契约日期（yyyy-MM-dd）取该日日志文件（不存在=null）。 */
    fun fileForContractDate(dir: File, logDate: String): File? {
        val name = "worktool_(" + logDate.replace("-", "_") + ").log"
        val f = File(dir, name)
        return f.takeIf { it.isFile }
    }

    /** 最近一个有内容的日志文件（301/302 尾读目标；无=null）。 */
    fun latestFile(dir: File): File? =
        dir.listFiles { f -> f.name.startsWith("worktool_(2") && f.length() > 0 }
            ?.maxByOrNull { it.name }

    /** 510：单日打 zip（内层名 log_yyyy_MM_dd.zip 的实体=原日志文件名）。 */
    fun dayZip(dir: File, logDate: String): ByteArray? {
        val f = fileForContractDate(dir, logDate) ?: return null
        return runCatching {
            java.io.ByteArrayOutputStream().use { bos ->
                ZipOutputStream(bos).use { zos ->
                    zos.putNextEntry(ZipEntry(f.name))
                    f.inputStream().use { it.copyTo(zos) }
                    zos.closeEntry()
                }
                bos.toByteArray()
            }
        }.getOrNull()
    }
}
