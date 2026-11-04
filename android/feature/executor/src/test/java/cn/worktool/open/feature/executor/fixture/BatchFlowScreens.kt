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
 * 对等计划批（docs/10 §B B2~B4）FakeNode 界面夹具：
 * 富气泡聊天页 / 长按菜单 / 转发面板 / 多选面板 / 「+」面板 / 微盘 / 文件选择器 /
 * 推荐条表单 / 待办表单 / 群看板 / 扫码流 / 群活码 / 通讯录操作面 /
 * 工作台·打卡·日历 / 抽屉企业列表 / 我-设置-存储空间。
 * 树形与 WeworkScreenRules 内置占位选择器一一对应（同 WeworkScreens.kt 口径）。
 */

/** 富气泡聊天页：气泡行 = 发送人 TextView + 正文 TextView（chat_message_text）。 */
class RichChatFixture(
    title: String,
    bubbles: List<Pair<String, String>> = emptyList(),
    withInput: Boolean = true,
) {
    val titleNode: FakeExecutorNode = fakeNode(
        text = title,
        resId = "com.tencent.wework:id/o6j",
        cls = TEXT_VIEW,
        clickable = true,
    )

    /** 气泡正文节点（长按/定位锚点）。 */
    val bubbleNodes: List<FakeExecutorNode> = bubbles.map { (sender, content) ->
        fakeNode(text = content, resId = "com.tencent.wework:id/ith", cls = TEXT_VIEW, clickable = true)
    }

    private val messageList: FakeExecutorNode = fakeNode(cls = VIEW_GROUP, scrollable = true) {
        bubbles.forEachIndexed { index, (sender, _) ->
            add(
                fakeNode(cls = VIEW_GROUP) {
                    add(fakeNode(text = sender, cls = TEXT_VIEW))
                    add(bubbleNodes[index])
                },
            )
        }
    }

    val inputNode: FakeExecutorNode? =
        if (withInput) {
            fakeNode(resId = "com.tencent.wework:id/iu5", cls = EDIT_TEXT, editable = true, clickable = true)
        } else {
            null
        }

    val sendNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/chat_send_btn",
        cls = TEXT_VIEW,
        clickable = true,
    )

    val screen: FakeScreen = FakeScreen(
        name = "rich-chat-$title",
        root = fakeNode(cls = VIEW_GROUP) {
            add(fakeNode(cls = VIEW_GROUP) { add(titleNode) })
            add(messageList)
            inputNode?.let {
                add(fakeNode(cls = VIEW_GROUP) { add(it); add(sendNode) })
            }
        },
    )

    fun bubbleContents(): List<String?> = messageList.children.map { it.textValue }

    /** 发送生效：输入框文本落为一条消息（send 点击效果闭包调用）。 */
    fun commitInputAsMessage() {
        val text = inputNode?.textValue
        if (!text.isNullOrBlank()) {
            messageList.add(fakeNode(text = text, cls = TEXT_VIEW))
            inputNode?.textValue = null
        }
    }
}

/** 长按消息菜单（items 提供哪些菜单项；缺项=菜单不支持该操作）。 */
class MessageMenuFixture(items: Set<String> = setOf("回复", "转发", "撤回", "多选", "逐条转发", "合并转发")) {
    val itemNodes: Map<String, FakeExecutorNode> = items.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val screen: FakeScreen = FakeScreen(
        name = "message-menu",
        root = fakeNode(cls = VIEW_GROUP) { itemNodes.values.forEach { add(it) } },
    )
}

/** 转发接收人面板：搜索框 + 接收人条目 + 确认按钮。 */
class ForwardPickerFixture(
    receivers: List<String>,
    withConfirm: Boolean = true,
) {
    val receiverNodes: Map<String, FakeExecutorNode> = receivers.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/relay_picker_item_name", cls = TEXT_VIEW, clickable = true)
    }

    val searchInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/relay_picker_input",
        cls = EDIT_TEXT,
        editable = true,
    )

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) fakeNode(resId = "com.tencent.wework:id/relay_picker_confirm", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "forward-picker",
        root = fakeNode(cls = VIEW_GROUP) {
            add(searchInput)
            receiverNodes.values.forEach { add(it) }
            confirmNode?.let { add(it) }
        },
    )
}

