/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fixture

import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver

/**
 * 每执行器一个「世界」：把假节点树装配成可导航的假驱动（界面跳转闭包），
 * 构造参数即故障注入开关（节点缺失/跳转失效/按钮不可点等场景）。
 *
 * 各世界共享：[FakeClock]（确定性超时）、[FakePauseGate]（FR-52 暂停门翻转）。
 */

/** 203 SEND_MESSAGE 世界。 */
class SendTextWorld(
    val targetRoom: String = "外部群测试群",
    val content: String = "您好，WorkTool-Open 协议测试消息",
    conversationPresent: Boolean = true,
    /** false = 点击会话项不切屏（点击被接受但聊天页永不出现 → 超时场景）。 */
    chatOpens: Boolean = true,
    withSendButton: Boolean = true,
    sendClickable: Boolean = true,
) {
    val clock = FakeClock()
    val gate = FakePauseGate()
    val home = HomeFixture(
        conversations = if (conversationPresent) listOf(targetRoom, "工作群") else listOf("工作群"),
    )
    val chat = ChatFixture(
        title = targetRoom,
        withSendButton = withSendButton,
        sendClickable = sendClickable,
    )
    val driver = FakeUiDriver(clock).apply {
        show(home.screen)
        if (chatOpens) {
            home.conversationNodes[targetRoom]?.let { conversationNode ->
                onClick(home.screen, conversationNode) { push(chat.screen) }
            }
            chat.sendNode?.let { sendNode ->
                onClick(chat.screen, sendNode) { chat.commitInputAsMessage() }
            }
        }
    }
}

/** 206 INIT_GROUP 世界。 */
class InitGroupWorld(
    val groupName: String = "客户交付群",
    members: List<String> = listOf("张三", "李四"),
    missingMembers: Set<String> = emptySet(),
    withAnnouncementField: Boolean = false,
    withPlusButton: Boolean = true,
    /** false = 「+」点击后菜单不出现（choose-init-group 超时场景）。 */
    menuOpens: Boolean = true,
    withConfirm: Boolean = true,
) {
    val clock = FakeClock()
    val gate = FakePauseGate()
    val home = HomeFixture(
        conversations = listOf("工作群"),
        withPlusButton = withPlusButton,
    )
    private val createMenu = CreateMenuFixture()
    val dialog = CreateGroupDialogFixture(
        selectableMembers = members - missingMembers,
        withAnnouncementField = withAnnouncementField,
        withConfirm = withConfirm,
    )
    val chatAfterCreate = ChatFixture(title = groupName)

    val driver = FakeUiDriver(clock).apply {
        show(home.screen)
        if (menuOpens) {
            home.plusButton?.let { plus ->
                onClick(home.screen, plus) { push(createMenu.screen) }
            }
            onClick(createMenu.screen, createMenu.initGroupItem) { push(dialog.screen) }
        }
        if (withConfirm) {
            onClick(dialog.screen, dialog.confirmNode!!) { show(chatAfterCreate.screen) }
        }
    }
}

