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