/** 多选面板：消息勾选条目 + 模式按钮（逐条/合并转发）+ 确认。 */
class MultiSelectFixture(
    messages: List<String>,
    modes: Set<String> = setOf("逐条转发", "合并转发"),
) {
    val messageNodes: Map<String, FakeExecutorNode> = messages.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/ith", cls = TEXT_VIEW, clickable = true)
    }

    val modeNodes: Map<String, FakeExecutorNode> = modes.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val confirmNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/relay_picker_confirm",
        cls = TEXT_VIEW,
        clickable = true,
    )

    val screen: FakeScreen = FakeScreen(
        name = "multi-select",
        root = fakeNode(cls = VIEW_GROUP) {
            messageNodes.values.forEach { add(it) }
            modeNodes.values.forEach { add(it) }
            add(confirmNode)
        },
    )
}

/** 聊天「+」面板（微盘/文件/待办入口；缺项=界面不支持）。 */
class PlusPanelFixture(entries: Set<String> = setOf("微盘", "文件", "待办")) {
    val entryNodes: Map<String, FakeExecutorNode> = entries.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val screen: FakeScreen = FakeScreen(
        name = "plus-panel",
        root = fakeNode(cls = VIEW_GROUP) { entryNodes.values.forEach { add(it) } },
    )
}

/** 微盘页：搜索框 + 文件/图片条目 + 发送按钮。 */
class MicroDiskFixture(items: List<String>, withSend: Boolean = true) {
    val searchInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/micro_disk_search_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val itemNodes: Map<String, FakeExecutorNode> = items.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/micro_disk_item_name", cls = TEXT_VIEW, clickable = true)
    }

    val sendNode: FakeExecutorNode? =
        if (withSend) fakeNode(text = "发送", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "micro-disk",
        root = fakeNode(cls = VIEW_GROUP) {
            add(searchInput)
            itemNodes.values.forEach { add(it) }
            sendNode?.let { add(it) }
        },
    )
}

/** 文件选择器（218 落盘文件可见）。 */
class FilePickerFixture(files: List<String>) {
    val fileNodes: Map<String, FakeExecutorNode> = files.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/file_picker_item_name", cls = TEXT_VIEW, clickable = true)
    }

    val screen: FakeScreen = FakeScreen(
        name = "file-picker",
        root = fakeNode(cls = VIEW_GROUP) { fileNodes.values.forEach { add(it) } },
    )
}

/** 推荐条表单（235）：收款金额输入+发起收款 / 商品条目+发送。 */
class RecommendFormFixture(
    withAmountInput: Boolean = true,
    products: List<String> = emptyList(),
) {
    val amountInput: FakeExecutorNode? =
        if (withAmountInput) {
            fakeNode(resId = "com.tencent.wework:id/recommend_amount_input", cls = EDIT_TEXT, editable = true, clickable = true)
        } else {
            null
        }

    val collectConfirm: FakeExecutorNode? =
        if (withAmountInput) fakeNode(text = "发起收款", cls = TEXT_VIEW, clickable = true) else null

    val productNodes: Map<String, FakeExecutorNode> = products.associateWith {
        fakeNode(text = it, cls = TEXT_VIEW, clickable = true)
    }

    val sendNode: FakeExecutorNode = fakeNode(text = "发送", cls = TEXT_VIEW, clickable = true)

    val screen: FakeScreen = FakeScreen(
        name = "recommend-form",
        root = fakeNode(cls = VIEW_GROUP) {
            amountInput?.let { add(it) }
            collectConfirm?.let { add(it) }
            productNodes.values.forEach { add(it) }
            add(sendNode)
        },
    )
}

