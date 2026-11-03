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
import cn.worktool.open.feature.executor.fixture.ContactsFixture
import cn.worktool.open.feature.executor.fixture.GroupDetailFixture
import cn.worktool.open.feature.executor.fixture.GroupQrFixture
import cn.worktool.open.feature.executor.fixture.GroupSettingsFixture
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
 * 504/507/508 群·好友查询族执行器场景（docs/10 B2，L0）：
 * 504 主动群二维码（自拟兼容面）/ 504 入口缺失 / 507 我的客户分页全量 /
 * 507 入口缺失 / 508 群列表+逐群详情推送 / 暂停拦截 / 干跑。
 */
class GroupQueryExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()

    // ---- 504 ----

    private fun qrWorld(withQrEntry: Boolean = true): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("测试群01"))
        val chat = ChatFixture(title = "测试群01")
        val settings = GroupSettingsFixture(groupName = "测试群01", members = listOf("张三"))
        val qr = GroupQrFixture(withEntry = withQrEntry)
        qr.qrEntry?.let { settings.screen.root.add(it) }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("测试群01")) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
            qr.qrEntry?.let { entry -> onClick(settings.screen, entry) { push(qr.qrScreen) } }
        }
    }

    @Test
    fun `504成功-读群二维码并推送主动面标记`() {
        val driver = qrWorld()
        val registry = ExecutorRegistry.of(GetGroupQrCodeExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupQrCode(groupName = "测试群01"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        val (type, payload) = structured.results.single()
        assertEquals(CommandIntent.GetGroupQrCode("x").commandType, type)
        assertEquals("504", payload["extraText"]!!.toString().trim('"'))
        assertTrue(payload["qrcode"].toString().contains("qr"))
    }

    @Test
    fun `504负例-群二维码入口缺失`() {
        val driver = qrWorld(withQrEntry = false)
        val registry = ExecutorRegistry.of(GetGroupQrCodeExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupQrCode(groupName = "测试群01"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    // ---- 507 ----

    private fun customersWorld(pages: List<List<String>>): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        val customers = MyCustomersFixture(names = pages.flatten(), pages = pages)
        customers.entryNode?.let { contacts.screen.root.add(it) }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            customers.entryNode?.let { onClick(contacts.screen, it) { push(customers.screen) } }
            customers.listContainer?.let { container ->
                onScroll(customers.screen, container) { customers.appendNextPage() }
            }
        }
    }

    @Test
    fun `507成功-我的客户分页全量推送nameList`() {
        val driver = customersWorld(
            listOf(
                listOf("客户A", "客户B", "客户C"),
                listOf("客户D"),
            ),
        )
        val registry = ExecutorRegistry.of(GetAllFriendInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetAllFriendInfo(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        val names = payload["nameList"].toString()
        assertTrue("分页采集到 D", names.contains("客户D"))
        assertTrue(names.contains("客户A"))
    }

    @Test
    fun `507负例-我的客户入口缺失`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
        }
        val registry = ExecutorRegistry.of(GetAllFriendInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetAllFriendInfo(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-my-customers", report.errorStepId)
    }

    // ---- 508 ----

    @Test
    fun `508成功-群列表逐群详情每群一条推送`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false, withGroupChatEntry = true)
        val groupList = cn.worktool.open.feature.executor.fixture.GroupListFixture(
            pages = listOf(listOf("交付群", "客户群")),
            itemClickable = true,
        )
        // 逐群详情屏：点击群名条目进入 → 读字段 → 返回
        val details = listOf("交付群", "客户群").associateWith { name ->
            GroupDetailFixture(groupName = name, owner = "群主甲", announcement = "公告A", members = listOf("张三", "李四"))
        }
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            onClick(contacts.screen, contacts.groupChatEntry!!) { push(groupList.screen) }
            details.forEach { (name, detail) ->
                val item = groupList.listContainer.children.first { it.textValue == name }
                onClick(groupList.screen, item) { push(detail.screen) }
            }
        }
        val registry = ExecutorRegistry.of(GetAllGroupInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetAllGroupInfo(), registry)

        assertEquals(
            "errorStep=${report.errorStepId} reason=${report.errorReason} pushed=${structured.results.map { it.second["groupName"] }}",
            ExecutionStatus.SUCCEEDED,
            report.status,
        )
        assertEquals("每群独立一条推送", 2, structured.results.size)
        val first = structured.results.first().second
        assertEquals("交付群", first["groupName"]!!.toString().trim('"'))
        assertTrue("payload=$first", first["memberCount"].toString().contains("2"))
    }

    @Test
    fun `507干跑-零动作零推送`() {
        val driver = customersWorld(listOf(listOf("客户A")))
        val registry = ExecutorRegistry.of(GetAllFriendInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetAllFriendInfo(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertTrue(dry.plannedSteps.size > 3)
    }

    @Test
    fun `508负例-群聊列表为空失败`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false, withGroupChatEntry = true)
        val groupList = cn.worktool.open.feature.executor.fixture.GroupListFixture(pages = listOf(emptyList()))
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            onClick(contacts.screen, contacts.groupChatEntry!!) { push(groupList.screen) }
        }
        val registry = ExecutorRegistry.of(GetAllGroupInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.GetAllGroupInfo(), registry)
        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `508干跑-零动作零推送`() {
        val driver = customersWorld(listOf(listOf("客户A")))
        val registry = ExecutorRegistry.of(GetAllGroupInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetAllGroupInfo(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertTrue(dry.plannedSteps.size > 3)
    }

    // ---- 横切 ----

    @Test
    fun `暂停拦截-零副作用零推送`() {
        val driver = qrWorld()
        val registry = ExecutorRegistry.of(GetGroupQrCodeExecutor(structured, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupQrCode(groupName = "测试群01"),
            registry,
        )
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `干跑-零动作`() {
        val driver = qrWorld()
        val registry = ExecutorRegistry.of(GetGroupQrCodeExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.GetGroupQrCode(groupName = "测试群01"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.size >= 5)
    }
}
