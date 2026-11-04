/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.app.DevicePortImpls.PortHttpClients
import java.io.File
import java.io.IOException
import java.io.InputStream
import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * S3/R5 端口收口回归（code review 遗留；纯 JVM，不触 Android 框架）：
 * - S3 OkHttp 客户端复用：每端口 client 构造一次复用，不再每次调用 new；
 *   callTimeout 各自保留（510 上传 60s / 218 下载 120s / 307 配置 15s / 243 相册 60s）；
 * - R5 下载落盘原子语义：中途失败删除半文件；相册 tmp png 用后即删（成败都清理）。
 */
class DevicePortImplsPortCleanupTest {

    // ---- S3：client 复用 + callTimeout 各自保留 ----

    @Test
    fun `S3-各端口client构造一次复用且callTimeout各自保留`() {
        assertEquals("510 日志上传 60s", 60_000, PortHttpClients.logUpload.callTimeoutMillis)
        assertEquals("218 远端文件下载 120s", 120_000, PortHttpClients.remoteFiles.callTimeoutMillis)
        assertEquals("307 配置重拉 15s", 15_000, PortHttpClients.appConfig.callTimeoutMillis)
        assertEquals("243 相册下载 60s", 60_000, PortHttpClients.qrJoin.callTimeoutMillis)
        // 各端口独立实例（超时口径互不串扰）
        assertNotSame(PortHttpClients.logUpload, PortHttpClients.remoteFiles)
        assertNotSame(PortHttpClients.appConfig, PortHttpClients.qrJoin)
        // 同端口重复取用 = 同一实例（不再每次 new）
        assertSame(PortHttpClients.logUpload, PortHttpClients.logUpload)
    }

    @Test
    fun `S3-端口实例持复用client字段-多次调用不重建`() {
        // LogUpload/AppConfig 无 Context 依赖，可 JVM 直构；127.0.0.1:1 连接拒绝毫秒级快速失败
        val up = DevicePortImpls.LogUpload({ "http://127.0.0.1:1" })
        val fUp = DevicePortImpls.LogUpload::class.java.getDeclaredField("client")
        fUp.isAccessible = true
        assertSame("LogUpload 字段接线到复用 client", PortHttpClients.logUpload, fUp.get(up))
        assertTrue("上传快速失败（连接拒绝）", !up.uploadLogZip("x.zip", ByteArray(0)))
        assertTrue(!up.uploadLogZip("x.zip", ByteArray(0)))
        assertSame("多次上传后仍是同一 client 实例（S3 复用）", PortHttpClients.logUpload, fUp.get(up))

        val cfg = DevicePortImpls.AppConfig({ "http://127.0.0.1:1" })
        val fCfg = DevicePortImpls.AppConfig::class.java.getDeclaredField("client")
        fCfg.isAccessible = true
        assertSame("AppConfig 字段接线到复用 client", PortHttpClients.appConfig, fCfg.get(cfg))
        assertTrue("配置重拉快速失败（连接拒绝）", !cfg.refreshRemoteConfig())
        assertSame("多次重拉后仍是同一 client 实例（S3 复用）", PortHttpClients.appConfig, fCfg.get(cfg))
    }

    // ---- R5：下载半文件删除 / tmp png 用后即删 ----

    @Test
    fun `R5-下载中途失败删除半文件`() {
        val dir = File(System.getProperty("java.io.tmpdir"), "r5-${System.nanoTime()}").apply { mkdirs() }
        val partial = object : InputStream() {
            var n = 0
            override fun read(): Int {
                if (n++ >= 4) throw IOException("中途断流")
                return 'x'.code
            }
        }
        val target = File(dir, "downloads/a.bin")
        assertFalse("中途断流=失败", downloadTo(target) { partial })
        assertFalse("半文件须删除（不留损坏产物）", target.exists())

        assertFalse("空响应体=失败", downloadTo(File(dir, "downloads/b.bin")) { null })
        assertFalse("空响应体不留空文件", File(dir, "downloads/b.bin").exists())

        assertTrue("完整下载=成功", downloadTo(File(dir, "downloads/c.bin")) { "hello".byteInputStream() })
        assertEquals("成功路径产物完整保留", "hello", File(dir, "downloads/c.bin").readText())
        dir.deleteRecursively()
    }

    @Test
    fun `R5-tmpPng用后即删-成败都清理`() {
        val dir = File(System.getProperty("java.io.tmpdir"), "r5-${System.nanoTime()}").apply { mkdirs() }
        var seen: File? = null
        val path = withTempPng(dir) { tmp ->
            seen = tmp
            tmp.writeText("png")
            tmp.absolutePath
        }
        assertEquals("block 内拿到 tmp png", path, seen?.absolutePath)
        assertFalse("用后即删（成功路径）", (seen ?: error("unreachable")).exists())

        try {
            withTempPng(dir) { tmp ->
                tmp.writeText("png2")
                error("block 抛异常")
            }
            fail("block 异常须透传")
        } catch (e: IllegalStateException) {
            // 预期透传
        }
        assertEquals("异常路径残留 tmp 数=0（finally 清理）", 0, dir.listFiles()!!.size)
        dir.deleteRecursively()
    }
}
