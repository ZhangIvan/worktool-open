/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeSingleFlightGuard
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MePageFixture
import cn.worktool.open.feature.executor.fixture.StorageCleanFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 237 清理企微存储执行器场景（docs/10 B3，**L2** 破坏性清理）：
 * 成功 / 互斥占用（清理中再触发=MUTEX_BUSY）/ 入口缺失 / L2 暂停拦截 / 干跑。
 */
class CleanCacheExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val guard = FakeSingleFlightGuard()

    private fun world(withCleanButton: Boolean = true): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val me = MePageFixture()
        home.screen.root.children.last().add(me.meTab)
        val storage = StorageCleanFixture(withClean = withCleanButton)
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, me.meTab) { push(me.screen) }
            me.settingsNode?.let { onClick(me.screen, it) { push(storage.settingsScreen) } }
            onClick(storage.settingsScreen, storage.storageEntry) { push(storage.screen) }
            storage.cleanButton?.let { clean ->
                onClick(storage.screen, clean) { push(storage.confirmScreen) }
            }
            storage.confirmNode?.let { confirm ->
                onClick(storage.confirmScreen, confirm) { pop() }
            }
        }
    }

    private fun registry() = ExecutorRegistry.of(CleanCacheExecutor(guard, timeouts = timeouts))

    @Test
    fun `237成功-清理并释放互斥`() {
        val driver = world()
        val report = CommandRunner(driver, gate).execute(CommandIntent.CleanCache(), registry())

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L2, report.riskLevel)
        assertEquals("release-mutex", report.executedSteps.last().id)
        assertTrue("互斥已释放", !guard.held)
    }

    @Test
    fun `237负例-清理进行中再触发互斥拒绝`() {
        guard.held = true
        val driver = world()
        val report = CommandRunner(driver, gate).execute(CommandIntent.CleanCache(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.MUTEX_BUSY, report.errorCode)
        assertEquals("acquire-mutex", report.errorStepId)
        assertEquals("零界面动作", 0, driver.actionLog.size)
    }

    @Test
    fun `237负例-清理按钮缺失`() {
        val driver = world(withCleanButton = false)
        val report = CommandRunner(driver, gate).execute(CommandIntent.CleanCache(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("click-clean", report.errorStepId)
        // B3-237 收口（对齐上游「异常即释放」）：失败路径同样释放互斥——
        // 异常后重试不再 MUTEX_BUSY（原护栏语义废除）。
        assertTrue("失败路径互斥已由 cleanup 释放", !guard.held)
    }

    @Test
    fun `237暂停拦截-L2零副作用`() {
        val driver = world()
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.CleanCache(), registry())

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertTrue("暂停路径未占用互斥", !guard.held)
    }

    @Test
    fun `237干跑-零动作`() {
        val driver = world()
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.CleanCache(), registry())
        assertEquals(0, driver.actionLog.size)
        assertEquals(RiskLevel.L2, dry.riskLevel)
        assertTrue(!guard.held)
    }
}
