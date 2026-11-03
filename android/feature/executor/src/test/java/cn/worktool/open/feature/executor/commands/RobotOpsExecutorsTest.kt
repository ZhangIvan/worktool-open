/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeAppConfig
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeCommandQueue
import cn.worktool.open.feature.executor.fake.FakeLogStore
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeReportSink
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingBatchSink
import cn.worktool.open.feature.executor.fake.RecordingStructuredSink
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 301/302/304/305/306/307/303 机器人运维族执行器场景（docs/10 B3）。
 */
class RobotOpsExecutorsTest {

    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val driver = FakeUiDriver(clock)

    // ---- 301/302 ----

    @Test
    fun `301成功-运行日志经WS通道上报`() {
        val sink = FakeReportSink()
        val logs = FakeLogStore()
        val registry = ExecutorRegistry.of(RobotLogExecutor(sink, logs))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotLog(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        val bean = sink.acceptedBeans().single()
        assertEquals(MessageTypes.ROBOT_LOG, bean.type)
        assertTrue(bean.extraString("log").orEmpty().contains("appVersion"))
    }

    @Test
    fun `302成功-错误日志分流到302通道`() {
        val sink = FakeReportSink()
        val logs = FakeLogStore()
        val registry = ExecutorRegistry.of(RobotErrorLogExecutor(sink, logs))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RobotErrorLog(logText = "未捕获异常: KotlinNullPointerException"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val bean = sink.acceptedBeans().single()
        assertEquals("分流铁证：错误级走 302", MessageTypes.ROBOT_ERROR_LOG, bean.type)
        assertTrue(bean.extraString("log").orEmpty().contains("KotlinNullPointerException"))
    }

    @Test
    fun `301负例-通道不可投递`() {
        val sink = FakeReportSink().apply { accept = false }
        val logs = FakeLogStore()
        val registry = ExecutorRegistry.of(RobotLogExecutor(sink, logs))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotLog(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ACTION_REJECTED, report.errorCode)
    }

    // ---- 304 ----

    @Test
    fun `304成功-清空队列并出被清理id集`() {
        val queue = FakeCommandQueue(pending = listOf("id-1", "id-2", "id-3"))
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueClearExecutor(queue, batch))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotQueueClear(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L2, report.riskLevel)
        assertEquals(listOf("id-1", "id-2", "id-3"), batch.batches.single().second.map { it.target })
        assertTrue("队列已空", queue.queue.isEmpty())
    }

    @Test
    fun `304边界-空队列清零仍成功`() {
        val queue = FakeCommandQueue()
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueClearExecutor(queue, batch))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotQueueClear(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, batch.batches.single().second.size)
    }

    @Test
    fun `304暂停拦截-L2治理差异零清理`() {
        val queue = FakeCommandQueue(pending = listOf("id-1"))
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueClearExecutor(queue, batch))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotQueueClear(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertTrue("未清队列（我方统一走暂停门=增强差异）", queue.queue.isNotEmpty())
    }

    // ---- 305 ----

