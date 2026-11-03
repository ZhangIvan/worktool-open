/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

/**
 * 个人微信内置规则包（FR-56，骨架；设计 local-private/design/wechat-rulepack.md §1.2/§1.3）。
 *
 * 红线（设计稿贯穿）：
 * 1. 本包**自研、无厂商参照**——不存在可对照的厂商选择器资产（APK 3.9.1.5 无
 *    com.tencent.mm 自动化路径，2026-11-03 双源深审）；
 * 2. **禁填 resourceId**：所有选择器只用 text/textContains/className；待 W-M5R 真机
 *    dump 出稳定资源名后回填（G2 前缀门同时静态阻断误配企微资源名）；
 * 3. 屏幕认知均为假设（A-xxx-n，见 selectors.json `_comment`）；**未过 calibrate 前执行
 *    路径整体保持 G6 关闭**（defaultEnabled=false + R7 确认流），天然不可触发。
 *
 * 选择器文件以 Kotlin 常量承载 `selectors.json` 的**文件原文**（同一 RuleConfig v1 形态、
 * 同一解析器）；外置文件/资产装载适配（需 Android Context）归 app 车道接线，本层只提供
 * 纯文本 + 解析入口，保证 JVM 单测可直接验证。
 */
object WeChatRulePack {

    const val PACK_ID = "wechat"
    const val PACKAGE_NAME = "com.tencent.mm"
    const val SELECTOR_NAMESPACE = "wechat."

    /** 风险声明文案版本（文案变更 → +1，旧 ack 失效重走确认流）。 */
    const val ACK_VERSION = 1

    /** pack.json 文件原文（设计 §1.2；schemaVersion=1，`_` 前缀键=注释约定）。 */
    const val PACK_JSON: String = """
    {
      "_comment": "FR-56 WeChat 规则包清单。schemaVersion=1（pack 层，独立于 RuleConfig.version）",
      "schemaVersion": 1,
      "packId": "wechat",
      "app": "WECHAT",
      "packageName": "com.tencent.mm",
      "selectorNamespace": "wechat.",
      "selectorsFile": "selectors.json",
      "selectorsFormat": "ruleconfig-v1",
      "defaultEnabled": false,
      "riskConfirm": {
        "required": true,
        "policyRef": "R7 + FR-56（docs/01 L104；docs/00 §7 R7）",
        "reconfirmOnMajorBump": true
      },
      "screens": [
        { "id": "wechat.home",          "assertSelectors": ["wechat.nav.home_tab"] },
        { "id": "wechat.chat",          "assertSelectors": ["wechat.chat.input"] },
        { "id": "wechat.contacts",      "assertSelectors": ["wechat.nav.contacts_tab"] },
        { "id": "wechat.contacts.new_friends", "assertSelectors": ["wechat.contacts.new_friends_title"] },
        { "id": "wechat.discover.scan", "assertSelectors": ["wechat.scan.album_entry"] }
      ],
      "commands": [203, 501, 502, 212, 243],
      "naCommands": [221, 222, 223, 208, 209, 210]
    }
    """

    /**
     * selectors.json 文件原文（设计 §1.3 首批骨架，全部待 calibrate；红线：无 resourceId）。
     */
    const val SELECTORS_JSON: String = """
    {
      "version": 1,
      "selectors": [
        {
          "id": "wechat.nav.home_tab",
          "_comment": "主界面底部「微信」Tab。A-NAV-1：底部四 Tab 假设（微信/通讯录/发现/我）；resourceId 禁填，待真机 calibrate",
          "match": { "text": "微信", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.nav.contacts_tab",
          "_comment": "主界面底部「通讯录」Tab。A-NAV-1；注意与企微「通讯录」同文案——身份区分只靠包名门（G3/G4），不靠文案",
          "match": { "text": "通讯录", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.nav.discover_tab",
          "_comment": "主界面底部「发现」Tab。A-NAV-1",
          "match": { "text": "发现", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.chat.title",
          "_comment": "聊天页标题（会话名）。A-CHAT-1",
          "match": { "className": "android.widget.TextView" },
          "chain": []
        },
        {
          "id": "wechat.chat.input",
          "_comment": "聊天页输入框。A-CHAT-2：个微输入框或为低栏提示态 EditText，需先点按展开；resourceId 待 calibrate",
          "match": { "className": "android.widget.EditText" }
        },
        {
          "id": "wechat.chat.send",
          "_comment": "聊天页发送按钮。A-CHAT-3：假设「发送」按钮仅在输入非空后出现；resourceId 待 calibrate",
          "match": { "text": "发送", "className": "android.widget.Button" }
        },
        {
          "id": "wechat.contacts.new_friends_entry",
          "_comment": "通讯录页「新的朋友」入口。A-FR15-1：假设位于通讯录页顶部功能条",
          "match": { "text": "新的朋友", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.contacts.new_friends_title",
          "_comment": "「新的朋友」页标题（屏幕身份断言用）。A-FR15-1",
          "match": { "text": "新的朋友", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.contacts.friend_accept",
          "_comment": "好友请求行「接受」按钮（批量 findAll）。A-FR15-2：假设行内按钮文案为「接受」；存在「回复」等干扰文案，匹配需精确 text",
          "match": { "text": "接受", "className": "android.widget.Button" }
        },
        {
          "id": "wechat.contacts.list_container",
          "_comment": "通讯录联系人列表滚动容器。A-502-2：resourceId 待 calibrate（当前仅能以可滚动 ViewGroup 类兜底，不稳定，禁止在未校准前投产）",
          "match": { "className": "android.view.ViewGroup" }
        },
        {
          "id": "wechat.contacts.contact_item",
          "_comment": "联系人条目名称文本。A-502-1；resourceId 待 calibrate",
          "match": { "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.contacts.group_chat_entry",
          "_comment": "501 群聊列表入口。A-501-1：【高危假设】个微通讯录可能无「群聊」全局入口（企微才有）——不存在则 501 语义降级",
          "match": { "text": "群聊", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.scan.entry",
          "_comment": "「扫一扫」入口（发现页菜单项）。A-243-2",
          "match": { "text": "扫一扫", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.scan.album_entry",
          "_comment": "扫一扫页「相册」按钮。A-243-3：假设位于扫一扫页（默认假设右上角，位置不定）；resourceId 待 calibrate",
          "match": { "text": "相册", "className": "android.widget.TextView" }
        },
        {
          "id": "wechat.join.confirm",
          "_comment": "二维码识别后「加入群聊」确认按钮。A-243-5",
          "match": { "text": "加入群聊", "className": "android.widget.Button" }
        }
      ]
    }
    """

    /** 独立解析（不经注册表；装载门 lint 归 [RulePackRegistry.register]）。 */
    fun parse(): RulePack = RulePack(
        manifest = RulePackParser.parseManifest(PACK_JSON),
        selectors = RulePackParser.parseSelectors(SELECTORS_JSON),
    )

    /** 解析并注册进 [registry]（G1/G2 全量 lint 后可用）。 */
    fun loadInto(registry: RulePackRegistry): RulePack = registry.register(PACK_JSON, SELECTORS_JSON)
}
