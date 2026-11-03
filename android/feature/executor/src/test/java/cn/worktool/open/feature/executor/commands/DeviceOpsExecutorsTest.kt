/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeDeviceResources
import cn.worktool.open.feature.executor.fake.FakeLogStore
import cn.worktool.open.feature.executor.fake.FakeLogUpload
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeSingleFlightGuard
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingStructuredSink
import cn.worktool.open.feature.executor.fake.defaultResourceSnapshot
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.LogSnippet
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 513/514/510 设备运维族执行器场景（docs/10 B3，L0；纯本地面零企微 UI）。
 */
class DeviceOpsExecutorsTest {

    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()
    private val driver = FakeUiDriver(clock)

    // ---- 513 ----

    @Test
    fun `513成功-资源快照推送且不动企微界面`() {
        val resources = FakeDeviceResources(defaultResourceSnapshot())
        val registry = ExecutorRegistry.of(GetDeviceResourceExecutor(resources, structured))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetDeviceResource(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("纯本地采集零界面动作", 0, driver.actionLog.size)
        val (_, payload) = structured.results.single()
        val data = payload["data"].toString()
        assertTrue(data.contains("15190"))
        assertTrue("无外部存储=null 非 error", data.contains("null"))
        assertTrue("恒等式 total=available+used", data.contains("10530"))
    }

    @Test
    fun `513暂停拦截-零采集`() {
        val resources = FakeDeviceResources(defaultResourceSnapshot())
        val registry = ExecutorRegistry.of(GetDeviceResourceExecutor(resources, structured))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetDeviceResource(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, resources.queried)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `513干跑-零采集零推送`() {
        val resources = FakeDeviceResources(defaultResourceSnapshot())
        val registry = ExecutorRegistry.of(GetDeviceResourceExecutor(resources, structured))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetDeviceResource(), registry)
        assertEquals(0, resources.queried)
        assertEquals(0, structured.results.size)
        assertEquals(1, dry.plannedSteps.size)
    }

    // ---- 514 ----

    private fun snippet(base64: String = "H4sIAAAAAAAA", truncated: Boolean = false) =
        LogSnippet(payloadBase64 = base64, truncated = truncated, meta = mapOf("compression" to "gzip", "encoding" to "base64"))

    @Test
    fun `514成功-messageId模式命中推送`() {
        val logs = FakeLogStore(snippetsByMessageId = mapOf("1784417250785046528" to snippet()))
        val guard = FakeSingleFlightGuard()
        val registry = ExecutorRegistry.of(GetLogSnippetExecutor(logs, structured, guard))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetLogSnippet(targetMessageId = "1784417250785046528"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertTrue(payload["fileBase64"].toString().contains("H4sIA"))
        assertTrue("互斥已释放", !guard.held)
    }

    @Test
    fun `514负例-未命中messageId`() {
        val logs = FakeLogStore(snippetsByMessageId = emptyMap())
        val registry = ExecutorRegistry.of(GetLogSnippetExecutor(logs, structured, FakeSingleFlightGuard()))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetLogSnippet(targetMessageId = "not-exist"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("not-exist"))
    }

    @Test
    fun `514负例-截断即失败码`() {
        val logs = FakeLogStore(
            snippetsByRange = listOf(("08:00" to "20:00") to snippet(base64 = "xx", truncated = true)),
        )
        val registry = ExecutorRegistry.of(GetLogSnippetExecutor(logs, structured, FakeSingleFlightGuard()))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetLogSnippet(logStartTime = "08:00", logEndTime = "20:00"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("日志大小上限"))
    }

    @Test
    fun `514负例-采集进行中互斥拒绝`() {
        val guard = FakeSingleFlightGuard(held = true)
        val logs = FakeLogStore(snippetsByMessageId = mapOf("x" to snippet()))
        val registry = ExecutorRegistry.of(GetLogSnippetExecutor(logs, structured, guard))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetLogSnippet(targetMessageId = "x"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.MUTEX_BUSY, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("正在采集"))
    }

    // ---- 510 ----

    @Test
    fun `510成功-打日包上传`() {
        val zip = ByteArray(64) { 1 }
        val logs = FakeLogStore(dayZips = mapOf("2026-11-02" to zip))
        val upload = FakeLogUpload()
        val registry = ExecutorRegistry.of(UploadLogFileExecutor(logs, upload))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.UploadLogFile(logDate = "2026-11-02"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(listOf("log_2026-11-02.zip"), upload.uploaded)
    }

    @Test
    fun `510负例-无该日日志`() {
        val logs = FakeLogStore(dayZips = emptyMap())
        val upload = FakeLogUpload()
        val registry = ExecutorRegistry.of(UploadLogFileExecutor(logs, upload))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.UploadLogFile(logDate = "2026-10-01"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("未找到指定日期"))
        assertEquals("零上传", 0, upload.uploaded.size)
    }

    @Test
    fun `510负例-上传失败`() {
        val logs = FakeLogStore(dayZips = mapOf("2026-11-02" to ByteArray(8)))
        val upload = FakeLogUpload(accept = false)
        val registry = ExecutorRegistry.of(UploadLogFileExecutor(logs, upload))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.UploadLogFile(logDate = "2026-11-02"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals("upload-zip", report.errorStepId)
    }

    @Test
    fun `510干跑-零上传`() {
        val logs = FakeLogStore(dayZips = mapOf("2026-11-02" to ByteArray(8)))
        val upload = FakeLogUpload()
        val registry = ExecutorRegistry.of(UploadLogFileExecutor(logs, upload))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.UploadLogFile("2026-11-02"), registry)
        assertEquals(0, upload.uploaded.size)
        assertEquals(2, dry.plannedSteps.size)
    }
}
