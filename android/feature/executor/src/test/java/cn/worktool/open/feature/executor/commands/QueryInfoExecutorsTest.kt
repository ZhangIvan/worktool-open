/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingStructuredSink
import cn.worktool.open.feature.executor.fixture.CorpDrawerFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MePageFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 503/505/506 查询信息族执行器场景（docs/10 B2，L0）：
 * 503 我的信息推送 / 505 会话列表（<2 行静默不推送）/ 506 抽屉企业列表 /
 * 节点缺失 / 暂停拦截 / 干跑。
 */
class QueryInfoExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()

    // ---- 503 ----

    private fun meWorld(withMeTab: Boolean = true): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val me = MePageFixture()
        // 「我」Tab 挂到首页底部 Tab 栏（HomeFixture 未内置）
        home.screen.root.children.last().add(me.meTab)
        if (!withMeTab) {
            me.meTab.detachFromParent()
        }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, me.meTab) { push(me.screen) }
        }
    }

    @Test
    fun `503成功-读我页并推送myInfo`() {
        val driver = meWorld()
        val registry = ExecutorRegistry.of(GetMyInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetMyInfo(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        val (type, payload) = structured.results.single()
        assertEquals(CommandIntent.GetMyInfo().commandType, type)
        assertEquals("机器人一号", payload["name"]!!.toString().trim('"'))
        assertEquals("A公司", payload["corporation"]!!.toString().trim('"'))
        assertTrue("空串不缺键（alias 等富字段）", payload.containsKey("alias"))
    }

    @Test
    fun `503负例-我Tab缺失`() {
        val driver = meWorld(withMeTab = false)
        val registry = ExecutorRegistry.of(GetMyInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetMyInfo(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    // ---- 505 ----

    @Test
    fun `505成功-去重采集并推送messageList`() {
        val home = HomeFixture(conversations = listOf("A群", "B群", "A群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetRecentListExecutor(structured))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetRecentList(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertTrue(payload["messageList"].toString().contains("A群"))
    }

    @Test
    fun `505边界-不足2行静默不推送仍成功`() {
        val home = HomeFixture(conversations = listOf("唯一会话"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetRecentListExecutor(structured))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetRecentList(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("静默不推送（上游特殊分支）", 0, structured.results.size)
    }

    // ---- 506 ----

    @Test
    fun `506成功-抽屉企业列表推送corpList`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val drawer = CorpDrawerFixture(corps = listOf("A公司", "B公司"), currentCorp = "A公司")
        home.screen.root.add(drawer.drawerEntry!!)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, drawer.drawerEntry!!) { push(drawer.drawerScreen) }
        }
        val registry = ExecutorRegistry.of(GetCorpListExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetCorpList(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        val corps = payload["corpList"].toString()
        assertTrue(corps.contains("A公司") && corps.contains("B公司"))
    }

    @Test
    fun `506负例-抽屉入口缺失`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetCorpListExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetCorpList(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
    }

    @Test
    fun `505干跑-零动作零推送`() {
        val home = HomeFixture(conversations = listOf("A群", "B群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetRecentListExecutor(structured))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetRecentList(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertEquals(2, dry.plannedSteps.size)
    }

    @Test
    fun `506干跑-零动作零推送`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetCorpListExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetCorpList(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertEquals(2, dry.plannedSteps.size)
    }

    // ---- 横切：暂停 + 干跑（以 503 为代表） ----

    @Test
    fun `暂停拦截-零副作用零推送`() {
        val driver = meWorld()
        val registry = ExecutorRegistry.of(GetMyInfoExecutor(structured, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetMyInfo(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `干跑-零动作零推送`() {
        val driver = meWorld()
        val registry = ExecutorRegistry.of(GetMyInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetMyInfo(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertEquals(2, dry.plannedSteps.size)
    }
}
