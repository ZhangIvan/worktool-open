/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fixture.BoardWebpageFixture
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.GroupSettingsFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 240 添加群看板网页执行器场景（docs/10 B3，L1；仅内部群）：
 * 成功 / 看板入口缺失（外部群族）/ 暂停拦截 / 干跑。
 */
class GroupBoardWebpageExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    private fun world(withBoardEntry: Boolean = true, withSubmit: Boolean = true): Pair<FakeUiDriver, BoardWebpageFixture> {
        val home = HomeFixture(conversations = listOf("测试群01", "其他群"))
        val chat = ChatFixture(title = "测试群01")
        val settings = GroupSettingsFixture(groupName = "测试群01", members = listOf("张三"))
        val boardPage = BoardWebpageFixture(withSubmit = withSubmit)
        if (withBoardEntry) {
            settings.screen.root.add(
                cn.worktool.open.feature.executor.fake.fakeNode(text = "群看板", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true),
            )
        }
        val boardEntryNode = settings.screen.root.children.firstOrNull { it.textValue == "群看板" }
        // 看板管理页（「添加」按钮）
        val manageScreen = FakeScreen(
            name = "board-manage",
            root = cn.worktool.open.feature.executor.fake.fakeNode(cls = cn.worktool.open.feature.executor.fake.VIEW_GROUP) {
                add(cn.worktool.open.feature.executor.fake.fakeNode(text = "添加", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true))
            },
        )
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("测试群01")) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
            boardEntryNode?.let { onClick(settings.screen, it) { push(manageScreen) } }
            manageScreen.root.children.first().let { addBtn ->
                onClick(manageScreen, addBtn) { push(boardPage.screen) }
            }
            boardPage.submitNode?.let { onClick(boardPage.screen, it) { pop() } }
        }
        return driver to boardPage
    }

    private val intent = CommandIntent.AddGroupBoardWebpage(
        groupName = "测试群01",
        url = "https://example.com/board",
    )

    @Test
    fun `240成功-添加网页并提交`() {
        val (driver, boardPage) = world()
        val registry = ExecutorRegistry.of(AddGroupBoardWebpageExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals("https://example.com/board", boardPage.urlInput.textValue)
    }

    @Test
    fun `240负例-看板入口缺失（外部群族）`() {
        val (driver, _) = world(withBoardEntry = false)
        val registry = ExecutorRegistry.of(AddGroupBoardWebpageExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-group-board", report.errorStepId)
        assertTrue(report.errorReason.orEmpty().contains("内部群"))
    }

    @Test
    fun `240暂停拦截-L1零副作用`() {
        val (driver, boardPage) = world()
        val registry = ExecutorRegistry.of(AddGroupBoardWebpageExecutor(timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals("URL 未写入", null, boardPage.urlInput.textValue)
    }

    @Test
    fun `240干跑-零动作`() {
        val (driver, boardPage) = world()
        val registry = ExecutorRegistry.of(AddGroupBoardWebpageExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(intent, registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(null, boardPage.urlInput.textValue)
        assertTrue(dry.plannedSteps.size >= 7)
    }
}
