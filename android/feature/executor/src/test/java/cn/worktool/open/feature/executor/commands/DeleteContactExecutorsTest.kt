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
import cn.worktool.open.feature.executor.fixture.ContactDetailFixture
import cn.worktool.open.feature.executor.fixture.ContactsFixture
import cn.worktool.open.feature.executor.fixture.DeleteFlowFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MyCustomersFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 234/236 删除联系人族执行器场景（docs/10 B2，**L2**）：
 * 234 成功（不可逆+校验移除）/ 好友不存在 / 确认弹窗缺失 /
 * 236 部分成功（failList 不压平）/ 全成功 / L2 暂停拦截 / 干跑。
 */
class DeleteContactExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val sink = RecordingBatchSink()

    // ---- 234 ----

    /** 世界：通讯录可见「小白」→ 详情 → 菜单 → 删除确认。 */
    private fun deleteWorld(
        withContact: Boolean = true,
        withConfirmDialog: Boolean = true,
    ): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        val contactNode = if (withContact) {
            fakeNode(text = "小白", cls = TEXT_VIEW, clickable = true).also { contacts.screen.root.add(it) }
        } else {
            null
        }
        val detail = ContactDetailFixture(name = "小白", withMore = true)
        val deleteFlow = DeleteFlowFixture(withConfirm = withConfirmDialog)
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            contactNode?.let { node ->
                onClick(contacts.screen, node) { push(detail.screen) }
            }
            detail.moreNode?.let { more -> onClick(detail.screen, more) { push(deleteFlow.menuScreen) } }
            deleteFlow.deleteMenuNode?.let { menu ->
                onClick(deleteFlow.menuScreen, menu) { push(deleteFlow.confirmScreen) }
            }
            deleteFlow.confirmNode?.let { confirm ->
                onClick(deleteFlow.confirmScreen, confirm) {
                    contactNode?.detachFromParent()
                    pop() // confirm → menu
                    pop() // menu → detail
                    pop() // detail → contacts（校验步返回首页途中即校验）
                }
            }
        }
    }

    private val deleteIntent = CommandIntent.DeleteContact(contactName = "小白")

    @Test
    fun `234成功-删除并校验移除`() {
        val driver = deleteWorld()
        val registry = ExecutorRegistry.of(DeleteContactExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(deleteIntent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L2, report.riskLevel)
        assertEquals("verify-removed", report.executedSteps.last().id)
    }

    @Test
    fun `234负例-好友不存在`() {
        val driver = deleteWorld(withContact = false)
        val registry = ExecutorRegistry.of(DeleteContactExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(deleteIntent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-contact-detail", report.errorStepId)
    }

    @Test
    fun `234负例-确认弹窗缺失`() {
        val driver = deleteWorld(withConfirmDialog = false)
        val registry = ExecutorRegistry.of(DeleteContactExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(deleteIntent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("confirm-delete", report.errorStepId)
    }

    @Test
    fun `234暂停拦截-L2零副作用`() {
        val driver = deleteWorld()
        val registry = ExecutorRegistry.of(DeleteContactExecutor(timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(deleteIntent, registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }

    @Test
    fun `234干跑-L2无需审批即返回计划`() {
        val driver = deleteWorld()
        val registry = ExecutorRegistry.of(DeleteContactExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(deleteIntent, registry)
        assertEquals(RiskLevel.L2, dry.riskLevel)
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }

    // ---- 236 ----

    private fun singleDirectionalWorld(names: List<String>): Triple<FakeUiDriver, ContactsFixture, MyCustomersFixture> {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        val customers = MyCustomersFixture(names = names)
        val deleteFlow = DeleteFlowFixture()
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            customers.entryNode?.let {
                contacts.screen.root.add(it)
                onClick(contacts.screen, it) { push(customers.screen) }
            }
            customers.listContainer?.children?.forEach { item ->
                onClick(customers.screen, item) { /* 勾选 */ }
            }
            customers.deleteButton?.let { del ->
                onClick(customers.screen, del) { push(deleteFlow.menuScreen) }
            }
            deleteFlow.deleteMenuNode?.let { menu ->
                onClick(deleteFlow.menuScreen, menu) { push(deleteFlow.confirmScreen) }
            }
            deleteFlow.confirmNode?.let { confirm ->
                onClick(deleteFlow.confirmScreen, confirm) { pop() }
            }
        }
        return Triple(driver, contacts, customers)
    }

    @Test
    fun `236成功-批量删除全命中`() {
        val (driver, _, _) = singleDirectionalWorld(listOf("Dory", "麻球哇"))
        val registry = ExecutorRegistry.of(DeleteSingleDirectionalContactExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.DeleteSingleDirectionalContact(listOf("Dory", "麻球哇")),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L2, report.riskLevel)
        assertEquals(2, sink.batches.single().second.size)
        assertTrue(sink.batches.single().second.all { it.succeeded })
    }

    @Test
    fun `236部分成功-未匹配目标进failList不中断`() {
        val (driver, _, _) = singleDirectionalWorld(listOf("Dory"))
        val registry = ExecutorRegistry.of(DeleteSingleDirectionalContactExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.DeleteSingleDirectionalContact(listOf("Dory", "马铃薯小鬼")),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcomes = sink.batches.single().second
        assertEquals(2, outcomes.size)
        assertTrue(outcomes.first { it.target == "Dory" }.succeeded)
        assertTrue(!outcomes.first { it.target == "马铃薯小鬼" }.succeeded)
    }

    @Test
    fun `236暂停拦截-L2零副作用`() {
        val (driver, _, _) = singleDirectionalWorld(listOf("Dory"))
        val registry = ExecutorRegistry.of(DeleteSingleDirectionalContactExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.DeleteSingleDirectionalContact(listOf("Dory")),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    @Test
    fun `236干跑-零动作`() {
        val (driver, _, _) = singleDirectionalWorld(listOf("Dory"))
        val registry = ExecutorRegistry.of(DeleteSingleDirectionalContactExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.DeleteSingleDirectionalContact(listOf("Dory")),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }
}