/** 待办表单（221）：内容输入 + 添加按钮。 */
class TodoFormFixture(withSubmit: Boolean = true) {
    val contentInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/todo_content_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val submitNode: FakeExecutorNode? =
        if (withSubmit) fakeNode(text = "添加", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "todo-form",
        root = fakeNode(cls = VIEW_GROUP) {
            add(contentInput)
            submitNode?.let { add(it) }
        },
    )
}

/** 群看板添加网页页（240）：URL 输入 + 确定。 */
class BoardWebpageFixture(withSubmit: Boolean = true) {
    val urlInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/board_url_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val submitNode: FakeExecutorNode? =
        if (withSubmit) fakeNode(text = "确定", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "board-webpage",
        root = fakeNode(cls = VIEW_GROUP) {
            add(urlInput)
            submitNode?.let { add(it) }
        },
    )
}

/** 扫码结果页（233/243）：加入群聊按钮。 */
class ScanResultFixture(withJoin: Boolean = true, joinClickable: Boolean = true) {
    val joinNode: FakeExecutorNode? =
        if (withJoin) fakeNode(text = "加入群聊", cls = TEXT_VIEW, clickable = joinClickable) else null

    val screen: FakeScreen = FakeScreen(
        name = "scan-result",
        root = fakeNode(cls = VIEW_GROUP) { joinNode?.let { add(it) } },
    )
}

/** 群活码生成页（239）：新群名输入 + 生成确认 + 保存到相册。 */
class LiveQrFixture(
    withSave: Boolean = true,
    saveClickable: Boolean = true,
) {
    val nameInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/live_qr_name_input",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val confirmNode: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/live_qr_confirm",
        cls = TEXT_VIEW,
        clickable = true,
    )

    val saveNode: FakeExecutorNode? =
        if (withSave) fakeNode(text = "保存到相册", cls = TEXT_VIEW, clickable = saveClickable) else null

    /** 群二维码内容节点（保存后出现）。 */
    val qrContentNode: FakeExecutorNode = fakeNode(
        text = "https://work.weixin.qq.cn/liveqr/xyz",
        resId = "com.tencent.wework:id/group_qrcode_content",
        cls = TEXT_VIEW,
    )

    val screen: FakeScreen = FakeScreen(
        name = "live-qr",
        root = fakeNode(cls = VIEW_GROUP) {
            add(nameInput)
            add(confirmNode)
            saveNode?.let { add(it) }
        },
    )
}

/** 通讯录操作面：添加朋友页（搜索框+结果+发送）。 */
class AddFriendPageFixture(
    results: List<String> = emptyList(),
    withSend: Boolean = true,
) {
    val searchInput: FakeExecutorNode = fakeNode(
        resId = "com.tencent.wework:id/m0f",
        cls = EDIT_TEXT,
        editable = true,
        clickable = true,
    )

    val resultNodes: Map<String, FakeExecutorNode> = results.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/search_result_item_name", cls = TEXT_VIEW, clickable = true)
    }

    val sendNode: FakeExecutorNode? =
        if (withSend) fakeNode(text = "发送", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "add-friend",
        root = fakeNode(cls = VIEW_GROUP) {
            add(searchInput)
            resultNodes.values.forEach { add(it) }
            sendNode?.let { add(it) }
        },
    )
}

