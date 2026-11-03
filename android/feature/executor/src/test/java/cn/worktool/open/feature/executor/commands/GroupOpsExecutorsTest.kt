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
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fixture.GroupDetailFixture
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
 * 219/244/214 群运营族执行器场景（docs/10 B3）：
 * 219 解散成功/确认弹窗缺失/L2 暂停/干跑；244 退出成功/群主拦截；
 * 214 读群信息推送/已退群失败。
 */
class GroupOpsExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()

    /** 群设置世界：[dismissEntry]/[exitEntry] 控制入口可见性。 */
    private fun settingsWorld(
        dismissEntry: Boolean = false,
        exitEntry: Boolean = false,
        confirmDialog: Boolean = true,
        groupExists: Boolean = true,
        ownerBlock: Boolean = false,
    ): FakeUiDriver {
        val conversations = if (groupExists) listOf("测试群01", "其他群") else listOf("其他群")
        val home = HomeFixture(conversations = conversations)
        val chat = ChatFixture(title = "测试群01")
        val settings = GroupSettingsFixture(groupName = "测试群01", members = listOf("张三", "李四"))
        if (dismissEntry) {
            settings.screen.root.add(
                cn.worktool.open.feature.executor.fake.fakeNode(text = "解散群聊", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true),
            )
        }
        if (exitEntry) {
            settings.screen.root.add(
                cn.worktool.open.feature.executor.fake.fakeNode(text = "退出群聊", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true),
            )
        }
        val dialog = if (confirmDialog) {
            FakeScreen(
                name = "dismiss-confirm",
                root = cn.worktool.open.feature.executor.fake.fakeNode(cls = cn.worktool.open.feature.executor.fake.VIEW_GROUP) {
                    add(cn.worktool.open.feature.executor.fake.fakeNode(text = "确定", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true))
                    if (ownerBlock) add(cn.worktool.open.feature.executor.fake.fakeNode(text = "请转让群主后退出", cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW))
                },
            )
        } else {
            null
        }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            if (groupExists) {
                onClick(home.screen, home.conversationNodes.getValue("测试群01")) { push(chat.screen) }
                onClick(chat.screen, chat.titleNode) { push(settings.screen) }
            }
            settings.screen.root.children
                .filter { it.textValue == "解散群聊" || it.textValue == "退出群聊" }
                .forEach { entry -> onClick(settings.screen, entry) { dialog?.let { push(it) } } }
            dialog?.root?.children?.firstOrNull()?.let { confirm ->
                onClick(dialog, confirm) {
                    home.conversationNodes["测试群01"]?.detachFromParent()
                    pop() // dialog → settings
                    pop() // settings → chat
                    pop() // chat → home
                }
            }
        }
    }

    // ---- 219 ----

    @Test
    fun `219成功-群主解散并校验消失`() {
        val driver = settingsWorld(dismissEntry = true)
        val registry = ExecutorRegistry.of(DismissGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.DismissGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L2, report.riskLevel)
        assertEquals("verify-dismissed", report.executedSteps.last().id)
    }

    @Test
    fun `219负例-解散入口缺失（非群主）`() {
        val driver = settingsWorld(dismissEntry = false)
        val registry = ExecutorRegistry.of(DismissGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.DismissGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-dismiss", report.errorStepId)
    }

    @Test
    fun `219暂停拦截-L2零副作用`() {
        val driver = settingsWorld(dismissEntry = true)
        val registry = ExecutorRegistry.of(DismissGroupExecutor(timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.DismissGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }

    @Test
    fun `219干跑-零动作`() {
        val driver = settingsWorld(dismissEntry = true)
        val registry = ExecutorRegistry.of(DismissGroupExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.DismissGroup("测试群01"), registry)
        assertEquals(RiskLevel.L2, dry.riskLevel)
        assertEquals(0, driver.actionLog.size)
    }

    // ---- 244 ----

    @Test
    fun `244成功-普通成员退出并清会话`() {
        val driver = settingsWorld(exitEntry = true)
        val registry = ExecutorRegistry.of(ExitGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ExitGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
    }

    @Test
    fun `244负例-群主身份被拦截不退出`() {
        val driver = settingsWorld(exitEntry = true, ownerBlock = true)
        val registry = ExecutorRegistry.of(ExitGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ExitGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ILLEGAL_OPERATION, report.errorCode)
        assertEquals("confirm-exit", report.errorStepId)
        assertTrue(report.errorReason.orEmpty().contains("群主"))
    }

    @Test
    fun `244负例-确认弹窗缺失`() {
        val driver = settingsWorld(exitEntry = true, confirmDialog = false)
        val registry = ExecutorRegistry.of(ExitGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ExitGroup("测试群01"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("confirm-exit", report.errorStepId)
        assertTrue(report.errorReason.orEmpty().contains("未确认退出"))
    }

    @Test
    fun `244暂停拦截-零副作用`() {
        val driver = settingsWorld(exitEntry = true)
        val registry = ExecutorRegistry.of(ExitGroupExecutor(timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(CommandIntent.ExitGroup("测试群01"), registry)
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }

    // ---- 214 ----

    @Test
    fun `214成功-读群信息并推送501形态`() {
        val home = HomeFixture(conversations = listOf("技术交流群", "其他群"))
        val chat = ChatFixture(title = "技术交流群")
        val detail = GroupDetailFixture(
            groupName = "技术交流群",
            owner = "群主甲",
            announcement = "周五例会",
            members = listOf("张三", "李四", "王五"),
        )
        val settings = GroupSettingsFixture(groupName = "技术交流群", members = listOf("张三"))
        // 群设置页挂扩展读取位（群名标题/群主/公告/成员列表）
        detail.titleNode.let { settings.screen.root.add(it) }
        detail.ownerNode.let { settings.screen.root.add(it) }
        detail.announcementNode.let { settings.screen.root.add(it) }
        detail.memberContainer.let { settings.screen.root.add(it) }
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("技术交流群")) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
        }
        val registry = ExecutorRegistry.of(ShowGroupInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ShowGroupInfo("技术交流群"), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        val (_, payload) = structured.results.single()
        assertEquals("技术交流群", payload["groupName"]!!.toString().trim('"'))
        assertEquals("群主甲", payload["groupOwner"]!!.toString().trim('"'))
        assertTrue(payload["memberCount"].toString().contains("3"))
    }

    @Test
    fun `214负例-群不存在`() {
        val driver = settingsWorld(groupExists = false)
        val registry = ExecutorRegistry.of(ShowGroupInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ShowGroupInfo("测试群01"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `214干跑-零动作零推送`() {
        val driver = settingsWorld()
        val registry = ExecutorRegistry.of(ShowGroupInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.ShowGroupInfo("测试群01"), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