    @Test
    fun `305成功-精确取消存在的id`() {
        val queue = FakeCommandQueue(pending = listOf("id-A", "id-B"))
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueRemoveExecutor(queue, batch))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RobotQueueRemove(listOf("id-A")),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("剩余 id-B", listOf("id-B"), queue.queue.toList())
        assertTrue(batch.batches.single().second.single().succeeded)
    }

    @Test
    fun `305幂等-不存在的id不报错恒成功`() {
        val queue = FakeCommandQueue(pending = listOf("id-A"))
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueRemoveExecutor(queue, batch))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RobotQueueRemove(listOf("不存在")),
            registry,
        )

        assertEquals("恒成功（幂等友好）", ExecutionStatus.SUCCEEDED, report.status)
        val outcome = batch.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertTrue(outcome.reason.orEmpty().contains("未在待执行队列找到"))
    }

    // ---- 306 ----

    @Test
    fun `306成功-快照FIFO序并可附详情`() {
        val queue = FakeCommandQueue(pending = listOf("id-1", "id-2")).apply {
            details["id-1"] = """{"type":203}"""
            details["id-2"] = """{"type":206}"""
        }
        val structured = RecordingStructuredSink()
        val registry = ExecutorRegistry.of(RobotQueueGetExecutor(queue, structured))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RobotQueueGet(showMessageHistory = true),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertTrue(payload["messageIdList"].toString().contains("id-1"))
        assertTrue("详情深拷贝口径", payload["commandList"].toString().contains("type"))
    }

    @Test
    fun `306默认-不附commandList`() {
        val queue = FakeCommandQueue(pending = listOf("id-1"))
        val structured = RecordingStructuredSink()
        val registry = ExecutorRegistry.of(RobotQueueGetExecutor(queue, structured))
        CommandRunner(driver, gate).execute(CommandIntent.RobotQueueGet(), registry)

        val (_, payload) = structured.results.single()
        assertTrue(!payload.containsKey("commandList"))
    }

    // ---- 307 ----

    @Test
    fun `307成功-通知即全量重拉`() {
        val appConfig = FakeAppConfig(refreshOk = true)
        val registry = ExecutorRegistry.of(RobotConfigUpdatedExecutor(appConfig))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotConfigUpdated(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(1, appConfig.refreshCount)
    }

    @Test
    fun `307负例-重拉失败保持旧配置`() {
        val appConfig = FakeAppConfig(refreshOk = false)
        val registry = ExecutorRegistry.of(RobotConfigUpdatedExecutor(appConfig))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotConfigUpdated(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ACTION_REJECTED, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("保持旧配置"))
    }

    // ---- 303 ----

    @Test
    fun `303成功-回显即测试零界面动作`() {
        val echoes = mutableListOf<String>()
        val registry = ExecutorRegistry.of(RobotControllerTestExecutor(log = { echoes += it }))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RobotControllerTest(echo = "probe-2026-11-03T09:00"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("日志回显一次", 1, echoes.size)
        assertTrue(echoes.single().contains("probe-2026-11-03T09:00"))
        assertEquals("零界面动作", 0, driver.actionLog.size)
    }

    @Test
    fun `301干跑-零上报`() {
        val sink = FakeReportSink()
        val logs = FakeLogStore()
        val registry = ExecutorRegistry.of(RobotLogExecutor(sink, logs))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.RobotLog(), registry)
        assertEquals(0, sink.attempts.size)
        assertEquals(1, dry.plannedSteps.size)
    }

    @Test
    fun `302负例-日志为空`() {
        val sink = FakeReportSink()
        val logs = FakeLogStore(errorText = "")
        val registry = ExecutorRegistry.of(RobotErrorLogExecutor(sink, logs))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotErrorLog(), registry)
        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
    }

    @Test
    fun `302干跑-零上报`() {
        val sink = FakeReportSink()
        val logs = FakeLogStore()
        val registry = ExecutorRegistry.of(RobotErrorLogExecutor(sink, logs))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.RobotErrorLog(), registry)
        assertEquals(0, sink.attempts.size)
        assertEquals(1, dry.plannedSteps.size)
    }

    @Test
    fun `305干跑-零移除`() {
        val queue = FakeCommandQueue(pending = listOf("id-A"))
        val batch = RecordingBatchSink()
        val registry = ExecutorRegistry.of(RobotQueueRemoveExecutor(queue, batch))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.RobotQueueRemove(listOf("id-A")), registry)
        assertEquals("队列未变", listOf("id-A"), queue.queue.toList())
        assertEquals(0, batch.batches.size)
    }

    @Test
    fun `306负例-空队列快照成功`() {
        val queue = FakeCommandQueue(pending = emptyList())
        val structured = RecordingStructuredSink()
        val registry = ExecutorRegistry.of(RobotQueueGetExecutor(queue, structured))
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotQueueGet(), registry)
        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(structured.results.single().second["messageIdList"].toString().contains("[]"))
    }

    @Test
    fun `306干跑-零快照`() {
        val queue = FakeCommandQueue(pending = listOf("id-1"))
        val structured = RecordingStructuredSink()
        val registry = ExecutorRegistry.of(RobotQueueGetExecutor(queue, structured))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.RobotQueueGet(), registry)
        assertEquals(0, structured.results.size)
        assertEquals(1, dry.plannedSteps.size)
    }

    @Test
    fun `307干跑-零重拉`() {
        val appConfig = FakeAppConfig()
        val registry = ExecutorRegistry.of(RobotConfigUpdatedExecutor(appConfig))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.RobotConfigUpdated(), registry)
        assertEquals(0, appConfig.refreshCount)
        assertEquals(1, dry.plannedSteps.size)
    }

    @Test
    fun `303暂停拦截-零回显`() {
        val echoes = mutableListOf<String>()
        val registry = ExecutorRegistry.of(RobotControllerTestExecutor(log = { echoes += it }))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.RobotControllerTest(), registry)
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, echoes.size)
    }

    @Test
    fun `303干跑-零回显`() {
        val echoes = mutableListOf<String>()
        val registry = ExecutorRegistry.of(RobotControllerTestExecutor(log = { echoes += it }))
        CommandRunner(driver, gate).dryRun(CommandIntent.RobotControllerTest(), registry)
        assertEquals(0, echoes.size)
    }
}
