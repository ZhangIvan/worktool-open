/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeRemoteFiles
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingBatchSink
import cn.worktool.open.feature.executor.fixture.ChatFixture
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
 * 210/211 卡片推送族执行器场景（docs/10 §B B4；自拟 schema）：
 * 成功（210 小程序卡片 / 211 文档卡片）/ 211 厂商废弃注记（等价实现保通道）/
 * 暂停拦截 / 干跑。
 */
class PushCardExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val sink = RecordingBatchSink()
    private val remote = FakeRemoteFiles(reachable = emptySet())

    private fun world(): Pair<FakeUiDriver, ChatFixture> {
        val home = HomeFixture(conversations = listOf("测试群007"))
        val chat = ChatFixture(title = "测试群007")
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("测试群007")) { push(chat.screen) }
            onClick(chat.screen, chat.sendNode!!) { chat.commitInputAsMessage() }
        }
        return driver to chat
    }

    @Test
    fun `210成功-小程序卡片上屏`() {
        val (driver, chat) = world()
        val registry = ExecutorRegistry.of(PushMicroprogramExecutor(remote, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroprogram(
                targets = listOf("测试群007"),
                title = "点餐小程序",
                description = "今天吃什么",
                pageUrl = "https://m.example.com/app",
            ),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertTrue(chat.messageTexts().any { it?.contains("https://m.example.com/app") == true })
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `211成功-文档卡片上屏（厂商已废弃·等价实现保通道）`() {
        val (driver, chat) = world()
        val registry = ExecutorRegistry.of(PushOfficeExecutor(remote, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushOffice(
                targets = listOf("测试群007"),
                title = "交付手册",
                docUrl = "https://doc.example.com/handbook",
            ),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertTrue(chat.messageTexts().any { it?.contains("交付手册") == true })
    }

    @Test
    fun `210负例-会话不存在记败`() {
        val home = HomeFixture(conversations = listOf("其他群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(PushMicroprogramExecutor(remote, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroprogram(listOf("测试群007"), "点餐小程序", pageUrl = "https://m.example.com/app"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
    }

    @Test
    fun `210暂停拦截-零副作用`() {
        val (driver, chat) = world()
        val registry = ExecutorRegistry.of(PushMicroprogramExecutor(remote, sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroprogram(listOf("测试群007"), "点餐小程序", pageUrl = "https://m.example.com/app"),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue("零消息", chat.messageTexts().isEmpty())
    }

    @Test
    fun `211负例-目标会话缺失记败`() {
        val home = HomeFixture(conversations = listOf("其他群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(PushOfficeExecutor(remote, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushOffice(listOf("测试群007"), "交付手册", docUrl = "https://doc.example.com/handbook"),
            registry,
        )
        assertEquals(ExecutionStatus.FAILED, report.status)
        assertTrue(!sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `211干跑-零动作`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(PushOfficeExecutor(remote, sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.PushOffice(listOf("测试群007"), "交付手册", docUrl = "https://doc.example.com/handbook"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
