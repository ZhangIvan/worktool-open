/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fixture

import cn.worktool.open.feature.executor.fake.EDIT_TEXT
import cn.worktool.open.feature.executor.fake.FakeExecutorNode
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.VIEW_GROUP
import cn.worktool.open.feature.executor.fake.fakeNode

/**
 * 企微界面 FakeNode 夹具（T3.1 任务卡 ③）：每指令 ≥1 棵假节点树，
 * 执行器在其上走通全步骤（JVM 纯测试）。
 *
 * 树形与 [cn.worktool.open.feature.executor.runtime.WeworkScreenRules] 内置占位
 * 选择器一一对应（resourceId/text 占位值，待 D1 真机 dump 校准后由外置规则
 * 配置替换——fixtures 与执行器都不需要改，R1 缓解）。
 *
 * 五个必备界面面（任务卡原文）：
 * 会话列表 [HomeFixture] / 聊天页 [ChatFixture] / 建群弹窗 [CreateGroupDialogFixture] /
 * 群设置 [GroupSettingsFixture] / 好友请求 [FriendRequestsFixture]；
 * 其余为支撑跳转的次要界面（发起菜单/编辑页/成员选择/成员详情/通讯录）。
 */

/** 会话列表（主界面）：底部 Tab + 「+」入口 + 会话项。 */
class HomeFixture(
    conversations: List<String>,
    withContactsTab: Boolean = true,
    withPlusButton: Boolean = true,
) {
    val messageTab: FakeExecutorNode = fakeNode(text = "消息", cls = TEXT_VIEW)
    val contactsTab: FakeExecutorNode? =
        if (withContactsTab) fakeNode(text = "通讯录", cls = TEXT_VIEW, clickable = true) else null
    val plusButton: FakeExecutorNode? =
        if (withPlusButton) {
            fakeNode(resId = "com.tencent.wework:id/home_create_plus", cls = "android.widget.ImageView", clickable = true)
        } else {
            null
        }

    /** 会话名 → 名称节点（点击进入聊天页的锚点；可点击=点击被界面接受）。 */
    val conversationNodes: Map<String, FakeExecutorNode> = conversations.associateWith { name ->
        fakeNode(
            text = name,
            resId = "com.tencent.wework:id/conversation_item_name",
            cls = TEXT_VIEW,
            clickable = true,
        )
    }

    private val conversationList: FakeExecutorNode = fakeNode(cls = VIEW_GROUP, scrollable = true) {
        conversationNodes.values.forEach { nameNode ->
            add(
                fakeNode(cls = VIEW_GROUP, clickable = true) {
                    add(nameNode)
                    add(fakeNode(text = "最近一条消息", cls = TEXT_VIEW))
                },
            )
        }
    }

    val screen: FakeScreen = FakeScreen(
        name = "home",
        root = fakeNode(cls = VIEW_GROUP, resId = "com.tencent.wework:id/home_root") {
            add(fakeNode(cls = VIEW_GROUP) { add(fakeNode(text = "企业微信", cls = TEXT_VIEW)) })
            add(conversationList)
            plusButton?.let { add(it) }
            add(
                fakeNode(cls = VIEW_GROUP) {
                    add(messageTab)
                    contactsTab?.let { add(it) }
                },
            )
        },
    )
}

/** 聊天页：标题（点击进群设置）+ 消息列表 + 输入框 + 发送按钮。 */
class ChatFixture(
    title: String,
    initialMessages: List<String> = emptyList(),
    withSendButton: Boolean = true,
    sendClickable: Boolean = true,
) {
    val titleNode: FakeExecutorNode = fakeNode(
        text = title,
        resId = "com.tencent.wework:id/chat_title",
        cls = TEXT_VIEW,
        clickable = true,
    )

    /** 消息列表容器（发送后追加 TextView）。 */
    val messageList: FakeExecutorNode = fakeNode(cls = VIEW_GROUP, scrollable = true) {
        initialMessages.forEach { add(fakeNode(text = it, cls = TEXT_VIEW)) }
    }

    val inputNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/chat_input_edit",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val sendNode: FakeExecutorNode? =
        if (withSendButton) {
            fakeNode(
                resId = "com.tencent.wework:id/chat_send_btn",
                cls = TEXT_VIEW,
                clickable = sendClickable,
            )
        } else {
            null
        }

    val screen: FakeScreen = FakeScreen(
        name = "chat",
        root = fakeNode(cls = VIEW_GROUP) {
            add(fakeNode(cls = VIEW_GROUP) { add(titleNode) })
            add(messageList)
            add(
                fakeNode(cls = VIEW_GROUP) {
                    add(inputNode)
                    sendNode?.let { add(it) }
                },
            )
        },
    )

    /** 当前已上屏消息文本（断言用）。 */
    fun messageTexts(): List<String?> = messageList.children.map { it.textValue }

    /** 发送生效：把输入框文本落为一条消息并清空输入框（send 点击效果闭包调用）。 */
    fun commitInputAsMessage() {
        val text = inputNode.textValue
        if (!text.isNullOrBlank()) {
            messageList.add(fakeNode(text = text, cls = TEXT_VIEW))
            inputNode.textValue = null
        }
    }
}

