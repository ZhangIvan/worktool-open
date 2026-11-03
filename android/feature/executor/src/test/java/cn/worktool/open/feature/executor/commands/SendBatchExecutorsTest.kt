/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
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
 * 229/230/238 批量发送族执行器场景（docs/10 B2，L1）：
 * 229 逐条矩阵（消息级失败继续）/ 230 合并一卡 / 238 二维矩阵（单条失败继续、
 * 进房失败全格）/ 暂停拦截 / 干跑。
 */
class SendBatchExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingBatchSink()
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    /** 多会话世界：A/B 两会话可进房（B 可选缺失）。 */
    private fun world(conversations: List<String> = listOf("A群", "B群")): Pair<FakeUiDriver, Map<String, ChatFixture>> {
        val home = HomeFixture(conversations = conversations)
        val chats = conversations.associateWith { ChatFixture(title = it) }
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            conversations.forEach { name ->
                onClick(home.screen, home.conversationNodes.getValue(name)) { push(chats.getValue(name).screen) }
                onClick(chats.getValue(name).screen, chats.getValue(name).sendNode!!) {
                    chats.getValue(name).commitInputAsMessage()
                }
            }
        }
        return driver to chats
    }

    private val twoTexts = listOf(
        CommandIntent.OutboundMessage(textType = 1, content = "第一条文本"),
        CommandIntent.OutboundMessage(textType = 1, content = "第二条文本"),
    )

    // ---- 229 ----

    @Test
    fun `229成功-两接收人各收两条有序文本`() {
        val (driver, chats) = world()
        val registry = ExecutorRegistry.of(SendMultiMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendMultiMessage(twoTexts, listOf("A群", "B群")),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        val outcomes = sink.batches.single().second
        assertEquals("2 接收人 × 2 消息 = 4 格", 4, outcomes.size)
        assertTrue(outcomes.all { it.succeeded })
        assertEquals(listOf("第一条文本", "第二条文本"), chats.getValue("A群").messageTexts())
    }

    @Test
    fun `229负例-文件类消息按格记败且文本格继续`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendMultiMessageExecutor(sink, timeouts = timeouts))
        val mixed = listOf(
            twoTexts[0],
            CommandIntent.OutboundMessage(textType = 9, content = null, objectName = "手册.pdf", fileUrl = "https://x/f.pdf", fileType = "file"),
        )
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendMultiMessage(mixed, listOf("A群")),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcomes = sink.batches.single().second
        assertEquals(2, outcomes.size)
        assertTrue("文本格成功", outcomes[0].succeeded)
        assertTrue("文件格记败（fail-closed）", !outcomes[1].succeeded)
        assertEquals(ExecutorErrorCodes.PAYLOAD_INVALID, outcomes[1].errorCode)
    }

    @Test
    fun `229干跑-零动作零矩阵`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendMultiMessageExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.SendMultiMessage(twoTexts, listOf("A群")),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }

    // ---- 230 ----

    @Test
    fun `230成功-合并为一卡发送`() {
        val (driver, chats) = world()
        val registry = ExecutorRegistry.of(SendMergeMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendMergeMessage(twoTexts, listOf("A群")),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("接收侧 1 条合并消息", 1, chats.getValue("A群").messageTexts().size)
        val merged = chats.getValue("A群").messageTexts().single().orEmpty()
        assertTrue(merged.contains("第一条文本") && merged.contains("第二条文本"))
    }

    @Test
    fun `230负例-合并载荷无可发送文本`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendMergeMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendMergeMessage(
                listOf(CommandIntent.OutboundMessage(textType = 9, content = null, objectName = "手册.pdf")),
                listOf("A群"),
            ),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertTrue(!sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `230干跑-零动作`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendMergeMessageExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.SendMergeMessage(twoTexts, listOf("A群")),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue(dry.paramsSummary.contains("mode=合并"))
    }

    @Test
    fun `230暂停拦截-零副作用`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendMergeMessageExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendMergeMessage(twoTexts, listOf("A群")),
            registry,
        )
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    // ---- 238 ----

    @Test
    fun `238成功-单目标三条有序文本矩阵`() {
        val (driver, chats) = world(conversations = listOf("WT28号测试群"))
        val registry = ExecutorRegistry.of(SendBatchTextExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendBatchText(
                targets = listOf("WT28号测试群"),
                texts = listOf("第一条", "第二条", "第三条"),
                atList = listOf("张三"),
            ),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals(
            listOf("@张三 第一条", "@张三 第二条", "@张三 第三条"),
            chats.getValue("WT28号测试群").messageTexts(),
        )
        assertEquals("1 目标 × 3 文本 = 3 格", 3, sink.batches.single().second.size)
    }

    @Test
    fun `238负例-进房失败目标全格记败不中断下一目标`() {
        val (driver, _) = world(conversations = listOf("A群")) // B群不在列表
        val registry = ExecutorRegistry.of(SendBatchTextExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.SendBatchText(targets = listOf("A群", "B群"), texts = listOf("t1", "t2")),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcomes = sink.batches.single().second
        assertEquals("A 两格成功 + B 两格进房失败", 4, outcomes.size)
        assertTrue(outcomes.filter { it.target == "A群" }.all { it.succeeded })
        assertTrue(outcomes.filter { it.target == "B群" }.all { !it.succeeded && it.errorCode == ExecutorErrorCodes.NODE_NOT_FOUND })
    }

    @Test
    fun `238干跑-零动作`() {
        val (driver, _) = world()
        val registry = ExecutorRegistry.of(SendBatchTextExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.SendBatchText(listOf("A群"), listOf("t1", "t2")),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue(dry.paramsSummary.contains("texts=2"))
    }
}
