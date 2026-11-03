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
import cn.worktool.open.feature.executor.fixture.ForwardPickerFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MessageMenuFixture
import cn.worktool.open.feature.executor.fixture.MultiSelectFixture
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
 * 205/227/228 转发族执行器场景（docs/10 B2，L1）：
 * 成功 / 接收人不可见（failList）/ 模式按钮分流（逐条 vs 合并）/ 暂停拦截 / 干跑。
 */
class RelayExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingBatchSink()
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    private val quotedSender = "甲仑"
    private val quotedContent = "美团"

    /** 205 世界：源群含甲仑发的「美团」；转发面板含接收人「仑哥」。 */
    private fun relayWorld(receivers: List<String> = listOf("仑哥")): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("美团小程序转发群", "其他群"))
        val chat = RichChatFixture(title = "美团小程序转发群", bubbles = listOf(quotedSender to quotedContent))
        val menu = MessageMenuFixture(items = setOf("转发"))
        val picker = ForwardPickerFixture(receivers = receivers)
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("美团小程序转发群")) { push(chat.screen) }
            chat.bubbleNodes.forEach { bubble -> onClick(chat.screen, bubble) { push(menu.screen) } }
            menu.itemNodes["转发"]?.let { relay -> onClick(menu.screen, relay) { push(picker.screen) } }
            picker.receiverNodes.values.forEach { r ->
                onClick(picker.screen, r) { /* 勾选接收人（停留面板） */ }
            }
            onClick(picker.screen, picker.confirmNode!!) { pop() }
        }
    }

    private fun relayIntent(receivers: List<String> = listOf("仑哥")) = CommandIntent.RelayMessage(
        sourceRooms = listOf("美团小程序转发群"),
        quotedSender = quotedSender,
        quotedContent = quotedContent,
        quotedTextType = 7,
        receivers = receivers,
        extraText = "附加留言",
    )

    @Test
    fun `205成功-长按转发选择接收人确认`() {
        val driver = relayWorld()
        val registry = ExecutorRegistry.of(RelayMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(relayIntent(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        val outcome = sink.batches.single().second.single()
        assertEquals("美团小程序转发群→仑哥", outcome.target)
        assertTrue(outcome.succeeded)
    }

    @Test
    fun `205负例-接收人不可见记败`() {
        val driver = relayWorld(receivers = emptyList())
        val registry = ExecutorRegistry.of(RelayMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(relayIntent(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, outcome.errorCode)
    }

    @Test
    fun `205暂停拦截-零副作用`() {
        val driver = relayWorld()
        val registry = ExecutorRegistry.of(RelayMessageExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(relayIntent(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    @Test
    fun `205干跑-零动作零矩阵`() {
        val driver = relayWorld()
        val registry = ExecutorRegistry.of(RelayMessageExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(relayIntent(), registry)
        assertEquals(CommandIntent.RelayMessage(listOf("a"), "s", "c", 1, listOf("r")).commandType, dry.commandType)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    // ---- 227/228 ----

    private val relayMessages = listOf(
        CommandIntent.RelayItem("第一条", 1),
        CommandIntent.RelayItem("第二条", 1),
    )

    /** 227/228 世界：源群 2 条消息；多选面板 + 模式按钮 + 转发面板。 */
    private fun relayBatchWorld(): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("源群A", "其他群"))
        val chat = RichChatFixture(
            title = "源群A",
            bubbles = listOf("张三" to "第一条", "李四" to "第二条"),
        )
        val menu = MessageMenuFixture(items = setOf("多选"))
        val multi = MultiSelectFixture(messages = listOf("第一条", "第二条"))
        val picker = ForwardPickerFixture(receivers = listOf("仑哥"))
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("源群A")) { push(chat.screen) }
            chat.bubbleNodes.forEach { bubble -> onClick(chat.screen, bubble) { push(menu.screen) } }
            menu.itemNodes["多选"]?.let { item -> onClick(menu.screen, item) { push(multi.screen) } }
            multi.messageNodes.values.forEach { m -> onClick(multi.screen, m) { /* 勾选 */ } }
            multi.modeNodes.values.forEach { mode ->
                onClick(multi.screen, mode) { push(picker.screen) }
            }
            picker.receiverNodes.values.forEach { r -> onClick(picker.screen, r) { } }
            onClick(picker.screen, picker.confirmNode!!) { pop() }
        }
    }

    @Test
    fun `227成功-多选逐条转发`() {
        val driver = relayBatchWorld()
        val registry = ExecutorRegistry.of(RelayMultiMessageExecutor(sink, timeouts = timeouts))
        val intent = CommandIntent.RelayMultiMessage(
            sourceRooms = listOf("源群A"),
            messages = relayMessages,
            receivers = listOf("仑哥"),
        )
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `228成功-多选合并转发（模式按钮分流）`() {
        val driver = relayBatchWorld()
        val registry = ExecutorRegistry.of(RelayMergeMessageExecutor(sink, timeouts = timeouts))
        val intent = CommandIntent.RelayMergeMessage(
            sourceRooms = listOf("源群A"),
            messages = relayMessages,
            receivers = listOf("仑哥"),
        )
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `227负例-模式按钮缺失记败`() {
        val home = HomeFixture(conversations = listOf("源群A"))
        val chat = RichChatFixture(title = "源群A", bubbles = listOf("张三" to "第一条", "李四" to "第二条"))
        val menu = MessageMenuFixture(items = setOf("多选"))
        // 多选面板缺模式按钮
        val multi = MultiSelectFixture(messages = listOf("第一条", "第二条"), modes = emptySet())
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("源群A")) { push(chat.screen) }
            chat.bubbleNodes.forEach { bubble -> onClick(chat.screen, bubble) { push(menu.screen) } }
            menu.itemNodes["多选"]?.let { item -> onClick(menu.screen, item) { push(multi.screen) } }
            multi.messageNodes.values.forEach { m -> onClick(multi.screen, m) { } }
            onClick(multi.screen, multi.confirmNode) { pop() }
        }
        val registry = ExecutorRegistry.of(RelayMultiMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RelayMultiMessage(listOf("源群A"), relayMessages, listOf("仑哥")),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertTrue(!sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `227暂停拦截-零副作用`() {
        val driver = relayBatchWorld()
        val registry = ExecutorRegistry.of(RelayMultiMessageExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RelayMultiMessage(listOf("源群A"), relayMessages, listOf("仑哥")),
            registry,
        )
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }

    @Test
    fun `228负例-接收人不可见记败`() {
        val home = HomeFixture(conversations = listOf("源群A"))
        val chat = RichChatFixture(title = "源群A", bubbles = listOf("张三" to "第一条"))
        val menu = MessageMenuFixture(items = setOf("多选"))
        val multi = MultiSelectFixture(messages = listOf("第一条"))
        val picker = ForwardPickerFixture(receivers = emptyList())
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("源群A")) { push(chat.screen) }
            chat.bubbleNodes.forEach { bubble -> onClick(chat.screen, bubble) { push(menu.screen) } }
            menu.itemNodes["多选"]?.let { item -> onClick(menu.screen, item) { push(multi.screen) } }
            multi.messageNodes.values.forEach { m -> onClick(multi.screen, m) { } }
            multi.modeNodes.values.forEach { mode -> onClick(multi.screen, mode) { push(picker.screen) } }
        }
        val registry = ExecutorRegistry.of(RelayMergeMessageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.RelayMergeMessage(listOf("源群A"), relayMessages, listOf("陌生人")),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertTrue(!sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `228干跑-零动作`() {
        val driver = relayBatchWorld()
        val registry = ExecutorRegistry.of(RelayMergeMessageExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.RelayMergeMessage(listOf("源群A"), relayMessages, listOf("仑哥")),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
