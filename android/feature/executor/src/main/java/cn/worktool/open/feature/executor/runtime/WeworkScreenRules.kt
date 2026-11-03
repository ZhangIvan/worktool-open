/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.accessibility.rules.MatchSpec
import cn.worktool.open.core.accessibility.rules.RuleConfig
import cn.worktool.open.core.accessibility.rules.RuleConfigException
import cn.worktool.open.core.accessibility.rules.SelectorDef
import cn.worktool.open.core.accessibility.rules.SelectorEngine
import cn.worktool.open.core.accessibility.tree.UiNode

/** 企微界面选择器 ID（执行器引用键；值可被外置规则配置覆盖，docs/01 R1 缓解）。 */
object WeworkSelectorIds {
    /** 会话列表项名称文本（203/207/225 定位目标会话）。 */
    const val CONVERSATION_ITEM_NAME = "wework.conversation.item_name"

    /** 聊天页标题（点击进入群设置）。 */
    const val CHAT_TITLE = "wework.chat.title"

    /** 聊天页输入框。 */
    const val CHAT_INPUT = "wework.chat.input"

    /** 聊天页发送按钮。 */
    const val CHAT_SEND = "wework.chat.send"

    /** 主界面右上「+」入口（发起群聊等）。 */
    const val HOME_CREATE_PLUS = "wework.home.create_plus"

    /** 发起群聊菜单项。 */
    const val INIT_GROUP_MENU = "wework.group.init_menu"

    /** 建群弹窗：群名输入框。 */
    const val GROUP_NAME_INPUT = "wework.group.name_input"

    /** 建群弹窗：完成/创建按钮。 */
    const val GROUP_CREATE_CONFIRM = "wework.group.create_confirm"

    /** 群设置：群公告入口。 */
    const val SETTINGS_ANNOUNCEMENT_ENTRY = "wework.group.settings.announcement_entry"

    /** 群公告编辑：输入框。 */
    const val SETTINGS_ANNOUNCEMENT_INPUT = "wework.group.settings.announcement_input"

    /** 群公告编辑：确定按钮。 */
    const val SETTINGS_ANNOUNCEMENT_CONFIRM = "wework.group.settings.announcement_confirm"

    /** 群设置：添加成员入口。 */
    const val SETTINGS_ADD_MEMBER_ENTRY = "wework.group.settings.add_member_entry"

    /** 添加成员（拉人）：确定按钮。 */
    const val SETTINGS_ADD_MEMBER_CONFIRM = "wework.group.settings.add_member_confirm"

    /** 成员详情：备注输入框。 */
    const val MEMBER_MARK_INPUT = "wework.group.member.mark_input"

    /** 成员详情：标签输入框。 */
    const val MEMBER_TAG_INPUT = "wework.group.member.tag_input"

    /** 成员详情：确定按钮。 */
    const val MEMBER_MARK_CONFIRM = "wework.group.member.mark_confirm"

    /** 主界面底部「通讯录」Tab。 */
    const val CONTACTS_TAB = "wework.nav.contacts_tab"

    /** 通讯录「新的朋友」入口。 */
    const val NEW_FRIENDS_ENTRY = "wework.contacts.new_friends_entry"

    /** 好友请求行「接受」按钮。 */
    const val FRIEND_ACCEPT_BUTTON = "wework.contacts.friend_accept"

    /** 通讯录「群聊」入口（501 群列表导航）。 */
    const val GROUP_CHAT_ENTRY = "wework.contacts.group_chat_entry"

    /** 群聊列表滚动容器（501 分页读取）。 */
    const val GROUP_LIST_CONTAINER = "wework.contacts.group_list_container"

    /** 群聊列表项群名文本（501 分页读取）。 */
    const val GROUP_LIST_ITEM = "wework.contacts.group_list_item"

    /** 通讯录联系人列表滚动容器（502 分页读取）。 */
    const val CONTACTS_LIST_CONTAINER = "wework.contacts.list_container"

    /** 通讯录联系人条目名称文本（502 分页读取）。 */
    const val CONTACT_LIST_ITEM = "wework.contacts.contact_item"

    // ---- 对等计划扩展批（docs/10 §B B1~B4）选择器（占位默认同上口径，待真机校准） ----

