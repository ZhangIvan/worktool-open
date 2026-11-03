/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeSessionControl
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.testBindings
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 201 STOP_AND_GO_HOME 执行器场景（docs/10 B1）：成功归位 / 接管被拒 /
 * 首页不可达（节点缺失）/ 暂停拦截 / 干跑。
 */
class StopAndGoHomeExecutorTest {

    private val session = FakeSessionControl()
    private val registry = ExecutorRegistry.defaultFullBatch(testBindings(session = session))

    /** 起点在聊天页（回退一屏 = 消息首页）。 */
    private fun worldInChat(withContactsTab: Boolean = true): Triple<FakeUiDriver, FakePauseGate, FakeClock> {
        val clock = FakeClock()
        val gate = FakePauseGate()
        val home = HomeFixture(
            conversations = listOf("外部群测试群"),
            withContactsTab = withContactsTab,
        )
        val chat = ChatFixture(title = "外部群测试群")
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("外部群测试群")) { push(chat.screen) }
            push(chat.screen)
        }
        return Triple(driver, gate, clock)
    }

    private fun runner(driver: FakeUiDriver, gate: FakePauseGate) = CommandRunner(driver, gate)

    @Test
    fun `成功-请求接管暂停并回退到消息首页`() {
        val (driver, gate, _) = worldInChat()
        val report = runner(driver, gate).execute(CommandIntent.StopAndGoHome(reason = "cloud-201"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(listOf("request-takeover-pause", "go-home", "await-home-ready"), report.executedSteps.map { it.id })
        assertEquals(listOf("cloud-201"), session.requests)
        assertTrue("应有返回动作", driver.actionLog.any { it == "back" })
    }

    @Test
    fun `节点缺失-主界面Tab缺失则归位失败`() {
        val (driver, gate, _) = worldInChat(withContactsTab = false)
        val report = runner(driver, gate).execute(CommandIntent.StopAndGoHome(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("go-home", report.errorStepId)
    }

    @Test
    fun `接管被拒-首步失败不再归位`() {
        session.accept = false
        val (driver, gate, _) = worldInChat()
        val report = runner(driver, gate).execute(CommandIntent.StopAndGoHome(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ILLEGAL_OPERATION, report.errorCode)
        assertEquals("request-takeover-pause", report.errorStepId)
        assertEquals("无返回动作（fail-closed）", 0, driver.actionLog.count { it == "back" })
    }

    @Test
    fun `暂停拦截-执行前已暂停则零副作用PAUSED`() {
        val (driver, gate, _) = worldInChat()
        gate.paused = true
        gate.source = "floating-window"
        val report = runner(driver, gate).execute(CommandIntent.StopAndGoHome(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, report.executedSteps.size)
        assertEquals("接管请求未发出", 0, session.requests.size)
        assertEquals("零界面动作", 0, driver.actionLog.size)
    }

    @Test
    fun `干跑-产出三步报告且零界面动作零接管请求`() {
        val (driver, gate, _) = worldInChat()
        val dry = runner(driver, gate).dryRun(CommandIntent.StopAndGoHome(), registry)

        assertEquals(CommandIntent.StopAndGoHome().commandType, dry.commandType)
        assertEquals(3, dry.plannedSteps.size)
        assertEquals("零接管请求", 0, session.requests.size)
        assertEquals("零界面动作", 0, driver.actionLog.size)
    }
}