/** 「+」发起菜单（发起群聊入口）。 */
class CreateMenuFixture {
    val initGroupItem: FakeExecutorNode = fakeNode(text = "发起群聊", cls = TEXT_VIEW, clickable = true)

    val screen: FakeScreen = FakeScreen(
        name = "create-menu",
        root = fakeNode(cls = VIEW_GROUP) {
            add(initGroupItem)
            add(fakeNode(text = "加朋友", cls = TEXT_VIEW))
            add(fakeNode(text = "扫一扫", cls = TEXT_VIEW))
        },
    )
}

/** 建群弹窗：群名输入 + 成员勾选 + （可选）公告输入 + 完成按钮。 */
class CreateGroupDialogFixture(
    selectableMembers: List<String>,
    withAnnouncementField: Boolean = false,
    withConfirm: Boolean = true,
) {
    val nameInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/group_name_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val announcementInput: FakeExecutorNode? =
        if (withAnnouncementField) {
            fakeNode(resId = "com.tencent.wework:id/announcement_input", cls = EDIT_TEXT, editable = true)
        } else {
            null
        }

    /** 可选成员（缺失成员=模拟通讯录中不存在，选择步骤 NODE_NOT_FOUND）。 */
    val memberNodes: Map<String, FakeExecutorNode> = selectableMembers.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) {
            fakeNode(resId = "com.tencent.wework:id/group_create_confirm", cls = TEXT_VIEW, clickable = true)
        } else {
            null
        }

    val screen: FakeScreen = FakeScreen(
        name = "create-group",
        root = fakeNode(cls = VIEW_GROUP) {
            add(nameInput)
            announcementInput?.let { add(it) }
            add(fakeNode(cls = VIEW_GROUP) { memberNodes.values.forEach { add(it) } })
            confirmNode?.let { add(it) }
        },
    )
}

/** 群设置页：成员条 + 群公告/群备注/添加成员入口。 */
class GroupSettingsFixture(
    groupName: String,
    members: List<String>,
    withAnnouncementEntry: Boolean = true,
    withRemarkEntry: Boolean = true,
    withAddMemberEntry: Boolean = true,
) {
    val titleNode: FakeExecutorNode = fakeNode(text = groupName, cls = TEXT_VIEW)

    /** 成员名节点（点击进成员详情）。 */
    val memberNodes: Map<String, FakeExecutorNode> = members.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val announcementEntry: FakeExecutorNode? =
        if (withAnnouncementEntry) fakeNode(text = "群公告", cls = TEXT_VIEW, clickable = true) else null

    /** 公告预览文本（公告编辑生效后的断言锚点）。 */
    val announcementPreview: FakeExecutorNode? =
        announcementEntry?.let { fakeNode(text = "（暂无公告）", cls = TEXT_VIEW) }

    val remarkEntry: FakeExecutorNode? =
        if (withRemarkEntry) fakeNode(text = "群备注", cls = TEXT_VIEW, clickable = true) else null

    val remarkPreview: FakeExecutorNode? =
        remarkEntry?.let { fakeNode(text = "（暂无备注）", cls = TEXT_VIEW) }

    val addMemberEntry: FakeExecutorNode? =
        if (withAddMemberEntry) fakeNode(text = "添加成员", cls = TEXT_VIEW, clickable = true) else null

    private val memberStrip: FakeExecutorNode = fakeNode(cls = VIEW_GROUP) {
        memberNodes.values.forEach { add(it) }
    }

    val screen: FakeScreen = FakeScreen(
        name = "group-settings",
        root = fakeNode(cls = VIEW_GROUP) {
            add(titleNode)
            add(memberStrip)
            announcementEntry?.let {
                add(
                    fakeNode(cls = VIEW_GROUP) {
                        add(it)
                        announcementPreview?.let { p -> add(p) }
                    },
                )
            }
            remarkEntry?.let {
                add(
                    fakeNode(cls = VIEW_GROUP) {
                        add(it)
                        remarkPreview?.let { p -> add(p) }
                    },
                )
            }
            addMemberEntry?.let { add(it) }
        },
    )

    /** 拉人生效：成员条追加新成员（成员选择「确定」效果闭包调用；断言走全树 findByText）。 */
    fun appendMembers(newMembers: List<String>) {
        newMembers.forEach { name ->
            memberStrip.add(fakeNode(text = name, cls = TEXT_VIEW, clickable = true))
        }
    }
}