    /** 聊天页消息气泡内容文本（204/205/226 定位目标消息）。 */
    const val CHAT_MESSAGE_TEXT = "wework.chat.message_text"

    /** 聊天页「+」面板入口（208/209 微盘、221 待办入口）。 */
    const val CHAT_PLUS_ENTRY = "wework.chat.plus_entry"

    /** 长按消息菜单「回复」项（204）。 */
    const val CHAT_MENU_REPLY = "wework.chat.menu.reply"

    /** 长按消息菜单「转发」项（205/227/228）。 */
    const val CHAT_MENU_RELAY = "wework.chat.menu.relay"

    /** 长按消息菜单「撤回」项（226）。 */
    const val CHAT_MENU_RECALL = "wework.chat.menu.recall"

    /** 长按消息菜单「多选」项（227/228 多条勾选）。 */
    const val CHAT_MENU_MULTI_SELECT = "wework.chat.menu.multi_select"

    /** 转发/合并转发接收人搜索输入框。 */
    const val RELAY_PICKER_INPUT = "wework.relay.picker_input"

    /** 转发接收人条目名称文本。 */
    const val RELAY_PICKER_ITEM = "wework.relay.picker_item"

    /** 转发/多选确认（发送）按钮。 */
    const val RELAY_PICKER_CONFIRM = "wework.relay.picker_confirm"

    /** 消息页顶部搜索入口（219/244/512/515/213/234 搜索定位）。 */
    const val HOME_SEARCH_ENTRY = "wework.home.search_entry"

    /** 搜索页输入框。 */
    const val SEARCH_INPUT = "wework.search.input"

    /** 搜索结果条目名称文本。 */
    const val SEARCH_RESULT_ITEM = "wework.search.result_item"

    /** 群设置「解散群聊」入口（219）。 */
    const val SETTINGS_DISMISS_ENTRY = "wework.group.settings.dismiss_entry"

    /** 群设置「退出群聊」入口（244）。 */
    const val SETTINGS_EXIT_ENTRY = "wework.group.settings.exit_entry"

    /** 通用确认弹窗「确定」按钮。 */
    const val DIALOG_CONFIRM = "wework.dialog.confirm"

    /** 删除类确认弹窗「删除」按钮（234/236）。 */
    const val DIALOG_DELETE_CONFIRM = "wework.dialog.delete_confirm"

    /** 群设置「群二维码」入口（504）。 */
    const val SETTINGS_GROUP_QRCODE_ENTRY = "wework.group.settings.qrcode_entry"

    /** 群设置页群名标题文本（214/512/515 读真实群名）。 */
    const val SETTINGS_GROUP_NAME_TITLE = "wework.group.settings.name_title"

    /** 群设置「群主」值文本（214/512）。 */
    const val SETTINGS_GROUP_OWNER = "wework.group.settings.owner_value"

    /** 群成员列表滚动容器（214/512 分页读取）。 */
    const val GROUP_MEMBER_LIST_CONTAINER = "wework.group.member_list_container"

    /** 群成员条目名称文本（214/512 分页读取）。 */
    const val GROUP_MEMBER_LIST_ITEM = "wework.group.member_item"

    /** 群公告当前值文本（214/512 读取）。 */
    const val SETTINGS_ANNOUNCEMENT_VALUE = "wework.group.settings.announcement_value"

    /** 群备注当前值文本（512/515 读取）。 */
    const val SETTINGS_GROUP_REMARK_VALUE = "wework.group.settings.remark_value"

    /** 通讯录「我的客户」入口（507/236）。 */
    const val MY_CUSTOMERS_ENTRY = "wework.contacts.my_customers_entry"

    /** 联系人/成员详情页名称标题（220/213 修改语义）。 */
    const val CONTACT_DETAIL_NAME = "wework.contacts.detail_name"

    /** 联系人详情页「添加好友」按钮（220）。 */
    const val CONTACT_ADD_FRIEND_BUTTON = "wework.contacts.add_friend_button"

    /** 联系人详情页「…」更多菜单入口（234）。 */
    const val CONTACT_DETAIL_MORE = "wework.contacts.detail_more"

