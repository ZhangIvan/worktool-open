/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeReceiveLoop
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 202 LOOP_RECEIVE_NEW_MESSAGE 执行器场景：开/关成功 / 开关被拒 /
 * 暂停拦截 / 干跑。
 */
class ReceiveLoopSwitchExecutorTest {

    private val loop = FakeReceiveLoop(enabled = true)
    private val registry = ExecutorRegistry.of(ReceiveLoopSwitchExecutor(loop))
    private val driver = FakeUiDriver(FakeClock())
    private val gate = FakePauseGate()

    private fun runner() = CommandRunner(driver, gate)

    @Test
    fun `成功-关闭主循环`() {
        val report = runner().execute(CommandIntent.ReceiveLoopSwitch(enabled = false), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(!loop.enabled)
        assertEquals(listOf("apply-loop-switch"), report.executedSteps.map { it.id })
    }

    @Test
    fun `成功-开启主循环`() {
        loop.enabled = false
        val report = runner().execute(CommandIntent.ReceiveLoopSwitch(enabled = true), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(loop.enabled)
    }

    @Test
    fun `开关被拒-循环组件不可用则失败`() {
        loop.refuse = true
        val report = runner().execute(CommandIntent.ReceiveLoopSwitch(enabled = true), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ACTION_REJECTED, report.errorCode)
        assertEquals("apply-loop-switch", report.errorStepId)
    }

    @Test
    fun `暂停拦截-执行前已暂停则开关不变`() {
        gate.paused = true
        loop.enabled = true
        val report = runner().execute(CommandIntent.ReceiveLoopSwitch(enabled = false), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertTrue("开关未被翻转", loop.enabled)
    }

    @Test
    fun `干跑-产出报告且开关不变`() {
        loop.enabled = true
        val dry = runner().dryRun(CommandIntent.ReceiveLoopSwitch(enabled = false), registry)

        assertEquals(CommandIntent.ReceiveLoopSwitch(false).commandType, dry.commandType)
        assertTrue(dry.paramsSummary.contains("enabled=false"))
        assertTrue("开关未被翻转", loop.enabled)
    }
}
