/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeCurrentCorp
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingBatchSink
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.CorpDrawerFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.PlusPanelFixture
import cn.worktool.open.feature.executor.fixture.TodoFormFixture
import cn.worktool.open.feature.executor.fixture.ClockPageFixture
import cn.worktool.open.feature.executor.fixture.WorkbenchFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 221/222/223 效率族执行器场景（docs/10 B3）：
 * 221 待办成功/目标失败矩阵/干跑；222 打卡成功/不在范围；223 切换成功/
 * 已在目标企业/企业不存在。
 */
class EfficiencyExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val sink = RecordingBatchSink()

    // ---- 221 ----

    private fun todoWorld(conversations: List<String> = listOf("张三")): Pair<FakeUiDriver, TodoFormFixture> {
        val home = HomeFixture(conversations = conversations)
        val chat = ChatFixture(title = "张三")
        val plus = PlusPanelFixture(entries = setOf("待办"))
        val todo = TodoFormFixture()
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            conversations.forEach { name ->
                onClick(home.screen, home.conversationNodes.getValue(name)) { push(chat.screen) }
            }
            val plusEntry = fakeNode(text = "+", clickable = true)
            chat.screen.root.add(plusEntry)
            onClick(chat.screen, plusEntry) { push(plus.screen) }
            plus.entryNodes["待办"]?.let { onClick(plus.screen, it) { push(todo.screen) } }
            todo.submitNode?.let { onClick(todo.screen, it) { pop() } }
        }
        return driver to todo
    }

    @Test
    fun `221成功-为内部成员添加待办`() {
        val (driver, todo) = todoWorld()
        val registry = ExecutorRegistry.of(AddNeedDealExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.AddNeedDeal(listOf("张三"), "周五前提交周报"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("周五前提交周报", todo.contentInput.textValue)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `221负例-目标非内部成员（会话不可进）记败不中断`() {
        val (driver, _) = todoWorld(conversations = listOf("张三")) // 王五不在
        val registry = ExecutorRegistry.of(AddNeedDealExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.AddNeedDeal(listOf("张三", "王五"), "周五前提交周报"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcomes = sink.batches.single().second
        assertEquals(2, outcomes.size)
        assertTrue(outcomes.first { it.target == "张三" }.succeeded)
        assertTrue(!outcomes.first { it.target == "王五" }.succeeded)
    }

    @Test
    fun `221干跑-零动作`() {
        val (driver, todo) = todoWorld()
        val registry = ExecutorRegistry.of(AddNeedDealExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.AddNeedDeal(listOf("张三"), "t"), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(null, todo.contentInput.textValue)
    }

    // ---- 222 ----

    private fun clockWorld(inRange: Boolean = true, withAction: Boolean = true): Triple<FakeUiDriver, ClockPageFixture, WorkbenchFixture> {
        val home = HomeFixture(conversations = listOf("工作群"))
        val workbench = WorkbenchFixture(withClock = true)
        home.screen.root.children.last().add(workbench.workbenchTab)
        val clockPage = ClockPageFixture(inRange = inRange, withActionButton = withAction)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, workbench.workbenchTab) { push(workbench.screen) }
            workbench.clockEntry?.let { onClick(workbench.screen, it) { push(clockPage.screen) } }
            clockPage.actionButton?.let { btn ->
                onClick(clockPage.screen, btn) { clockPage.screen.root.add(clockPage.resultNode) }
            }
        }
        return Triple(driver, clockPage, workbench)
    }

    @Test
    fun `222成功-范围内点击打卡并识别成功文案`() {
        val (driver, _, _) = clockWorld()
        val registry = ExecutorRegistry.of(ClockInExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ClockIn(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("await-success", report.executedSteps.last().id)
    }

    @Test
    fun `222负例-不在打卡范围`() {
        val (driver, _, _) = clockWorld(inRange = false)
        val registry = ExecutorRegistry.of(ClockInExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ClockIn(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("await-in-range", report.errorStepId)
        assertTrue(report.errorReason.orEmpty().contains("打卡范围"))
    }

    @Test
    fun `222负例-打卡按钮缺失`() {
        val (driver, _, _) = clockWorld(withAction = false)
        val registry = ExecutorRegistry.of(ClockInExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ClockIn(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals("click-clock", report.errorStepId)
    }

    @Test
    fun `222干跑-零动作`() {
        val (driver, _, _) = clockWorld()
        val registry = ExecutorRegistry.of(ClockInExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.ClockIn(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(5, dry.plannedSteps.size)
    }

    // ---- 223 ----

    private fun corpWorld(corps: List<String> = listOf("A公司", "B公司"), current: String = "A公司"): Triple<FakeUiDriver, CorpDrawerFixture, HomeFixture> {
        val home = HomeFixture(conversations = listOf("工作群"))
        val drawer = CorpDrawerFixture(corps = corps, currentCorp = current)
        home.screen.root.add(drawer.drawerEntry!!)
        home.screen.root.add(drawer.homeCorpNode)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, drawer.drawerEntry!!) { push(drawer.drawerScreen) }
            // 点击非当前企业条目 → 首页企业名切换 + 回首页（登出登录语义的界面态变迁）
            corps.filter { it != current }.forEach { target ->
                drawer.corpNodes[target]?.let { node ->
                    onClick(drawer.drawerScreen, node) {
                        drawer.homeCorpNode.textValue = target
                        pop()
                    }
                }
            }
        }
        return Triple(driver, drawer, home)
    }

    @Test
    fun `223成功-切换到目标企业并校验`() {
        val (driver, drawer, home) = corpWorld()
        val corp = FakeCurrentCorp("A公司")
        val registry = ExecutorRegistry.of(SwitchCorpExecutor(corp, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.SwitchCorp("B公司"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals("B公司", drawer.homeCorpNode.textValue)
        assertTrue(report.executedSteps.map { it.id }.containsAll(listOf("open-drawer", "click-target-corp", "verify-switched")))
    }

    @Test
    fun `223成功-已在目标企业零UI动作`() {
        val (driver, _, _) = corpWorld()
        val corp = FakeCurrentCorp("B公司")
        val registry = ExecutorRegistry.of(SwitchCorpExecutor(corp, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.SwitchCorp("B公司"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("零 UI 动作（已在目标企业短路）", 0, driver.actionLog.size)
    }

    @Test
    fun `223负例-目标企业不存在`() {
        val (driver, drawer, home) = corpWorld()
        val corp = FakeCurrentCorp("A公司")
        val registry = ExecutorRegistry.of(SwitchCorpExecutor(corp, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.SwitchCorp("C公司"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("click-target-corp", report.errorStepId)
    }

    @Test
    fun `223滚动兜底-目标企业不在首屏则滚动搜索命中`() {
        // B3-223 收口回归：企业列表滚动查找（首屏未命中 → 容器翻页重查）。
        val home = HomeFixture(conversations = listOf("工作群"))
        val drawer = CorpDrawerFixture(corps = listOf("A公司"), currentCorp = "A公司")
        val hiddenCorp = cn.worktool.open.feature.executor.fake.fakeNode(
            text = "B公司",
            resId = "com.tencent.wework:id/corp_list_item_name",
            cls = "android.widget.TextView",
            clickable = true,
        )
        // 抽屉容器可滚动；目标企业节点经 onScroll 入树（懒加载语义）
        val scrollableList = cn.worktool.open.feature.executor.fake.fakeNode(
            cls = "android.view.ViewGroup", scrollable = true,
        ) { drawer.corpNodes.values.forEach { add(it) } }
        drawer.drawerScreen.root.add(scrollableList)
        home.screen.root.add(drawer.drawerEntry!!)
        home.screen.root.add(drawer.homeCorpNode)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, drawer.drawerEntry!!) { push(drawer.drawerScreen) }
            onClick(drawer.drawerScreen, hiddenCorp) {
                drawer.homeCorpNode.textValue = "B公司"
                pop()
            }
            onScroll(drawer.drawerScreen, scrollableList) { scrollableList.add(hiddenCorp) }
        }
        val corp = FakeCurrentCorp("A公司")
        val registry = ExecutorRegistry.of(SwitchCorpExecutor(corp, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.SwitchCorp("B公司"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue("须经滚动兜底命中", driver.actionLog.any { it.startsWith("scroll(") })
        assertEquals("B公司", drawer.homeCorpNode.textValue)
    }

    @Test
    fun `223暂停拦截-L1零副作用`() {
        val (driver, _, _) = corpWorld()
        val corp = FakeCurrentCorp("A公司")
        val registry = ExecutorRegistry.of(SwitchCorpExecutor(corp, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.SwitchCorp("A公司"), registry)
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }
}
