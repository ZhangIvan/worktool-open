/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingBatchSink
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MessageMenuFixture
import cn.worktool.open.feature.executor.fixture.RichChatFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 204 REPLY_MESSAGE 执行器场景（docs/10 B2）：
 * 成功（引用回复上屏）/ 引用目标缺失（部分成功矩阵）/ 暂停拦截 / 干跑。
 */
class ReplyMessageExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingBatchSink()
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    private val registry = ExecutorRegistry.of(ReplyMessageExecutor(sink, timeouts = timeouts))

    /** 世界：会话「测试群A」含 张三 发的「你好」。 */
    private fun world(
        conversations: List<String> = listOf("测试群A", "其他群"),
        bubbles: List<Pair<String, String>> = listOf("张三" to "你好"),
    ): Triple<FakeUiDriver, RichChatFixture, HomeFixture> {
        val home = HomeFixture(conversations = conversations)
        val chat = RichChatFixture(title = "测试群A", bubbles = bubbles)
        val menu = MessageMenuFixture(items = setOf("回复"))
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            home.conversationNodes["测试群A"]?.let { node ->
                onClick(home.screen, node) { push(chat.screen) }
            }
            chat.bubbleNodes.forEach { bubble ->
                onClick(chat.screen, bubble) { push(menu.screen) }
            }
            menu.itemNodes["回复"]?.let { reply ->
                onClick(menu.screen, reply) { pop() }
            }
            onClick(chat.screen, chat.sendNode) { chat.commitInputAsMessage() }
        }
        return Triple(driver, chat, home)
    }

    private fun intent(targets: List<String> = listOf("测试群A")) = CommandIntent.ReplyMessage(
        targets = targets,
        quotedSender = "张三",
        quotedContent = "你好",
        quotedTextType = 1,
        content = "收到，明天处理",
    )

    @Test
    fun `成功-定位引用消息并回复上屏`() {
        val (driver, chat, _) = world()
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertTrue("回复应上屏", chat.bubbleContents().contains("收到，明天处理"))
        val outcomes = sink.batches.single().second
        assertEquals(1, outcomes.size)
        assertTrue(outcomes.single().succeeded)
        assertTrue(driver.actionLog.any { it.startsWith("longclick(") })
    }

    @Test
    fun `引用目标缺失-该目标记败且不中断后续目标`() {
        // 会话存在但气泡发送人不匹配（李四发的）
        val (driver, chat, _) = world(bubbles = listOf("李四" to "你好"))
        val report = CommandRunner(driver, gate).execute(intent(targets = listOf("测试群A", "测试群A")), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcomes = sink.batches.single().second
        assertEquals("两目标各记一败（矩阵不压平）", 2, outcomes.size)
        assertTrue(outcomes.all { !it.succeeded })
        assertTrue("零输入动作", chat.inputNode?.textValue == null)
    }

    @Test
    fun `会话缺失-进房级失败记败不中断`() {
        val (driver, _, _) = world(conversations = listOf("其他群"))
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertEquals("测试群A", outcome.target)
        assertTrue(!outcome.succeeded)
    }

    @Test
    fun `暂停拦截-执行前已暂停则零副作用`() {
        val (driver, chat, _) = world()
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals("零矩阵外发", 0, sink.batches.size)
        assertTrue("回复未上屏", chat.bubbleContents().none { it?.contains("收到") == true })
    }

    @Test
    fun `干跑-产出计划且零界面动作零矩阵`() {
        val (driver, _, _) = world()
        val dry = CommandRunner(driver, gate).dryRun(intent(), registry)

        assertEquals(CommandIntent.ReplyMessage(listOf("x"), "a", "b", 1, "c").commandType, dry.commandType)
        assertEquals("每目标 7 步 + 末步 1 = 8", 8, dry.plannedSteps.size)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }
}