    /** 联系人详情菜单「删除」项（234）。 */
    const val CONTACT_DELETE_ENTRY = "wework.contacts.delete_entry"

    /** 联系人详情备注输入框（213 修改语义）。 */
    const val CONTACT_MARK_INPUT = "wework.contacts.mark_input"

    /** 联系人详情确定按钮（213 修改语义）。 */
    const val CONTACT_MARK_CONFIRM = "wework.contacts.mark_confirm"

    /** 通讯录「添加朋友」入口（213 添加语义）。 */
    const val ADD_FRIEND_ENTRY = "wework.contacts.add_friend_entry"

    /** 添加朋友「发送申请」按钮（213）。 */
    const val ADD_FRIEND_SEND_BUTTON = "wework.contacts.add_friend_send"

    /** 主界面底部「工作台」Tab（222/511 入口）。 */
    const val WORKBENCH_TAB = "wework.nav.workbench_tab"

    /** 工作台「打卡」入口（222）。 */
    const val CLOCK_ENTRY = "wework.workbench.clock_entry"

    /** 打卡页「上班打卡/下班打卡」按钮（222）。 */
    const val CLOCK_ACTION_BUTTON = "wework.clock.action_button"

    /** 工作台「日历」入口（511）。 */
    const val CALENDAR_ENTRY = "wework.workbench.calendar_entry"

    /** 日历会议条目名称文本（511）。 */
    const val MEETING_LIST_ITEM = "wework.calendar.meeting_item"

    /** 消息页左上抽屉（企业列表）入口（223/506）。 */
    const val DRAWER_ENTRY = "wework.home.drawer_entry"

    /** 抽屉企业条目名称文本（223/506）。 */
    const val CORP_LIST_ITEM = "wework.drawer.corp_item"

    /** 消息页当前企业名文本（223 切换后校验）。 */
    const val HOME_CORP_NAME = "wework.home.corp_name"

    /** 主界面底部「我」Tab（237/503 入口）。 */
    const val ME_TAB = "wework.nav.me_tab"

    /** 「我」页「设置」入口（237）。 */
    const val SETTINGS_ENTRY = "wework.me.settings_entry"

    /** 设置「存储空间」入口（237）。 */
    const val STORAGE_ENTRY = "wework.settings.storage_entry"

    /** 存储空间「清理」按钮（237）。 */
    const val CLEAN_STORAGE_BUTTON = "wework.settings.clean_button"

    /** 聊天「+」面板「微盘」入口（208/209）。 */
    const val MICRO_DISK_ENTRY = "wework.chat.micro_disk_entry"

    /** 微盘搜索输入框（208/209）。 */
    const val MICRO_DISK_SEARCH_INPUT = "wework.micro_disk.search_input"

    /** 微盘文件/图片条目名称文本（208/209）。 */
    const val MICRO_DISK_ITEM = "wework.micro_disk.item_name"

    /** 微盘「发送」按钮（208/209）。 */
    const val MICRO_DISK_SEND_BUTTON = "wework.micro_disk.send"

    /** 收款金额输入框（235 发起收款表单）。 */
    const val RECOMMEND_AMOUNT_INPUT = "wework.recommend.amount_input"

    /** 收款表单「发起收款」按钮（235）。 */
    const val RECOMMEND_COLLECT_CONFIRM = "wework.recommend.collect_confirm"

    /** 推荐表单「发送」按钮（235 名片/图册）。 */
    const val RECOMMEND_SEND_BUTTON = "wework.recommend.send"

    /** 聊天「+」面板「待办」入口（221）。 */
    const val CHAT_TODO_ENTRY = "wework.chat.todo_entry"

    /** 待办内容输入框（221）。 */
    const val TODO_CONTENT_INPUT = "wework.todo.content_input"

    /** 待办「添加」按钮（221）。 */
    const val TODO_SUBMIT = "wework.todo.submit"

    /** 群管理「群看板」入口（240）。 */
    const val GROUP_BOARD_ENTRY = "wework.group.board_entry"

    /** 群看板「添加」按钮（240）。 */
    const val GROUP_BOARD_ADD = "wework.group.board_add"

    /** 群看板网页链接输入框（240）。 */
    const val BOARD_URL_INPUT = "wework.board.url_input"

