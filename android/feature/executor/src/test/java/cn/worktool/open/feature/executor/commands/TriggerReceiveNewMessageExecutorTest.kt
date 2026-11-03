/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeReportSink
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 283 DO_RECEIVE_NEW_MESSAGE 执行器场景：成功扫描上报（101 信封）/
 * 通道不可投递失败 / 空会话列表零事件空过 / 暂停拦截 / 干跑。
 */
class TriggerReceiveNewMessageExecutorTest {

    private val sink = FakeReportSink()
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    private val registry = ExecutorRegistry.of(TriggerReceiveNewMessageExecutor(sink))

    private fun driverWith(conversations: List<String>): FakeUiDriver {
        val home = HomeFixture(conversations = conversations)
        return FakeUiDriver(clock).apply { show(home.screen) }
    }

    private fun runner(driver: FakeUiDriver) = CommandRunner(driver, gate)

    @Test
    fun `成功-扫描可见会话并以101信封上报`() {
        val driver = driverWith(listOf("外部群测试群", "工作群"))
        val report = runner(driver).execute(CommandIntent.TriggerReceiveNewMessage(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(
            listOf("go-home", "scan-conversations", "emit-report"),
            report.executedSteps.map { it.id },
        )
        val beans = sink.acceptedBeans()
        assertEquals("两会话各一条 101", 2, beans.size)
        assertTrue(beans.all { it.type == MessageTypes.TYPE_RECEIVE_MESSAGE_LIST })
        assertTrue(beans.any { it.extraString("targetRoom") == "外部群测试群" })
    }

    @Test
    fun `失败-上报通道不可投递`() {
        sink.accept = false
        val driver = driverWith(listOf("外部群测试群"))
        val report = runner(driver).execute(CommandIntent.TriggerReceiveNewMessage(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ACTION_REJECTED, report.errorCode)
        assertEquals("emit-report", report.errorStepId)
        assertEquals("已尝试投递（含被拒轮次）", 1, sink.attempts.size)
    }

    @Test
    fun `空会话-零事件空过成功`() {
        val driver = driverWith(emptyList())
        val report = runner(driver).execute(CommandIntent.TriggerReceiveNewMessage(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("零上报", 0, sink.attempts.size)
    }

    @Test
    fun `暂停拦截-执行前已暂停则零扫描零上报`() {
        gate.paused = true
        val driver = driverWith(listOf("外部群测试群"))
        val report = runner(driver).execute(CommandIntent.TriggerReceiveNewMessage(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, report.executedSteps.size)
        assertEquals("零上报", 0, sink.attempts.size)
    }

    @Test
    fun `干跑-产出三步报告且零上报`() {
        val driver = driverWith(listOf("外部群测试群"))
        val dry = runner(driver).dryRun(CommandIntent.TriggerReceiveNewMessage(), registry)

        assertEquals(CommandIntent.TriggerReceiveNewMessage().commandType, dry.commandType)
        assertEquals(3, dry.plannedSteps.size)
        assertEquals("零上报", 0, sink.attempts.size)
    }
}