/** 联系人详情页（213 修改/220 群成员/234 删除入口）。 */
class ContactDetailFixture(
    name: String,
    withAddButton: Boolean = false,
    withMarkInput: Boolean = false,
    withMarkConfirm: Boolean = true,
    withMore: Boolean = true,
) {
    val nameNode: FakeExecutorNode = fakeNode(
        text = name,
        resId = "com.tencent.wework:id/contact_detail_name",
        cls = TEXT_VIEW,
    )

    val addFriendNode: FakeExecutorNode? =
        if (withAddButton) fakeNode(text = "添加好友", cls = TEXT_VIEW, clickable = true) else null

    val markInput: FakeExecutorNode? =
        if (withMarkInput) {
            fakeNode(resId = "com.tencent.wework:id/contact_mark_input", cls = EDIT_TEXT, editable = true, clickable = true)
        } else {
            null
        }

    val markConfirm: FakeExecutorNode? =
        if (withMarkInput && withMarkConfirm) {
            fakeNode(resId = "com.tencent.wework:id/contact_mark_confirm", cls = TEXT_VIEW, clickable = true)
        } else {
            null
        }

    val moreNode: FakeExecutorNode? =
        if (withMore) fakeNode(resId = "com.tencent.wework:id/contact_detail_more", clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "contact-detail",
        root = fakeNode(cls = VIEW_GROUP) {
            add(nameNode)
            addFriendNode?.let { add(it) }
            markInput?.let { add(it) }
            markConfirm?.let { add(it) }
            moreNode?.let { add(it) }
        },
    )
}

/** 删除菜单 + 删除确认弹窗（234/236 共用两屏）。 */
class DeleteFlowFixture(withDeleteMenu: Boolean = true, withConfirm: Boolean = true) {
    val deleteMenuNode: FakeExecutorNode? =
        if (withDeleteMenu) fakeNode(text = "删除", cls = TEXT_VIEW, clickable = true) else null

    val menuScreen: FakeScreen = FakeScreen(
        name = "delete-menu",
        root = fakeNode(cls = VIEW_GROUP) { deleteMenuNode?.let { add(it) } },
    )

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) fakeNode(text = "删除", cls = TEXT_VIEW, clickable = true) else null

    val confirmScreen: FakeScreen = FakeScreen(
        name = "delete-confirm",
        root = fakeNode(cls = VIEW_GROUP) { confirmNode?.let { add(it) } },
    )
}

/** 「我的客户」列表页（236/507）：条目 + 批量删除按钮 + 容器（分页）。 */
class MyCustomersFixture(
    names: List<String>,
    pages: List<List<String>>? = null,
    withEntry: Boolean = true,
    withDeleteButton: Boolean = true,
) {
    private val pagesInternal: List<List<String>> = pages ?: listOf(names)
    private var nextPage = 1

    val entryNode: FakeExecutorNode? =
        if (withEntry) fakeNode(text = "我的客户", cls = TEXT_VIEW, clickable = true) else null

    val deleteButton: FakeExecutorNode? =
        if (withDeleteButton) fakeNode(text = "删除", cls = TEXT_VIEW, clickable = true) else null

    val listContainer: FakeExecutorNode? = fakeNode(
        cls = VIEW_GROUP,
        resId = "com.tencent.wework:id/my_customers_container",
        scrollable = true,
    ) {
        pagesInternal.first().forEach { add(customerNode(it)) }
    }

    fun appendNextPage(): Boolean {
        val next = pagesInternal.getOrNull(nextPage) ?: return false
        nextPage++
        next.forEach { listContainer?.add(customerNode(it)) }
        return true
    }

    private fun customerNode(name: String): FakeExecutorNode = fakeNode(
        text = name,
        resId = "com.tencent.wework:id/my_customer_item",
        cls = TEXT_VIEW,
        clickable = true,
    )

    val screen: FakeScreen = FakeScreen(
        name = "my-customers",
        root = fakeNode(cls = VIEW_GROUP) {
            deleteButton?.let { add(it) }
            listContainer?.let { add(it) }
        },
    )
}

