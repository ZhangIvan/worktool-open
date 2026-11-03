/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.Friend
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fixture.AddFriendPageFixture
import cn.worktool.open.feature.executor.fixture.ContactDetailFixture
import cn.worktool.open.feature.executor.fixture.ContactsFixture
import cn.worktool.open.feature.executor.fixture.GroupSettingsFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 213/220 加好友族执行器场景（docs/10 B2，L1）：
 * 213 添加语义（搜索→发送申请）/ 213 搜索无结果 / 213 修改语义（备注）/
 * 220 群内添加 / 220 成员不在群 / 暂停拦截 / 干跑。
 */
class AddFriendExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    // ---- 213 ----

    private fun addWorld(searchResults: List<String> = listOf("13010001000")): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        val addPage = AddFriendPageFixture(results = searchResults)
        // 「添加朋友」入口挂到通讯录页（ContactsFixture 未内置该入口）
        val addFriendEntry = cn.worktool.open.feature.executor.fake.fakeNode(
            text = "添加朋友",
            cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW,
            clickable = true,
        )
        contacts.screen.root.add(addFriendEntry)
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            onClick(contacts.screen, addFriendEntry) { push(addPage.screen) }
        }
    }

    private val addIntent = CommandIntent.AddFriendByPhone(
        phone = "13010001000",
        markName = "备注名",
        leavingMsg = "你好，我是机器人~",
    )

    @Test
    fun `213成功-手机号搜索并发送申请`() {
        val driver = addWorld()
        val registry = ExecutorRegistry.of(AddFriendByPhoneExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(addIntent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals(
            listOf("open-contacts-tab", "open-add-friend", "input-phone", "send-request"),
            report.executedSteps.map { it.id },
        )
    }

    @Test
    fun `213负例-搜索无结果`() {
        val driver = addWorld(searchResults = emptyList())
        val registry = ExecutorRegistry.of(AddFriendByPhoneExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(addIntent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("input-phone", report.errorStepId)
    }

    @Test
    fun `213修改语义-定位联系人写备注`() {
        val home = HomeFixture(conversations = listOf("工作群"))
        val contacts = ContactsFixture(withNewFriendsEntry = false)
        // 通讯录直接可见联系人「小白」+ 详情页备注输入
        val contactNode = cn.worktool.open.feature.executor.fake.fakeNode(
            text = "小白",
            cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW,
            clickable = true,
        )
        contacts.screen.root.add(contactNode)
        val detail = ContactDetailFixture(name = "小白", withMarkInput = true)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
            onClick(contacts.screen, contactNode) { push(detail.screen) }
            detail.markConfirm?.let { onClick(detail.screen, it) { pop() } }
        }
        val registry = ExecutorRegistry.of(AddFriendByPhoneExecutor(timeouts = timeouts))
        val intent = CommandIntent.AddFriendByPhone(
            phone = "13010001000",
            name = "小白",
            markName = "重点客户",
            tagList = listOf("VIP"),
        )
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(listOf("open-contacts-tab", "open-contact-detail", "write-mark", "confirm-mark"), report.executedSteps.map { it.id })
        assertEquals("重点客户", detail.markInput?.textValue)
    }

    @Test
    fun `213干跑-零动作`() {
        val driver = addWorld()
        val registry = ExecutorRegistry.of(AddFriendByPhoneExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(addIntent, registry)
        assertEquals(MessageTypes.ADD_FRIEND_BY_PHONE, dry.commandType)
        assertEquals(0, driver.actionLog.size)
    }

    // ---- 220 ----

    private fun groupWorld(members: List<String> = listOf("张三", "李四")): Triple<FakeUiDriver, ChatFixture, GroupSettingsFixture> {
        val home = HomeFixture(conversations = listOf("外部群10010"))
        val chat = ChatFixture(title = "外部群10010")
        val settings = GroupSettingsFixture(groupName = "外部群10010", members = members)
        val detail = ContactDetailFixture(name = "张三", withAddButton = true)
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("外部群10010")) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
            settings.memberNodes["张三"]?.let { member ->
                onClick(settings.screen, member) { push(detail.screen) }
            }
        }
        return Triple(driver, chat, settings)
    }

    private val groupIntent = CommandIntent.AddFriendByGroup(
        groupName = "外部群10010",
        memberName = "张三",
        leavingMsg = "你好，我是机器人~",
    )

    @Test
    fun `220成功-群内定位成员并发送添加`() {
        val (driver, _, _) = groupWorld()
        val registry = ExecutorRegistry.of(AddFriendByGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(groupIntent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals("click-add-friend", report.executedSteps.last().id)
    }

    @Test
    fun `220负例-成员不在群`() {
        val (driver, _, _) = groupWorld(members = listOf("李四"))
        val registry = ExecutorRegistry.of(AddFriendByGroupExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(groupIntent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-member-detail", report.errorStepId)
    }

    @Test
    fun `220暂停拦截-零副作用`() {
        val (driver, _, _) = groupWorld()
        val registry = ExecutorRegistry.of(AddFriendByGroupExecutor(timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(groupIntent, registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
    }

    @Test
    fun `220干跑-零动作`() {
        val (driver, _, _) = groupWorld()
        val registry = ExecutorRegistry.of(AddFriendByGroupExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(groupIntent, registry)
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.size >= 5)
    }
}
