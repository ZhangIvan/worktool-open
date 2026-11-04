/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.ActivityManager
import android.content.Context
import android.os.Build
import android.os.Environment
import android.os.StatFs
import android.widget.Toast
import cn.worktool.open.feature.executor.ports.AppConfigPort
import cn.worktool.open.feature.executor.ports.CurrentCorpPort
import cn.worktool.open.feature.executor.ports.DeviceResourcePort
import cn.worktool.open.feature.executor.ports.DeviceResourceSnapshot
import cn.worktool.open.feature.executor.ports.LogSnippet
import cn.worktool.open.feature.executor.ports.LogStorePort
import cn.worktool.open.feature.executor.ports.LogUploadPort
import cn.worktool.open.feature.executor.ports.QrCodeJoinPort
import cn.worktool.open.feature.executor.ports.QrJoinOutcome
import cn.worktool.open.feature.executor.ports.RemoteFilePort
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import okhttp3.MediaType.Companion.toMediaType
import java.util.concurrent.TimeUnit

/**
 * 设备侧真实端口实现集（W-M6 fullBatch 装配，SessionRuntime 注入）。
 *
 * 边界如实登记（不假装成功）：
 * - [LogStorePort.collectDayLogZip]/[findSnippetBy*]：日志文件系统按日期分区/
 *   messageId 行格式尚未落地（FR-49 落盘器 = W-M6 Phase B）——本版返回 null
 *   （510/514 执行器按「无日志」语义失败/空结果，契约可辨）；
 * - [QrCodeJoinPort.weChatScanAlbumJoin]：FR-56 个微规则包未交付——相册下载
 *   为真实实现，扫码入群诚实返回 [QrJoinOutcome.QR_NOT_RECOGNIZED] 使
 *   243 以失败回执（矩阵保持 ⬜，不虚报成功）；
 * - [CurrentCorpPort]：企微当前企业名需 UI 域读取——v1 返回 null（223 执行器
 *   走完整切换流程，语义保守正确）。
 */
object DevicePortImpls {

    /** 513：真机资源快照（内存/存储/机型；外部存储缺失=null）。 */
    class Resources(private val context: Context) : DeviceResourcePort {
        override fun snapshot(): DeviceResourceSnapshot {
            val rt = Runtime.getRuntime()
            val am = context.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
            val mem = ActivityManager.MemoryInfo().also { am.getMemoryInfo(it) }
            val internal = StatFs(Environment.getDataDirectory().absolutePath)
            val externalRoot = Environment.getExternalStorageDirectory().takeIf {
                Environment.getExternalStorageState() == Environment.MEDIA_MOUNTED
            }
            val external = externalRoot?.let { runCatching { StatFs(it.absolutePath) }.getOrNull() }
            return DeviceResourceSnapshot(
                totalMemoryMb = mem.totalMem shr 20,
                availableMemoryMb = mem.availMem shr 20,
                appMaxMemoryMb = rt.maxMemory() shr 20,
                appAllocatedMemoryMb = rt.totalMemory() shr 20,
                totalInternalStorageMb = internal.totalBytes shr 20,
                availableInternalStorageMb = internal.availableBytes shr 20,
                totalExternalStorageMb = external?.totalBytes?.shr(20),
                availableExternalStorageMb = external?.availableBytes?.shr(20),
                deviceModel = Build.MODEL ?: "unknown",
                androidVersion = Build.VERSION.RELEASE ?: "?",
                sdkVersion = Build.VERSION.SDK_INT,
            )
        }
    }

    /**
     * 301/302/510/514：本地日志读取（FR-49 落盘器全实装，W-M6）：
     * - 301/302 = 最近分区文件尾部；
     * - 510 collectDayLogZip = 该日分区 zip（内层实体=原日志文件名）；
     * - 514 messageId 模式 = 行内含目标 id 的命中区间；时间段模式 = 行首时间戳过滤；
     *   产物 gzip+base64 + meta{logFilePath/startLine/endLine/matchedCount}。
     */
    class Logs(private val logsDir: () -> File) : LogStorePort {
        override fun recentLogText(maxChars: Int): String = tailText(LogStoreWriter.latestFile(logsDir()), maxChars)