/** 「我」页（503/237 入口）：姓名 + 企业 + 设置入口。 */
class MePageFixture(
    name: String = "机器人一号",
    corp: String = "A公司",
    withSettings: Boolean = true,
) {
    val nameNode: FakeExecutorNode = fakeNode(text = name, resId = "com.tencent.wework:id/me_name", cls = TEXT_VIEW)
    val corpNode: FakeExecutorNode = fakeNode(text = corp, resId = "com.tencent.wework:id/me_corp", cls = TEXT_VIEW)
    val settingsNode: FakeExecutorNode? =
        if (withSettings) fakeNode(text = "设置", cls = TEXT_VIEW, clickable = true) else null
    val meTab: FakeExecutorNode = fakeNode(text = "我", cls = TEXT_VIEW, clickable = true)

    val screen: FakeScreen = FakeScreen(
        name = "me",
        root = fakeNode(cls = VIEW_GROUP) {
            add(nameNode)
            add(corpNode)
            settingsNode?.let { add(it) }
        },
    )
}

/** 设置-存储空间页（237）：设置列表页（存储空间入口）→ 存储空间页（清理）→ 确认弹窗。 */
class StorageCleanFixture(withClean: Boolean = true, withConfirm: Boolean = true) {
    /** 设置列表页「存储空间」入口。 */
    val storageEntry: FakeExecutorNode = fakeNode(text = "存储空间", cls = TEXT_VIEW, clickable = true)

    val settingsScreen: FakeScreen = FakeScreen(
        name = "wework-settings",
        root = fakeNode(cls = VIEW_GROUP) { add(storageEntry) },
    )

    val cleanButton: FakeExecutorNode? =
        if (withClean) fakeNode(text = "清理", cls = TEXT_VIEW, clickable = true) else null

    val screen: FakeScreen = FakeScreen(
        name = "storage",
        root = fakeNode(cls = VIEW_GROUP) { cleanButton?.let { add(it) } },
    )

    val confirmNode: FakeExecutorNode? =
        if (withConfirm) fakeNode(text = "确定", cls = TEXT_VIEW, clickable = true) else null

    val confirmScreen: FakeScreen = FakeScreen(
        name = "storage-confirm",
        root = fakeNode(cls = VIEW_GROUP) { confirmNode?.let { add(it) } },
    )
}

/** 工作台页（222/511/239 入口）：打卡/日历/客户群入口。 */
class WorkbenchFixture(
    withClock: Boolean = true,
    withCalendar: Boolean = false,
    withCustomerGroup: Boolean = false,
) {
    val clockEntry: FakeExecutorNode? =
        if (withClock) fakeNode(text = "打卡", cls = TEXT_VIEW, clickable = true) else null
    val calendarEntry: FakeExecutorNode? =
        if (withCalendar) fakeNode(text = "日历", cls = TEXT_VIEW, clickable = true) else null
    val customerGroupEntry: FakeExecutorNode? =
        if (withCustomerGroup) fakeNode(text = "客户群", cls = TEXT_VIEW, clickable = true) else null

    val workbenchTab: FakeExecutorNode = fakeNode(text = "工作台", cls = TEXT_VIEW, clickable = true)

    val screen: FakeScreen = FakeScreen(
        name = "workbench",
        root = fakeNode(cls = VIEW_GROUP) {
            clockEntry?.let { add(it) }
            calendarEntry?.let { add(it) }
            customerGroupEntry?.let { add(it) }
        },
    )
}

/** 打卡页（222）：范围内提示 + 打卡按钮 + 结果文案。 */
class ClockPageFixture(
    inRange: Boolean = true,
    withActionButton: Boolean = true,
    actionLabel: String = "上班打卡",
    successText: String = "上班·正常",
) {
    val rangeMarker: FakeExecutorNode? =
        if (inRange) fakeNode(text = "你已在打卡范围内", cls = TEXT_VIEW) else null

    val actionButton: FakeExecutorNode? =
        if (withActionButton) {
            fakeNode(resId = "com.tencent.wework:id/clock_action_button", cls = TEXT_VIEW, clickable = true)
        } else {
            null
        }

    /** 打卡结果文案（点击打卡按钮后出现）。 */
    val resultNode: FakeExecutorNode = fakeNode(text = successText, cls = TEXT_VIEW)

    val screen: FakeScreen = FakeScreen(
        name = "clock",
        root = fakeNode(cls = VIEW_GROUP) {
            rangeMarker?.let { add(it) }
            actionButton?.let {
                add(
                    fakeNode(cls = VIEW_GROUP) {
                        add(fakeNode(text = actionLabel, cls = TEXT_VIEW))
                        add(it)
                    },
                )
            }
        },
    )
}

