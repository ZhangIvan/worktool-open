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
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.RecommendFormFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 235 点击聊天推荐条执行器场景（docs/10 B3，L1；四形态）：
 * 发起收款成功 / 商品图册选品 / 推荐条缺失 / 暂停拦截 / 干跑。
 */
class ClickRecommendItemExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val sink = RecordingBatchSink()

    /** [recommendItems] 聊天页可见推荐条；[products] 图册商品。 */
    private fun world(
        recommendItems: List<String> = listOf("发起收款", "企业名片"),
        products: List<String> = emptyList(),
    ): Triple<FakeUiDriver, RecommendFormFixture, ChatFixture> {
        val home = HomeFixture(conversations = listOf("Dory", "其他群"))
        val chat = ChatFixture(title = "Dory")
        val form = RecommendFormFixture(products = products)
        recommendItems.forEach { chat.screen.root.add(fakeNode(text = it, cls = TEXT_VIEW, clickable = true)) }
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("Dory")) { push(chat.screen) }
            recommendItems.forEach { item ->
                chat.screen.root.children.first { it.textValue == item }.let { node ->
                    onClick(chat.screen, node) { push(form.screen) }
                }
            }
            form.collectConfirm?.let { onClick(form.screen, it) { pop() } }
            form.productNodes.values.forEach { p -> onClick(form.screen, p) { } }
            onClick(form.screen, form.sendNode) { pop() }
        }
        return Triple(driver, form, chat)
    }

    @Test
    fun `235成功-发起收款输金额`() {
        val (driver, form, _) = world()
        val registry = ExecutorRegistry.of(ClickRecommendItemExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.ClickRecommendItem(listOf("Dory"), "发起收款", amount = "10.5"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals("10.5", form.amountInput?.textValue)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `235成功-商品图册选品发送`() {
        val (driver, _, _) = world(recommendItems = listOf("商品图册"), products = listOf("商品A"))
        val registry = ExecutorRegistry.of(ClickRecommendItemExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.ClickRecommendItem(listOf("Dory"), "商品图册", productName = "商品A"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `235负例-推荐条缺失`() {
        val (driver, _, _) = world(recommendItems = emptyList())
        val registry = ExecutorRegistry.of(ClickRecommendItemExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.ClickRecommendItem(listOf("Dory"), "推荐客服"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, outcome.errorCode)
        assertTrue(outcome.reason.orEmpty().contains("推荐条"))
    }

    @Test
    fun `235暂停拦截-L1零副作用`() {
        val (driver, form, _) = world()
        val registry = ExecutorRegistry.of(ClickRecommendItemExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.ClickRecommendItem(listOf("Dory"), "发起收款", amount = "10"),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(null, form.amountInput?.textValue)
        assertEquals(0, sink.batches.size)
    }

    @Test
    fun `235干跑-零动作`() {
        val (driver, _, _) = world()
        val registry = ExecutorRegistry.of(ClickRecommendItemExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.ClickRecommendItem(listOf("Dory"), "发起收款", amount = "10"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