        override fun recentErrorText(maxChars: Int): String =
            tailText(LogStoreWriter.latestFile(logsDir()), maxChars * 4)
                .lineSequence()
                .filter { it.contains("E ", true) || it.contains("失败") || it.contains("拒帧") }
                .joinToString("\n")
                .takeLast(maxChars)

        override fun collectDayLogZip(logDate: String): ByteArray? =
            LogStoreWriter.dayZip(logsDir(), logDate)

        override fun findSnippetByMessageId(targetMessageId: String): LogSnippet? =
            snippet(linesWithIndex()) { it.contains(targetMessageId) }

        override fun findSnippetByRange(startTime: String, endTime: String): LogSnippet? =
            snippet(linesWithIndex()) { line ->
                stampOf(line)?.let { it >= startTime && it <= endTime } == true
            }

        /** 行内时间戳（写入器行首 `[MM-dd HH:mm:ss.SSS]`；年份补当前年——跨年边界近似，登记）。 */
        private fun stampOf(line: String): String? {
            val m = Regex("^(\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2})").find(line.trimStart('[')) ?: return null
            val year = SimpleDateFormat("yyyy", Locale.US).format(Date())
            return "$year-" + m.groupValues[1]
        }

        private fun linesWithIndex(): List<Pair<Int, String>> {
            val f = LogStoreWriter.latestFile(logsDir()) ?: return emptyList()
            return runCatching { f.readLines().withIndex().map { it.index to it.value } }.getOrDefault(emptyList())
        }

        private fun snippet(lines: List<Pair<Int, String>>, match: (String) -> Boolean): LogSnippet? {
            val hits = lines.filter { match(it.second) }
            if (hits.isEmpty()) return null
            val first = hits.first().first
            val last = hits.last().first
            val body = lines.subList(first, last + 1).joinToString("\n") { it.second }
            val gzipB64 = java.io.ByteArrayOutputStream().use { bos ->
                java.util.zip.GZIPOutputStream(bos).use { it.write(body.toByteArray(Charsets.UTF_8)) }
                android.util.Base64.encodeToString(bos.toByteArray(), android.util.Base64.NO_WRAP)
            }
            val f = LogStoreWriter.latestFile(logsDir())
            return LogSnippet(
                payloadBase64 = gzipB64,
                truncated = false,
                meta = mapOf(
                    "logFilePath" to (f?.absolutePath ?: ""),
                    "startLine" to (first + 1).toString(),
                    "endLine" to (last + 1).toString(),
                    "matchedCount" to hits.size.toString(),
                ),
            )
        }

        private fun tailText(f: File?, maxChars: Int): String = runCatching {
            f ?: return ""
            if (!f.isFile) return ""
            val bytes = ByteArray(minOf(f.length(), maxChars.toLong() * 4).toInt())
            java.io.RandomAccessFile(f, "r").use { raf ->
                val start = maxOf(0L, f.length() - bytes.size)
                raf.seek(start)
                raf.readFully(bytes)
            }
            String(bytes, Charsets.UTF_8).takeLast(maxChars)
        }.getOrDefault("")
    }

    /** 510：multipart 上传 {base}/robot/clientLog/upload（OkHttp；同步调用在执行线程）。 */
    class LogUpload(private val baseUrl: () -> String) : LogUploadPort {
        override fun uploadLogZip(fileName: String, zip: ByteArray): Boolean = runCatching {
            val client = okhttp3.OkHttpClient.Builder()
                .callTimeout(60, TimeUnit.SECONDS).build()
            val body = okhttp3.MultipartBody.Builder()
                .setType(okhttp3.MultipartBody.FORM)
                .addFormDataPart("robotId", "self")
                .addFormDataPart("clientVersion", AppInfo.VERSION)
                .addFormDataPart("deviceInfo", "Android ${Build.VERSION.RELEASE ?: "?"}")
                .addFormDataPart(
                    "file", fileName,
                    okhttp3.RequestBody.create("application/zip".toMediaType(), zip),
                )
                .build()
            val req = okhttp3.Request.Builder()
                .url(baseUrl().trimEnd('/') + "/robot/clientLog/upload")
                .post(body).build()
            client.newCall(req).execute().use { it.isSuccessful }
        }.getOrDefault(false)
    }