    /** 群看板「确定」按钮（240）。 */
    const val BOARD_SUBMIT = "wework.board.submit"

    /** 「+」菜单/工作台「扫一扫」入口（233）。 */
    const val SCAN_ENTRY = "wework.home.scan_entry"

    /** 扫一扫「相册」入口（233）。 */
    const val SCAN_ALBUM_ENTRY = "wework.scan.album_entry"

    /** 扫码结果「加入群聊」按钮（233/243）。 */
    const val SCAN_JOIN_BUTTON = "wework.scan.join_button"

    /** 工作台「客户群」入口（239）。 */
    const val CUSTOMER_GROUP_ENTRY = "wework.workbench.customer_group_entry"

    /** 群活码新建群名输入框（239）。 */
    const val LIVE_QR_NAME_INPUT = "wework.live_qr.name_input"

    /** 群活码生成确认按钮（239）。 */
    const val LIVE_QR_CONFIRM = "wework.live_qr.confirm"

    /** 群活码「保存到相册」按钮（239）。 */
    const val LIVE_QR_SAVE_BUTTON = "wework.live_qr.save"

    /** 多选后「逐条转发」模式按钮（227）。 */
    const val RELAY_MODE_SEQUENCE = "wework.chat.menu.relay_sequence"

    /** 多选后「合并转发」模式按钮（228）。 */
    const val RELAY_MODE_MERGE = "wework.chat.menu.relay_merge"

    /** 聊天「+」面板「文件」入口（218）。 */
    const val CHAT_FILE_ENTRY = "wework.chat.file_entry"

    /** 文件选择器条目名称文本（218）。 */
    const val FILE_PICKER_ITEM = "wework.file.picker_item"

    /** 「我」页姓名文本（503）。 */
    const val ME_NAME = "wework.me.name"

    /** 「我」页企业名文本（503）。 */
    const val ME_CORP = "wework.me.corp"

    /** 「我的客户」列表滚动容器（507）。 */
    const val MY_CUSTOMERS_LIST_CONTAINER = "wework.contacts.my_customers_container"

    /** 「我的客户」条目名称文本（507）。 */
    const val MY_CUSTOMERS_LIST_ITEM = "wework.contacts.my_customer_item"

    /** 群二维码内容文本（504/239）。 */
    const val GROUP_QRCODE_CONTENT = "wework.group.qrcode_content"
}

/**
 * 企微界面规则（T3.1）：选择器解析门面，**复用** core:accessibility 的
 * [SelectorEngine]/[MatchSpec]/[SelectorDef]（T2.4 交付），不另造匹配引擎。
 *
 * 选择器来源优先级：外置 [RuleConfig]（真机校准后由配置下发，R1：企微改版只改
 * 配置不改代码）→ [DEFAULTS] 程序化占位默认值（resourceId/text 为占位示例，
 * 与 core:accessibility `accessibility_rules.sample.json` 同一占位口径，
 * 待 D1 真机 dump 校准，W-M3R 替换）。
 */
class WeworkScreenRules(private val config: RuleConfig? = null) {

    private val engine = SelectorEngine()

    /** 按选择器 ID 解析定义（配置缺失该 ID 且无默认 = 规则错误，fail-fast）。 */
    fun def(id: String): SelectorDef =
        config?.selector(id) ?: DEFAULTS[id]
        ?: throw RuleConfigException("选择器 id='$id' 无定义（配置与内置默认均缺失）")

    /** 第一个命中节点；界面树中不存在返回 null。 */
    fun find(root: UiNode, selectorId: String): UiNode? =
        engine.resolveFirst(root, def(selectorId))

    /** 全部命中节点（多目标步骤用，如批量通过好友）。 */
    fun findAll(root: UiNode, selectorId: String): List<UiNode> =
        engine.resolveAll(root, def(selectorId))

    /** 动态精确文本匹配（会话名/成员名等运行期目标）。 */
    fun findByText(root: UiNode, text: String): UiNode? =
        engine.resolveFirst(
            root,
            SelectorDef(
                id = "dynamic.text=$text",
                match = MatchSpec(text = text, className = TEXT_VIEW),
            ),
        )