/** 文本编辑页（群公告/群备注共用：选择器同 announcement_input/announcement_confirm）。 */
class EditorFixture(
    private val screenName: String = "editor",
    /** false = 编辑页缺「确定」按钮（CR-t31-T31-R1：变更未保存必须失败上报）。 */
    withConfirm: Boolean = true,
) {
    val inputNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/announcement_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) {
            fakeNode(
                resId = "com.tencent.wework:id/announcement_confirm",
                cls = TEXT_VIEW,
                clickable = true,
            )
        } else {
            null
        }

    val screen: FakeScreen = FakeScreen(
        name = screenName,
        root = fakeNode(cls = VIEW_GROUP) {
            add(inputNode)
            confirmNode?.let { add(it) }
        },
    )
}

/** 添加成员（拉人）选择页。 */
class MemberPickerFixture(
    candidates: List<String>,
) {
    val candidateNodes: Map<String, FakeExecutorNode> = candidates.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val confirmNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/member_add_confirm",
        cls = TEXT_VIEW,
        clickable = true,
    )

    val screen: FakeScreen = FakeScreen(
        name = "member-picker",
        root = fakeNode(cls = VIEW_GROUP) {
            candidateNodes.values.forEach { add(it) }
            add(confirmNode)
        },
    )
}

/** 成员详情页（225）：备注输入 + 标签输入 + 确定。 */
class MemberDetailFixture(
    memberName: String,
    withMarkInput: Boolean = true,
    withTagInput: Boolean = true,
    withConfirm: Boolean = true,
) {
    val markInput: FakeExecutorNode? =
        if (withMarkInput) {
            fakeNode(resId = "com.tencent.wework:id/member_mark_input", cls = EDIT_TEXT, editable = true)
        } else {
            null
        }

    val tagInput: FakeExecutorNode? =
        if (withTagInput) {
            fakeNode(resId = "com.tencent.wework:id/member_tag_input", cls = EDIT_TEXT, editable = true)
        } else {
            null
        }

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) {
            fakeNode(resId = "com.tencent.wework:id/member_mark_confirm", cls = TEXT_VIEW, clickable = true)
        } else {
            null
        }

    val screen: FakeScreen = FakeScreen(
        name = "member-detail",
        root = fakeNode(cls = VIEW_GROUP) {
            add(fakeNode(text = memberName, cls = TEXT_VIEW))
            markInput?.let { add(it) }
            tagInput?.let { add(it) }
            confirmNode?.let { add(it) }
        },
    )
}

/** 通讯录页（好友请求入口）。 */
class ContactsFixture(
    withNewFriendsEntry: Boolean = true,
) {
    val newFriendsEntry: FakeExecutorNode? =
        if (withNewFriendsEntry) fakeNode(text = "新的朋友", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "contacts",
        root = fakeNode(cls = VIEW_GROUP) {
            add(fakeNode(text = "通讯录", cls = TEXT_VIEW))
            newFriendsEntry?.let { add(it) }
            add(fakeNode(text = "张三", cls = TEXT_VIEW))
        },
    )
}

/** 好友请求页：请求行（姓名 + 接受按钮），接受后行消失。 */
class FriendRequestsFixture(
    requesters: List<String>,
) {

    class RequestRow(requester: String) {
        val nameNode: FakeExecutorNode = fakeNode(text = requester, cls = TEXT_VIEW)
        val acceptNode: FakeExecutorNode = fakeNode(text = "接受", cls = TEXT_VIEW, clickable = true)
        val rowNode: FakeExecutorNode = fakeNode(cls = VIEW_GROUP) {
            add(nameNode)
            add(acceptNode)
        }
    }

    val rows: List<RequestRow> = requesters.map { RequestRow(it) }

    /** 剩余待处理请求行（断言用）。 */
    fun remaining(): List<RequestRow> = rows.filter { it.rowNode.parent != null }

    val screen: FakeScreen = FakeScreen(
        name = "friend-requests",
        root = fakeNode(cls = VIEW_GROUP) {
            rows.forEach { add(it.rowNode) }
        },
    )
}