    /** 218/224/243：远端文件拉取（下载 → 落盘应用私有目录按 objectName）。 */
    class RemoteFiles(private val context: Context) : RemoteFilePort {
        override fun fetch(url: String, objectName: String): Boolean = runCatching {
            val client = okhttp3.OkHttpClient.Builder()
                .callTimeout(120, TimeUnit.SECONDS).build()
            val req = okhttp3.Request.Builder().url(url).build()
            client.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) return@runCatching false
                val target = File(context.filesDir, "downloads/$objectName")
                target.parentFile?.mkdirs()
                target.outputStream().use { out -> resp.body?.byteStream()?.copyTo(out) ?: return@runCatching false }
                true
            }
        }.getOrDefault(false)
    }

    /** 307：服务端配置重拉（GET {base}/robot/robotInfo；失败=false 保持旧配置）。 */
    class AppConfig(private val baseUrl: () -> String) : AppConfigPort {
        override fun refreshRemoteConfig(): Boolean = runCatching {
            val client = okhttp3.OkHttpClient.Builder()
                .callTimeout(15, TimeUnit.SECONDS).build()
            val req = okhttp3.Request.Builder()
                .url(baseUrl().trimEnd('/') + "/robot/robotInfo").get().build()
            client.newCall(req).execute().use { it.isSuccessful }
        }.getOrDefault(false)
    }

    /** 223：当前企业名（UI 域未接线 → null=未知，见类注）。 */
    object CurrentCorp : CurrentCorpPort {
        override fun currentCorpName(): String? = null
    }

    /** 243：相册下载真实；个微扫码=诚实失败（FR-56 未交付，见类注）。 */
    class QrJoin(private val context: Context) : QrCodeJoinPort {
        override fun downloadAndSaveToAlbum(fileUrl: String): Boolean = runCatching {
            val client = okhttp3.OkHttpClient.Builder()
                .callTimeout(60, TimeUnit.SECONDS).build()
            val resp = client.newCall(okhttp3.Request.Builder().url(fileUrl).build()).execute()
            resp.use {
                if (!it.isSuccessful) return@runCatching false
                val tmp = File(context.cacheDir, "qr_${System.currentTimeMillis()}.png")
                tmp.outputStream().use { out -> it.body?.byteStream()?.copyTo(out) ?: return@runCatching false }
                // 写系统相册（MediaStore 插入；失败=false 诚实上报）
                val values = android.content.ContentValues().apply {
                    put(android.provider.MediaStore.Images.Media.DISPLAY_NAME, tmp.name)
                    put(android.provider.MediaStore.Images.Media.MIME_TYPE, "image/png")
                }
                val uri = context.contentResolver.insert(
                    android.provider.MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values,
                ) ?: return@runCatching false
                context.contentResolver.openOutputStream(uri)?.use { out -> tmp.inputStream().copyTo(out) } != null
            }
        }.getOrDefault(false)

        override fun weChatScanAlbumJoin(): QrJoinOutcome = QrJoinOutcome.QR_NOT_RECOGNIZED
    }

    /** 102 控制台提示：主线程 Toast（SessionRuntime 注入；无 Activity 依赖）。 */
    class ConsoleToast(private val context: Context) {
        private val handler = android.os.Handler(android.os.Looper.getMainLooper())
        fun notice(errorCode: Int, errorReason: String, stopMain: Boolean) {
            handler.post {
                Toast.makeText(
                    context,
                    if (stopMain) "[$errorCode] $errorReason（主功能已停止）" else "[$errorCode] $errorReason",
                    if (stopMain) Toast.LENGTH_LONG else Toast.LENGTH_SHORT,
                ).show()
            }
        }
    }

    /** 日志文件名统一（FR-49 落盘器 Phase B 对齐 514 文件名正则）。 */
    fun logFileNameFor(date: Date = Date()): String =
        "worktool_(yyyy_MM_dd).log".let { SimpleDateFormat("worktool_(yyyy_MM_dd).log", Locale.US).format(date) }
}