/**
 * 日历会议页（511）：日期格（测试侧拼装）+ 会议列表（分页懒加载容器，滚动经
 * [appendNextMeetingPage] 追加下一页）。详情页导航闭包由测试 driver 装配侧注册
 * （onClick 需 FakeUiDriver；详情屏用 [MeetingDetailFixture]）。
 */
class CalendarMeetingFixture(
    /** 首屏会议条目（兼容旧单页形态）。 */
    meetings: List<String>,
    withList: Boolean = true,
    /** 滚动分页数据（null=单页；首屏=第 1 页）。 */
    pages: List<List<String>>? = null,
) {
    private val pageData: List<List<String>> = if (withList) {
        pages ?: listOf(meetings)
    } else {
        emptyList()
    }

    private var nextPage: Int = 1

    /** 会议名 → 条目节点（可点击=详情导航生效锚点；511 逐会进详情）。 */
    val meetingNodes: Map<String, FakeExecutorNode> = pageData.flatten().associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/meeting_item_name", cls = TEXT_VIEW, clickable = true)
    }

    /** 会议列表滚动容器（CALENDAR_MEETING_LIST_CONTAINER 选择器锚点）。 */
    val listContainer: FakeExecutorNode = fakeNode(
        cls = VIEW_GROUP,
        resId = "com.tencent.wework:id/meeting_list_container",
        scrollable = true,
    ) {
        pageData.firstOrNull()?.forEach { name -> add(meetingNodes.getValue(name)) }
    }

    /** 滚动追加下一页会议（页耗尽返回 false → 分页读取判「无新增」收敛）。 */
    fun appendNextMeetingPage(): Boolean {
        val next = pageData.getOrNull(nextPage) ?: return false
        nextPage++
        next.forEach { name -> listContainer.add(meetingNodes.getValue(name)) }
        return true
    }

    /** 当前容器内全部会议名（断言用）。 */
    fun meetingNames(): List<String?> = listContainer.children.map { it.textValue }

    val screen: FakeScreen = FakeScreen(
        name = "calendar-meeting",
        root = fakeNode(cls = VIEW_GROUP) {
            add(listContainer)
        },
    )
}

/**
 * 会议详情页（511）：预定/实际时间 + 组织人三字段（选择器 NOT_CALIBRATED 占位，
 * resId 与 WeworkScreenRules.DEFAULTS 一一对应）。with*=false = 字段缺失（如实空串场景）。
 */
class MeetingDetailFixture(
    scheduledTime: String,
    actualTime: String,
    organizer: String,
    withScheduled: Boolean = true,
    withActual: Boolean = true,
    withOrganizer: Boolean = true,
    screenName: String = "meeting-detail",
) {
    val scheduledNode: FakeExecutorNode? =
        if (withScheduled) {
            fakeNode(
                text = scheduledTime,
                resId = "com.tencent.wework:id/meeting_detail_scheduled_time",
                cls = TEXT_VIEW,
            )
        } else {
            null
        }

    val actualNode: FakeExecutorNode? =
        if (withActual) {
            fakeNode(
                text = actualTime,
                resId = "com.tencent.wework:id/meeting_detail_actual_time",
                cls = TEXT_VIEW,
            )
        } else {
            null
        }

    val organizerNode: FakeExecutorNode? =
        if (withOrganizer) {
            fakeNode(
                text = organizer,
                resId = "com.tencent.wework:id/meeting_detail_organizer",
                cls = TEXT_VIEW,
            )
        } else {
            null
        }

    val screen: FakeScreen = FakeScreen(
        name = screenName,
        root = fakeNode(cls = VIEW_GROUP) {
            scheduledNode?.let { add(it) }
            actualNode?.let { add(it) }
            organizerNode?.let { add(it) }
        },
    )
}

