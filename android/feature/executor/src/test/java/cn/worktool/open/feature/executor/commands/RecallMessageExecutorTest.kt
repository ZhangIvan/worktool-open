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
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MessageMenuFixture
import cn.worktool.open.feature.executor.fixture.RichChatFixture
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 226 RECALL_MESSAGE 执行器场景（docs/10 B2，L1）：
 * 成功（撤回提示上屏）/ 消息不存在 / 超撤回窗（菜单无「撤回」项）/ 暂停拦截 / 干跑。
 */
class RecallMessageExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingBatchSink()
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val registry = ExecutorRegistry.of(RecallMessageExecutor(sink, timeouts = timeouts))

    /** [withRecallMenu]=false 模拟超撤回时间窗（长按菜单无「撤回」项）。 */
    private fun world(withMessage: Boolean = true, withRecallMenu: Boolean = true): Triple<FakeUiDriver, RichChatFixture, FakeExecutorNodeHolder> {
        val home = HomeFixture(conversations = listOf("测试群007", "其他群"))
        val chat = RichChatFixture(
            title = "测试群007",
            bubbles = if (withMessage) listOf("机器人" to "你好~") else emptyList(),
        )
        val menu = MessageMenuFixture(items = if (withRecallMenu) setOf("撤回") else emptySet())
        val recallNotice = fakeNode(text = "机器人撤回了一条消息", cls = TEXT_VIEW)
        val holder = FakeExecutorNodeHolder(recallNotice)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("测试群007")) { push(chat.screen) }
            chat.bubbleNodes.forEach { bubble -> onClick(chat.screen, bubble) { push(menu.screen) } }
            menu.itemNodes["撤回"]?.let { recall ->
                onClick(menu.screen, recall) {
                    chat.screen.root.add(recallNotice)
                    pop()
                }
            }
        }
        return Triple(driver, chat, holder)
    }

    private class FakeExecutorNodeHolder(val node: cn.worktool.open.feature.executor.fake.FakeExecutorNode)

    private fun intent() = CommandIntent.RecallMessage(
        targets = listOf("测试群007"),
        originalContent = "你好~",
        textType = 1,
    )

    @Test
    fun `成功-长按撤回且提示上屏`() {
        val (driver, chat, holder) = world()
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertTrue("撤回提示上屏", holder.node.parent != null)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `负例-消息不存在记败`() {
        val (driver, _, _) = world(withMessage = false)
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, outcome.errorCode)
    }

    @Test
    fun `负例-超撤回窗菜单无撤回项`() {
        val (driver, _, _) = world(withRecallMenu = false)
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertTrue(outcome.reason.orEmpty().contains("撤回"))
    }

    @Test
    fun `暂停拦截-零副作用`() {
        val (driver, _, _) = world()
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(intent(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    @Test
    fun `干跑-零动作`() {
        val (driver, _, _) = world()
        val dry = CommandRunner(driver, gate).dryRun(intent(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