    /** 动态子串文本匹配（发送后消息上屏校验）。 */
    fun findTextContains(root: UiNode, contains: String): UiNode? =
        engine.resolveFirst(
            root,
            SelectorDef(
                id = "dynamic.textContains=$contains",
                match = MatchSpec(textContains = contains, className = TEXT_VIEW),
            ),
        )

    companion object {
        private const val TEXT_VIEW = "android.widget.TextView"
        private const val EDIT_TEXT = "android.widget.EditText"
        private const val VIEW_GROUP_PLACEHOLDER = "android.view.ViewGroup"

        /** 内置占位默认（与 fixtures 假节点树对齐；真机校准后由配置覆盖）。 */
        val DEFAULTS: Map<String, SelectorDef> = mapOf(
            WeworkSelectorIds.CONVERSATION_ITEM_NAME to sel(
                resourceId = "com.tencent.wework:id/conversation_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CHAT_TITLE to sel(
                resourceId = "com.tencent.wework:id/chat_title",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CHAT_INPUT to sel(
                resourceId = "com.tencent.wework:id/chat_input_edit",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.CHAT_SEND to sel(
                resourceId = "com.tencent.wework:id/chat_send_btn",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.HOME_CREATE_PLUS to sel(
                resourceId = "com.tencent.wework:id/home_create_plus",
            ),
            WeworkSelectorIds.INIT_GROUP_MENU to sel(
                text = "发起群聊",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.GROUP_NAME_INPUT to sel(
                resourceId = "com.tencent.wework:id/group_name_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.GROUP_CREATE_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/group_create_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_ENTRY to sel(
                text = "群公告",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_INPUT to sel(
                resourceId = "com.tencent.wework:id/announcement_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/announcement_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_ADD_MEMBER_ENTRY to sel(
                text = "添加成员",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_ADD_MEMBER_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/member_add_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.MEMBER_MARK_INPUT to sel(
                resourceId = "com.tencent.wework:id/member_mark_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.MEMBER_TAG_INPUT to sel(
                resourceId = "com.tencent.wework:id/member_tag_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.MEMBER_MARK_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/member_mark_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CONTACTS_TAB to sel(
                text = "通讯录",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.NEW_FRIENDS_ENTRY to sel(
                text = "新的朋友",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.FRIEND_ACCEPT_BUTTON to sel(
                text = "接受",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.GROUP_CHAT_ENTRY to sel(
                text = "群聊",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.GROUP_LIST_CONTAINER to sel(
                resourceId = "com.tencent.wework:id/group_list_container",
                className = VIEW_GROUP_PLACEHOLDER,
            ),
            WeworkSelectorIds.GROUP_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/group_list_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CONTACTS_LIST_CONTAINER to sel(
                resourceId = "com.tencent.wework:id/contacts_list_container",
                className = VIEW_GROUP_PLACEHOLDER,
            ),
            WeworkSelectorIds.CONTACT_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/contact_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CHAT_MESSAGE_TEXT to sel(
                resourceId = "com.tencent.wework:id/chat_message_text",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CHAT_PLUS_ENTRY to sel(text = "+"),
            WeworkSelectorIds.CHAT_MENU_REPLY to sel(text = "回复", className = TEXT_VIEW),
            WeworkSelectorIds.CHAT_MENU_RELAY to sel(text = "转发", className = TEXT_VIEW),
            WeworkSelectorIds.CHAT_MENU_RECALL to sel(text = "撤回", className = TEXT_VIEW),
            WeworkSelectorIds.CHAT_MENU_MULTI_SELECT to sel(text = "多选", className = TEXT_VIEW),
            WeworkSelectorIds.RELAY_PICKER_INPUT to sel(
                resourceId = "com.tencent.wework:id/relay_picker_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.RELAY_PICKER_ITEM to sel(
                resourceId = "com.tencent.wework:id/relay_picker_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.RELAY_PICKER_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/relay_picker_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.HOME_SEARCH_ENTRY to sel(
                resourceId = "com.tencent.wework:id/home_search_entry",
            ),
            WeworkSelectorIds.SEARCH_INPUT to sel(
                resourceId = "com.tencent.wework:id/search_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.SEARCH_RESULT_ITEM to sel(
                resourceId = "com.tencent.wework:id/search_result_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_DISMISS_ENTRY to sel(text = "解散群聊", className = TEXT_VIEW),
            WeworkSelectorIds.SETTINGS_EXIT_ENTRY to sel(text = "退出群聊", className = TEXT_VIEW),
            WeworkSelectorIds.DIALOG_CONFIRM to sel(text = "确定", className = TEXT_VIEW),
            WeworkSelectorIds.DIALOG_DELETE_CONFIRM to sel(text = "删除", className = TEXT_VIEW),
            WeworkSelectorIds.SETTINGS_GROUP_QRCODE_ENTRY to sel(text = "群二维码", className = TEXT_VIEW),
            WeworkSelectorIds.SETTINGS_GROUP_NAME_TITLE to sel(
                resourceId = "com.tencent.wework:id/group_settings_title",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_GROUP_OWNER to sel(
                resourceId = "com.tencent.wework:id/group_owner_value",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.GROUP_MEMBER_LIST_CONTAINER to sel(
                resourceId = "com.tencent.wework:id/group_member_list_container",
                className = VIEW_GROUP_PLACEHOLDER,
            ),
            WeworkSelectorIds.GROUP_MEMBER_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/group_member_item",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_ANNOUNCEMENT_VALUE to sel(
                resourceId = "com.tencent.wework:id/group_announcement_value",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.SETTINGS_GROUP_REMARK_VALUE to sel(
                resourceId = "com.tencent.wework:id/group_remark_value",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.MY_CUSTOMERS_ENTRY to sel(text = "我的客户", className = TEXT_VIEW),
            WeworkSelectorIds.CONTACT_DETAIL_NAME to sel(
                resourceId = "com.tencent.wework:id/contact_detail_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CONTACT_ADD_FRIEND_BUTTON to sel(text = "添加好友", className = TEXT_VIEW),
            WeworkSelectorIds.CONTACT_DETAIL_MORE to sel(
                resourceId = "com.tencent.wework:id/contact_detail_more",
            ),
            WeworkSelectorIds.CONTACT_DELETE_ENTRY to sel(text = "删除", className = TEXT_VIEW),
            WeworkSelectorIds.CONTACT_MARK_INPUT to sel(
                resourceId = "com.tencent.wework:id/contact_mark_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.CONTACT_MARK_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/contact_mark_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.ADD_FRIEND_ENTRY to sel(text = "添加朋友", className = TEXT_VIEW),
            WeworkSelectorIds.ADD_FRIEND_SEND_BUTTON to sel(text = "发送", className = TEXT_VIEW),
            WeworkSelectorIds.WORKBENCH_TAB to sel(text = "工作台", className = TEXT_VIEW),
            WeworkSelectorIds.CLOCK_ENTRY to sel(text = "打卡", className = TEXT_VIEW),
            WeworkSelectorIds.CLOCK_ACTION_BUTTON to sel(
                resourceId = "com.tencent.wework:id/clock_action_button",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.CALENDAR_ENTRY to sel(text = "日历", className = TEXT_VIEW),
            WeworkSelectorIds.MEETING_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/meeting_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.DRAWER_ENTRY to sel(
                resourceId = "com.tencent.wework:id/home_drawer_entry",
            ),
            WeworkSelectorIds.CORP_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/corp_list_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.HOME_CORP_NAME to sel(
                resourceId = "com.tencent.wework:id/home_corp_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.ME_TAB to sel(text = "我", className = TEXT_VIEW),
            WeworkSelectorIds.SETTINGS_ENTRY to sel(text = "设置", className = TEXT_VIEW),
            WeworkSelectorIds.STORAGE_ENTRY to sel(text = "存储空间", className = TEXT_VIEW),
            WeworkSelectorIds.CLEAN_STORAGE_BUTTON to sel(text = "清理", className = TEXT_VIEW),
            WeworkSelectorIds.MICRO_DISK_ENTRY to sel(text = "微盘", className = TEXT_VIEW),
            WeworkSelectorIds.MICRO_DISK_SEARCH_INPUT to sel(
                resourceId = "com.tencent.wework:id/micro_disk_search_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.MICRO_DISK_ITEM to sel(
                resourceId = "com.tencent.wework:id/micro_disk_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.MICRO_DISK_SEND_BUTTON to sel(text = "发送", className = TEXT_VIEW),
            WeworkSelectorIds.RECOMMEND_AMOUNT_INPUT to sel(
                resourceId = "com.tencent.wework:id/recommend_amount_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.RECOMMEND_COLLECT_CONFIRM to sel(text = "发起收款", className = TEXT_VIEW),
            WeworkSelectorIds.RECOMMEND_SEND_BUTTON to sel(text = "发送", className = TEXT_VIEW),
            WeworkSelectorIds.CHAT_TODO_ENTRY to sel(text = "待办", className = TEXT_VIEW),
            WeworkSelectorIds.TODO_CONTENT_INPUT to sel(
                resourceId = "com.tencent.wework:id/todo_content_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.TODO_SUBMIT to sel(text = "添加", className = TEXT_VIEW),
            WeworkSelectorIds.GROUP_BOARD_ENTRY to sel(text = "群看板", className = TEXT_VIEW),
            WeworkSelectorIds.GROUP_BOARD_ADD to sel(text = "添加", className = TEXT_VIEW),
            WeworkSelectorIds.BOARD_URL_INPUT to sel(
                resourceId = "com.tencent.wework:id/board_url_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.BOARD_SUBMIT to sel(text = "确定", className = TEXT_VIEW),
            WeworkSelectorIds.SCAN_ENTRY to sel(text = "扫一扫", className = TEXT_VIEW),
            WeworkSelectorIds.SCAN_ALBUM_ENTRY to sel(text = "相册", className = TEXT_VIEW),
            WeworkSelectorIds.SCAN_JOIN_BUTTON to sel(text = "加入群聊", className = TEXT_VIEW),
            WeworkSelectorIds.CUSTOMER_GROUP_ENTRY to sel(text = "客户群", className = TEXT_VIEW),
            WeworkSelectorIds.LIVE_QR_NAME_INPUT to sel(
                resourceId = "com.tencent.wework:id/live_qr_name_input",
                className = EDIT_TEXT,
            ),
            WeworkSelectorIds.LIVE_QR_CONFIRM to sel(
                resourceId = "com.tencent.wework:id/live_qr_confirm",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.LIVE_QR_SAVE_BUTTON to sel(text = "保存到相册", className = TEXT_VIEW),
            WeworkSelectorIds.RELAY_MODE_SEQUENCE to sel(text = "逐条转发", className = TEXT_VIEW),
            WeworkSelectorIds.RELAY_MODE_MERGE to sel(text = "合并转发", className = TEXT_VIEW),
            WeworkSelectorIds.CHAT_FILE_ENTRY to sel(text = "文件", className = TEXT_VIEW),
            WeworkSelectorIds.FILE_PICKER_ITEM to sel(
                resourceId = "com.tencent.wework:id/file_picker_item_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.ME_NAME to sel(
                resourceId = "com.tencent.wework:id/me_name",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.ME_CORP to sel(
                resourceId = "com.tencent.wework:id/me_corp",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.MY_CUSTOMERS_LIST_CONTAINER to sel(
                resourceId = "com.tencent.wework:id/my_customers_container",
                className = VIEW_GROUP_PLACEHOLDER,
            ),
            WeworkSelectorIds.MY_CUSTOMERS_LIST_ITEM to sel(
                resourceId = "com.tencent.wework:id/my_customer_item",
                className = TEXT_VIEW,
            ),
            WeworkSelectorIds.GROUP_QRCODE_CONTENT to sel(
                resourceId = "com.tencent.wework:id/group_qrcode_content",
                className = TEXT_VIEW,
            ),
        )

        private fun sel(
            text: String? = null,
            resourceId: String? = null,
            className: String? = null,
        ) = SelectorDef(id = "", match = MatchSpec(text, null, resourceId, className))
    }
}

/** 步骤超时参数（执行器构造注入；测试用小值确定性复现超时）。 */
data class StepTimeouts(
    /** 单步等待界面状态的最长毫秒数。 */
    val awaitNodeMs: Long = 5_000,
    /** 轮询间隔毫秒数。 */
    val pollMs: Long = 200,
)
