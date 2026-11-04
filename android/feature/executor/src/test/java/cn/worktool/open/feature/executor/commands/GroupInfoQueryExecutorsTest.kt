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
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.fixture.CalendarMeetingFixture
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fixture.GroupDetailFixture
import cn.worktool.open.feature.executor.fixture.GroupSettingsFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MeetingDetailFixture
import cn.worktool.open.feature.executor.fixture.WorkbenchFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 512/515/511 群信息·会议查询族执行器场景（docs/10 B3，L0）：
 * 512 成功/已退群失败（GROUP_EXITED）；515 成功/无备注空串/已退群分支仍成功；
 * 511 分页滚动采集+逐会详情推送（详情缺失如实失败）/无会议不推送；512/515 同态对照断言。
 */
class GroupInfoQueryExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()

    /** 群详情世界（挂扩展读取位到群设置页）；[exitedMarker]=已退群提示。 */
    private fun groupWorld(
        groupName: String = "技术交流群",
        remark: String? = "技术交流群-南京",
        exitedMarker: Boolean = false,
    ): FakeUiDriver {
        val listKey = remark ?: groupName // 会话定位键：有备注必须用备注名（通则）
        val home = HomeFixture(conversations = listOf(listKey, "其他群"))
        val chat = ChatFixture(title = listKey)
        val detail = GroupDetailFixture(
            groupName = groupName,
            owner = "群主甲",
            announcement = "（暂无公告）",
            members = listOf("张三", "李四"),
        )
        val settings = GroupSettingsFixture(groupName = groupName, members = listOf("张三"))
        detail.titleLabelNode.let { settings.screen.root.add(it) }
        detail.titleNode.let { settings.screen.root.add(it) }
        remark?.let {
            settings.screen.root.add(
                fakeNode(text = it, resId = "com.tencent.wework:id/group_remark_value", cls = TEXT_VIEW),
            )
        }
        detail.ownerNode.let { settings.screen.root.add(it) }
        detail.announcementNode.let { settings.screen.root.add(it) }
        detail.memberContainer.let { settings.screen.root.add(it) }
        if (exitedMarker) {
            settings.screen.root.add(fakeNode(text = "无法在已退出的群聊中发送消息", cls = TEXT_VIEW))
        }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue(listKey)) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
        }
    }

    @Test
    fun `512成功-读成员列表并推送`() {
        val driver = groupWorld()
        val registry = ExecutorRegistry.of(GetGroupMemberInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupMemberInfo(groupName = "技术交流群-南京"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        val (_, payload) = structured.results.single()
        assertEquals("真实群名（入参可为备注名）", "技术交流群", payload["groupName"]!!.toString().trim('"'))
        assertEquals("备注兜底空串", "技术交流群-南京", payload["groupRemark"]!!.toString().trim('"'))
        assertTrue(payload["memberCount"].toString().contains("2"))
    }

    @Test
    fun `512负例-已退出群失败（对照515同态成功）`() {
        val driver = groupWorld(exitedMarker = true)
        val registry = ExecutorRegistry.of(GetGroupMemberInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupMemberInfo(groupName = "技术交流群-南京"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.GROUP_EXITED, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `512干跑-零动作`() {
        val driver = groupWorld()
        val registry = ExecutorRegistry.of(GetGroupMemberInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.GetGroupMemberInfo(groupName = "技术交流群"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `515成功-轻量读群名备注且不读成员`() {
        val driver = groupWorld()
        val registry = ExecutorRegistry.of(GetGroupNameInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupNameInfo(groupName = "技术交流群-南京"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertEquals("技术交流群", payload["groupName"]!!.toString().trim('"'))
        assertEquals("技术交流群-南京", payload["groupRemark"]!!.toString().trim('"'))
        assertTrue("轻量：不推送成员列表", !payload.containsKey("memberList"))
    }

    @Test
    fun `515成功-无备注群groupRemark空串`() {
        val driver = groupWorld(remark = null)
        val registry = ExecutorRegistry.of(GetGroupNameInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupNameInfo(groupName = "技术交流群"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertEquals("", payload["groupRemark"]!!.toString().trim('"'))
    }

    @Test
    fun `515负例-群不存在`() {
        val home = HomeFixture(conversations = listOf("其他群"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(GetGroupNameInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupNameInfo(groupName = "不存在的群"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
    }

    @Test
    fun `515已退群分支-搜索到已退出群聊仍成功推送群名备注（对照512同态失败）`() {
        // 同一 world：512 见 GROUP_EXITED 失败（上方负例），515 契约=已退群可搜索仍算成功
        val driver = groupWorld(exitedMarker = true)
        val registry = ExecutorRegistry.of(GetGroupNameInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupNameInfo(groupName = "技术交流群-南京"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertEquals("技术交流群", payload["groupName"]!!.toString().trim('"'))
        assertEquals("技术交流群-南京", payload["groupRemark"]!!.toString().trim('"'))
    }

    @Test
    fun `515已退群分支-退群且群名不可读如实失败零推送`() {
        // 群设置页：群名标题节点在场但文本为空 + 退群标记 → 对照上游 201104「未找到群聊」
        val listKey = "技术交流群"
        val home = HomeFixture(conversations = listOf(listKey))
        val chat = ChatFixture(title = listKey)
        val settings = FakeScreen(
            name = "group-settings-exited-blank",
            root = fakeNode(cls = cn.worktool.open.feature.executor.fake.VIEW_GROUP) {
                // 真机校准（2026-11-04）：行标签 hn1 + 值 lm8（sibling 取值）。
                add(fakeNode(text = "群聊名称", resId = "com.tencent.wework:id/hn1", cls = TEXT_VIEW))
                add(fakeNode(text = "", resId = "com.tencent.wework:id/lm8", cls = TEXT_VIEW))
                add(fakeNode(text = "无法在已退出的群聊中发送消息", cls = TEXT_VIEW))
            },
        )
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue(listKey)) { push(chat.screen) }
            onClick(chat.screen, chat.titleNode) { push(settings) }
        }
        val registry = ExecutorRegistry.of(GetGroupNameInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupNameInfo(groupName = listKey),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertTrue("失败留痕=退群分支口径", report.errorReason!!.contains("搜索到已退出群聊"))
        assertEquals(0, structured.results.size)
    }

    // ---- 511 ----

    /**
     * 511 世界（安卓恢复链车道扩展）：会议列表页（分页懒加载容器）+ 每会详情页。
     * [meetings]=首屏；[nextPages]=滚动追加页；[detailFor] 返回 null = 点击后详情
     * 永不出现（NODE_NOT_FOUND 负例，不虚构读取成功）。
     */
    private fun meetingWorld(
        meetings: List<String>,
        nextPages: List<List<String>> = emptyList(),
        detailFor: (String) -> MeetingDetailFixture? = {
            MeetingDetailFixture(
                scheduledTime = "2026-11-05 14:00",
                actualTime = "未开始",
                organizer = "组织者甲",
                screenName = "detail-$it",
            )
        },
    ): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val workbench = WorkbenchFixture(withCalendar = true)
        home.screen.root.children.last().add(workbench.workbenchTab)
        // 日历页：日期格 + 会议列表
        val calendarScreen = FakeScreen(
            name = "calendar",
            root = fakeNode(cls = cn.worktool.open.feature.executor.fake.VIEW_GROUP) {
                add(fakeNode(text = "5日", cls = TEXT_VIEW, clickable = true))
            },
        )
        val meetingPage = CalendarMeetingFixture(
            meetings = meetings,
            pages = (listOf(meetings) + nextPages).takeIf { nextPages.isNotEmpty() },
        )
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, workbench.workbenchTab) { push(workbench.screen) }
            workbench.calendarEntry?.let { onClick(workbench.screen, it) { push(calendarScreen) } }
            calendarScreen.root.children.first { it.textValue == "5日" }.let { cell ->
                onClick(calendarScreen, cell) { push(meetingPage.screen) }
            }
            onScroll(meetingPage.screen, meetingPage.listContainer) { meetingPage.appendNextMeetingPage() }
            (listOf(meetings) + nextPages).flatten().forEach { name ->
                meetingPage.meetingNodes[name]?.let { node ->
                    val detail = detailFor(name)
                    onClick(meetingPage.screen, node) { detail?.let { push(it.screen) } }
                }
            }
        }
        return driver
    }

    @Test
    fun `511成功-当日有会议推送meetingInfoList`() {
        val driver = meetingWorld(listOf("14:00 产品评审", "10:00 技术周会"))
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetMeetingInfo(date = "2026-11-05"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        val (_, payload) = structured.results.single()
        assertTrue(payload["meetingInfoList"].toString().contains("产品评审"))
        assertEquals("2026-11-05", payload["date"]!!.toString().trim('"'))
        val firstMeeting = payload["meetingInfoList"]!!.toString()
        assertTrue("详情字段已读（不再推空串冒充）", firstMeeting.contains("2026-11-05 14:00"))
        assertTrue(firstMeeting.contains("组织者甲"))
    }

    @Test
    fun `511成功-会议列表滚动分页采集后再逐会读详情`() {
        val driver = meetingWorld(
            meetings = listOf("10:00 技术周会"),
            nextPages = listOf(listOf("14:00 产品评审")),
        )
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetMeetingInfo(date = "2026-11-05"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        val list = payload["meetingInfoList"].toString()
        assertTrue("首页会议已采集", list.contains("技术周会"))
        assertTrue("滚动第 2 页会议已采集", list.contains("产品评审"))
    }

    @Test
    fun `511详情-未开始会议实际时间缺失如实空串仍成功`() {
        val driver = meetingWorld(
            meetings = listOf("14:00 产品评审"),
            detailFor = {
                MeetingDetailFixture(
                    scheduledTime = "2026-11-05 14:00",
                    actualTime = "",
                    organizer = "组织者甲",
                    withActual = false,
                )
            },
        )
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetMeetingInfo(date = "2026-11-05"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val (_, payload) = structured.results.single()
        assertTrue(payload["meetingInfoList"].toString().contains("\"actualMeetingTime\":\"\""))
    }

    @Test
    fun `511负例-会议详情页未出现如实失败零推送`() {
        val driver = meetingWorld(
            meetings = listOf("14:00 产品评审"),
            detailFor = { null }, // 点击后详情页永不出现（NOT_CALIBRATED 占位/界面不匹配形态）
        )
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetMeetingInfo(date = "2026-11-05"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("不虚构详情读取成功：失败路径零推送", 0, structured.results.size)
    }

    @Test
    fun `511边界-当日无会议不推送仍成功`() {
        val driver = meetingWorld(emptyList())
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetMeetingInfo(date = "2026-11-05"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("无会议不推送", 0, structured.results.size)
    }

    @Test
    fun `511干跑-零动作`() {
        val driver = meetingWorld(listOf("14:00 产品评审"))
        val registry = ExecutorRegistry.of(GetMeetingInfoExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.GetMeetingInfo("2026-11-05"), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `横切-暂停拦截零推送`() {
        val driver = groupWorld()
        val registry = ExecutorRegistry.of(GetGroupMemberInfoExecutor(structured, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.GetGroupMemberInfo(groupName = "技术交流群"),
            registry,
        )
        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }
}