/** 207 UPDATE_GROUP 世界（公告/备注/拉人）。 */
class UpdateGroupWorld(
    val targetGroup: String = "外部群测试群",
    existingMembers: List<String> = listOf("王五", "赵六"),
    /** 本轮要拉入的成员（=意图 addMembers；选择页候选人=其超集）。 */
    addMembers: List<String> = listOf("张三"),
    /** 选择页缺失的成员（拉人 NODE_NOT_FOUND 场景）。 */
    missingMembers: Set<String> = emptySet(),
    withAnnouncementEntry: Boolean = true,
    withRemarkEntry: Boolean = true,
    withAddMemberEntry: Boolean = true,
    /** false = 备注编辑页缺「确定」按钮（CR-t31-T31-R1 场景）。 */
    withRemarkConfirm: Boolean = true,
    groupOpens: Boolean = true,
) {
    val clock = FakeClock()
    val gate = FakePauseGate()
    private val toAdd = addMembers
    val home = HomeFixture(conversations = listOf(targetGroup, "工作群"))
    val chat = ChatFixture(title = targetGroup)
    val settings = GroupSettingsFixture(
        groupName = targetGroup,
        members = existingMembers,
        withAnnouncementEntry = withAnnouncementEntry,
        withRemarkEntry = withRemarkEntry,
        withAddMemberEntry = withAddMemberEntry,
    )

    /** 公告/备注各一编辑页（选择器同名、screen 实例分离，回写各自预览）。 */
    private val announcementEditor = EditorFixture(screenName = "announcement-editor")
    private val remarkEditor = EditorFixture(screenName = "remark-editor", withConfirm = withRemarkConfirm)
    val picker = MemberPickerFixture(candidates = addMembers - missingMembers + "钱七")

    val driver = FakeUiDriver(clock).apply {
        show(home.screen)
        if (groupOpens) {
            home.conversationNodes[targetGroup]?.let { conversationNode ->
                onClick(home.screen, conversationNode) { push(chat.screen) }
            }
            onClick(chat.screen, chat.titleNode) { push(settings.screen) }
            settings.announcementEntry?.let { entry ->
                onClick(settings.screen, entry) { push(announcementEditor.screen) }
                announcementEditor.confirmNode?.let { confirm ->
                    onClick(announcementEditor.screen, confirm) {
                        // 预览写全文（变更后校验步骤按「含新文本」断言，对齐真机预览语义）
                        settings.announcementPreview?.textValue =
                            "（已更新）" + announcementEditor.inputNode.textValue.orEmpty()
                        pop()
                    }
                }
            }
            settings.remarkEntry?.let { entry ->
                onClick(settings.screen, entry) { push(remarkEditor.screen) }
                remarkEditor.confirmNode?.let { confirm ->
                    onClick(remarkEditor.screen, confirm) {
                        settings.remarkPreview?.textValue =
                            "（已更新）" + remarkEditor.inputNode.textValue.orEmpty()
                        pop()
                    }
                }
            }
            settings.addMemberEntry?.let { entry ->
                onClick(settings.screen, entry) { push(picker.screen) }
            }
            onClick(picker.screen, picker.confirmNode) {
                settings.appendMembers(toAdd)
                pop()
            }
        }
    }
}

/** 225 MODIFY_GROUP_MEMBER_INFO 世界。 */
class MemberInfoWorld(
    val targetGroup: String = "外部群测试群",
    val memberName: String = "王五",
    membersInGroup: List<String>? = null,
    withMarkInput: Boolean = true,
    withConfirm: Boolean = true,
) {
    val clock = FakeClock()
    val gate = FakePauseGate()
    private val members = membersInGroup ?: listOf(memberName, "赵六")
    val home = HomeFixture(conversations = listOf(targetGroup, "工作群"))
    val chat = ChatFixture(title = targetGroup)
    val settings = GroupSettingsFixture(groupName = targetGroup, members = members)
    val memberDetail = MemberDetailFixture(
        memberName = memberName,
        withMarkInput = withMarkInput,
        withConfirm = withConfirm,
    )

    val driver = FakeUiDriver(clock).apply {
        show(home.screen)
        onClick(home.screen, home.conversationNodes.getValue(targetGroup)) { push(chat.screen) }
        onClick(chat.screen, chat.titleNode) { push(settings.screen) }
        settings.memberNodes[memberName]?.let { memberNode ->
            onClick(settings.screen, memberNode) { push(memberDetail.screen) }
        }
        if (withConfirm) {
            onClick(memberDetail.screen, memberDetail.confirmNode!!) { pop() }
        }
    }
}

/** 212 PASS_ALL_FRIEND_REQUEST 世界。 */
class PassFriendWorld(
    requesters: List<String> = listOf("新好友甲", "新好友乙"),
    withContactsTab: Boolean = true,
    withNewFriendsEntry: Boolean = true,
) {
    val clock = FakeClock()
    val gate = FakePauseGate()
    val home = HomeFixture(
        conversations = listOf("工作群"),
        withContactsTab = withContactsTab,
    )
    val contacts = ContactsFixture(withNewFriendsEntry = withNewFriendsEntry)
    val requests = FriendRequestsFixture(requesters = requesters)

    val driver = FakeUiDriver(clock).apply {
        show(home.screen)
        if (withContactsTab) {
            onClick(home.screen, home.contactsTab!!) { push(contacts.screen) }
        }
        if (withNewFriendsEntry) {
            onClick(contacts.screen, contacts.newFriendsEntry!!) { push(requests.screen) }
        }
        requests.rows.forEach { row ->
            onClick(requests.screen, row.acceptNode) { row.rowNode.detachFromParent() }
        }
    }
}