/** 消息页左上抽屉（223/506）：企业条目。 */
class CorpDrawerFixture(
    corps: List<String>,
    currentCorp: String,
    withDrawer: Boolean = true,
) {
    val drawerEntry: FakeExecutorNode? =
        if (withDrawer) fakeNode(resId = "com.tencent.wework:id/home_drawer_entry", clickable = true) else null

    val corpNodes: Map<String, FakeExecutorNode> = corps.associateWith {
        fakeNode(text = it, resId = "com.tencent.wework:id/corp_list_item_name", cls = TEXT_VIEW, clickable = true)
    }

    val homeCorpNode: FakeExecutorNode = fakeNode(
        text = currentCorp,
        resId = "com.tencent.wework:id/home_corp_name",
        cls = TEXT_VIEW,
    )

    val drawerScreen: FakeScreen = FakeScreen(
        name = "corp-drawer",
        root = fakeNode(cls = VIEW_GROUP) { corpNodes.values.forEach { add(it) } },
    )
}

/** 群设置扩展页（群二维码入口 + 二维码内容屏，504）。 */
class GroupQrFixture(withEntry: Boolean = true) {
    val qrEntry: FakeExecutorNode? =
        if (withEntry) fakeNode(text = "群二维码", cls = TEXT_VIEW, clickable = true) else null

    val qrContent: FakeExecutorNode = fakeNode(
        text = "https://work.weixin.qq.cn/qr/abc",
        resId = "com.tencent.wework:id/group_qrcode_content",
        cls = TEXT_VIEW,
    )

    val qrScreen: FakeScreen = FakeScreen(
        name = "group-qrcode",
        root = fakeNode(cls = VIEW_GROUP) { add(qrContent) },
    )
}

/** 群详情扩展（群名标题/群主/公告值/成员列表容器——214/508 用）。 */
class GroupDetailFixture(
    groupName: String,
    owner: String = "群主甲",
    announcement: String = "（暂无公告）",
    members: List<String> = emptyList(),
) {
    // 真机校准（2026-11-04）：群设置页「群聊名称」行=标签 hn1 + 值 lm8（sibling 取值）。
    val titleNode: FakeExecutorNode = fakeNode(
        text = groupName,
        resId = "com.tencent.wework:id/lm8",
        cls = TEXT_VIEW,
    )
    val titleLabelNode: FakeExecutorNode = fakeNode(
        text = "群聊名称",
        resId = "com.tencent.wework:id/hn1",
        cls = TEXT_VIEW,
    )
    val ownerNode: FakeExecutorNode = fakeNode(text = owner, resId = "com.tencent.wework:id/group_owner_value", cls = TEXT_VIEW)
    val announcementNode: FakeExecutorNode = fakeNode(
        text = announcement,
        resId = "com.tencent.wework:id/group_announcement_value",
        cls = TEXT_VIEW,
    )

    val memberContainer: FakeExecutorNode = fakeNode(
        cls = VIEW_GROUP,
        resId = "com.tencent.wework:id/group_member_list_container",
        scrollable = true,
    ) {
        members.forEach { add(fakeNode(text = it, resId = "com.tencent.wework:id/group_member_item", cls = TEXT_VIEW)) }
    }

    val screen: FakeScreen = FakeScreen(
        name = "group-detail-$groupName",
        root = fakeNode(cls = VIEW_GROUP) {
            add(titleLabelNode)
            add(titleNode)
            add(ownerNode)
            add(announcementNode)
            add(memberContainer)
        },
    )
}
