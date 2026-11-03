/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeConsoleNotice
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 102 TYPE_CONSOLE_TOAST 执行器场景（docs/10 B3）：60s 节流 + 二次熔断 +
 * 幂等 + 暂停拦截 + 干跑（假时钟确定性复现窗口边界）。
 */
class ConsoleToastExecutorTest {

    private val clock = FakeClock()
    private val console = FakeConsoleNotice()

    private fun executor() = ConsoleToastExecutor(console, clock = { clock.now })

    private val registry = ExecutorRegistry.of(executor())
    private val driver = FakeUiDriver(clock)
    private val gate = FakePauseGate()
    private fun runner() = CommandRunner(driver, gate)

    private fun intent(code: Int = 501013, reason: String = "机器人重复登录") =
        CommandIntent.ConsoleToast(errorCode = code, errorReason = reason)

    @Test
    fun `成功-窗口外首条仅记录不弹窗不停机`() {
        val report = runner().execute(intent(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("无弹窗", 0, console.notices.size)
        assertEquals("未停机", 0, console.stopRequests.size)
        assertTrue(!console.mainFunctionStopped)
    }

    @Test
    fun `熔断-60秒内第二条停机加弹窗`() {
        runner().execute(intent(), registry)
        clock.advance(30_000)
        val report = runner().execute(intent(code = 501011, reason = "robotId 错误"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(listOf(501011 to "robotId 错误"), console.notices)
        assertEquals(listOf(501011 to "robotId 错误"), console.stopRequests)
        assertTrue(console.mainFunctionStopped)
    }

    @Test
    fun `窗口过期-90秒后第二条按首条处理`() {
        runner().execute(intent(), registry)
        clock.advance(90_000)
        runner().execute(intent(), registry)

        assertEquals("仍无弹窗", 0, console.notices.size)
        assertTrue(!console.mainFunctionStopped)
    }

    @Test
    fun `幂等-已停机后再次到达维持停机态`() {
        runner().execute(intent(), registry)
        clock.advance(30_000)
        runner().execute(intent(), registry)
        clock.advance(120_000)
        runner().execute(intent(code = 501000), registry)

        assertEquals("停机幂等（重复弹提示）", 2, console.notices.size)
        assertTrue(console.mainFunctionStopped)
    }

    @Test
    fun `暂停拦截-执行前已暂停则零端口动作`() {
        gate.paused = true
        val report = runner().execute(intent(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals("零端口动作", 0, console.notices.size + console.stopRequests.size)
    }

    @Test
    fun `干跑-产出报告且零端口动作`() {
        val dry = runner().dryRun(intent(), registry)

        assertEquals(CommandIntent.ConsoleToast(0, null).commandType, dry.commandType)
        assertTrue(dry.plannedSteps.single().contains("60s"))
        assertEquals("零端口动作", 0, console.notices.size + console.stopRequests.size)
    }
}
