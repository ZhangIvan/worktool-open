/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

/**
 * 企业微信内置规则包（FR-56 骨架的企微侧同构镜像；设计 §1.2「wework/ 企微包同构，
 * W-M3R 校准结果迁移入此形态，另行任务」）。
 *
 * - 与微信包同一 schema（pack.json v1 + selectors.json RuleConfig v1）；
 * - 选择器为 W-M3R 校准基线的**子集占位**（resourceId 值与 feature:executor
 *   `WeworkScreenRules.DEFAULTS` 同一占位口径，全量迁移另行任务）；
 * - 企微是既有主线（非 FR-56 新风险面）：defaultEnabled=true、riskConfirm.required=false
 *   （R7 确认流仅微信包要求）。
 */
object WeComRulePack {

    const val PACK_ID = "wework"
    const val PACKAGE_NAME = "com.tencent.wework"
    const val SELECTOR_NAMESPACE = "wework."

    /** pack.json 文件原文。 */
    const val PACK_JSON: String = """
    {
      "_comment": "WeCom（企微）规则包清单。schemaVersion=1；选择器=W-M3R 校准基线子集占位（全量迁移另行任务）",
      "schemaVersion": 1,
      "packId": "wework",
      "app": "WEWORK",
      "packageName": "com.tencent.wework",
      "selectorNamespace": "wework.",
      "selectorsFile": "selectors.json",
      "selectorsFormat": "ruleconfig-v1",
      "defaultEnabled": true,
      "riskConfirm": {
        "required": false,
        "policyRef": "企微为既有主线（R7 确认流仅 FR-56 微信包要求）",
        "reconfirmOnMajorBump": false
      },
      "screens": [
        { "id": "wework.home",     "assertSelectors": ["wework.nav.message_tab"] },
        { "id": "wework.chat",     "assertSelectors": ["wework.chat.input"] },
        { "id": "wework.contacts", "assertSelectors": ["wework.nav.contacts_tab"] },
        { "id": "wework.contacts.new_friends", "assertSelectors": ["wework.contacts.new_friends_entry"] }
      ],
      "commands": [203, 212, 501, 502, 509],
      "naCommands": []
    }
    """

    /** selectors.json 文件原文（RuleConfig v1；resourceId=占位校准值，G2 前缀门锚定企微包名）。 */
    const val SELECTORS_JSON: String = """
    {
      "version": 1,
      "selectors": [
        {
          "id": "wework.nav.message_tab",
          "_comment": "主界面底部「消息」Tab（home 屏断言锚点）；占位，待全量校准迁移",
          "match": { "text": "消息", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.nav.contacts_tab",
          "_comment": "主界面底部「通讯录」Tab；注意与微信「通讯录」同文案——身份区分只靠包名门（G3/G4）",
          "match": { "text": "通讯录", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.chat.input",
          "_comment": "聊天页输入框；占位校准值",
          "match": { "resourceId": "com.tencent.wework:id/chat_input_edit", "className": "android.widget.EditText" }
        },
        {
          "id": "wework.chat.send",
          "_comment": "聊天页发送按钮；占位校准值",
          "match": { "resourceId": "com.tencent.wework:id/chat_send_btn", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.contacts.new_friends_entry",
          "_comment": "通讯录「新的朋友」入口（FR-15 导航首步）",
          "match": { "text": "新的朋友", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.contacts.friend_accept",
          "_comment": "好友请求行「接受」按钮（批量 findAll）；企微侧为 TextView 形态",
          "match": { "text": "接受", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.contacts.group_chat_entry",
          "_comment": "通讯录「群聊」入口（501 群列表导航）",
          "match": { "text": "群聊", "className": "android.widget.TextView" }
        },
        {
          "id": "wework.contacts.contact_item",
          "_comment": "通讯录联系人条目名称文本（502 分页读取）；占位校准值",
          "match": { "resourceId": "com.tencent.wework:id/contact_item_name", "className": "android.widget.TextView" }
        }
      ]
    }
    """

    fun parse(): RulePack = RulePack(
        manifest = RulePackParser.parseManifest(PACK_JSON),
        selectors = RulePackParser.parseSelectors(SELECTORS_JSON),
    )

    fun loadInto(registry: RulePackRegistry): RulePack = registry.register(PACK_JSON, SELECTORS_JSON)
}
