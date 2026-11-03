/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import cn.worktool.open.core.accessibility.fake.FakeUiNode
import cn.worktool.open.core.accessibility.tree.NodeRect
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 交叉 fail-closed（FR-56 验收行：WeChat 规则误配到企微会话 = fail-closed 拒执行；
 * 设计 §1.4 G3/G4/G5/G6）。
 *
 * 关键负例：企微界面树里**存在**与微信选择器同文案的节点（「通讯录」两 App 同名），
 * 也必须先在 app 门拒绝——文案永远不作为 App 身份证据（G3）。
 */
class WeChatRulePackFailClosedTest {

    private val registry = RulePackRegistry().also {
        WeChatRulePack.loadInto(it)
        WeComRulePack.loadInto(it)
    }
    private val wechat = registry.byPackId(WeChatRulePack.PACK_ID)!!
    private val wework = registry.byPackId(WeComRulePack.PACK_ID)!!

    /** 企微上下文界面树：含与微信选择器同文案的「通讯录」Tab（G3 交叉负例素材）。 */
    private fun weworkHomeTree(): FakeUiNode {
        val root = FakeUiNode(className = "android.widget.FrameLayout", bounds = NodeRect(0, 0, 1080, 2400))
        root.add(FakeUiNode(text = "消息", className = "android.widget.TextView"))
        root.add(FakeUiNode(text = "通讯录", className = "android.widget.TextView")) // 与微信同文案！
        return root
    }

    /** 微信上下文界面树（通讯录页）。 */
    private fun wechatContactsTree(): FakeUiNode {
        val root = FakeUiNode(className = "android.widget.FrameLayout", bounds = NodeRect(0, 0, 1080, 2400))
        root.add(FakeUiNode(text = "微信", className = "android.widget.TextView"))
        root.add(FakeUiNode(text = "通讯录", className = "android.widget.TextView"))
        return root
    }

    /** #1 核心负例：微信选择器在企微前台运行 → PackAppMismatchException（不试掷、不误匹配）。 */
    @Test
    fun `交叉 fail-closed - 微信选择器在企微上下文拒绝执行`() {
        val tree = weworkHomeTree() // 树里有「通讯录」同文案节点，诱惑误匹配
        val e = assertThrows(PackAppMismatchException::class.java) {
            RulePackGate.resolveSingle(
                wechat,
                RulePackGate.Context("com.tencent.wework"), // 企微前台
                enabled = true,
                root = tree,
                selectorId = "wechat.nav.contacts_tab",
            )
        }
        // 错误码经消息透出（feature:executor 车道登记同值常量后映射回执）
        assertTrue(e.message!!.contains("201007"))
        assertEquals("com.tencent.wework", e.actualPackage)
    }

    /** #2 反向交叉：企微选择器在微信前台 → 同样拒绝（隔离对称）。 */
    @Test
    fun `交叉 fail-closed - 企微选择器在微信上下文拒绝执行`() {
        assertThrows(PackAppMismatchException::class.java) {
            RulePackGate.resolveSingle(
                wework,
                RulePackGate.Context("com.tencent.mm"),
                enabled = true,
                root = wechatContactsTree(),
                selectorId = "wework.nav.contacts_tab",
            )
        }
    }

    /** #3 前台包名不可得 → 按不符处理（fail-closed，不得猜）。 */
    @Test
    fun `交叉 fail-closed - 包名不可得同样拒绝`() {
        assertThrows(PackAppMismatchException::class.java) {
            RulePackGate.assertApp(wechat, RulePackGate.Context(null))
        }
    }

    /** #4 第三方应用前台（如桌面 launcher）→ 拒绝（只认精确包名相等）。 */
    @Test
    fun `交叉 fail-closed - 无关应用前台拒绝`() {
        assertThrows(PackAppMismatchException::class.java) {
            RulePackGate.assertApp(wechat, RulePackGate.Context("com.android.launcher3"))
        }
    }

    /** #5 正向通路：微信前台 + 微信选择器 → 正常命中（fail-closed 不误伤本包执行）。 */
    @Test
    fun `交叉 fail-closed - 本包上下文正常解析`() {
        val node = RulePackGate.resolveSingle(
            wechat,
            RulePackGate.Context("com.tencent.mm"),
            enabled = true,
            root = wechatContactsTree(),
            selectorId = "wechat.nav.contacts_tab",
        )
        assertEquals("通讯录", node.text)
    }

    /** #6 G6 开关门：微信包未开启（R7 确认流未走）→ 拒执行（101002 语义）。 */
    @Test
    fun `G6 - 微信包未开启拒绝执行`() {
        val e = assertThrows(PackDisabledException::class.java) {
            RulePackGate.resolveSingle(
                wechat,
                RulePackGate.Context("com.tencent.mm"),
                enabled = false,
                root = wechatContactsTree(),
                selectorId = "wechat.nav.contacts_tab",
            )
        }
        assertTrue(e.message!!.contains("101002"))
        assertTrue(e.message!!.contains("未开启"))
    }

    /** #7 G6 不误伤企微包（riskConfirm.required=false → 开关门恒过）。 */
    @Test
    fun `G6 - 企微包不受开关门影响`() {
        RulePackGate.assertEnabled(wework, enabled = false) // required=false：不抛
    }

    /** #8 G5 屏幕断言：微信通讯录屏在微信上下文通过。 */
    @Test
    fun `G5 - 屏幕断言通过`() {
        RulePackGate.assertScreen(
            wechat,
            RulePackGate.Context("com.tencent.mm"),
            enabled = true,
            root = wechatContactsTree(),
            screenId = "wechat.contacts",
        )
    }

    /** #9 G5 屏幕断言：断言选择器在当前树零命中 → 拒绝（防走错屏继续误操作）。 */
    @Test
    fun `G5 - 屏幕断言零命中拒绝`() {
        val e = assertThrows(RulePackSelectorException::class.java) {
            RulePackGate.assertScreen(
                wechat,
                RulePackGate.Context("com.tencent.mm"),
                enabled = true,
                root = weworkHomeTree(), // 无「新的朋友」标题节点
                screenId = "wechat.contacts.new_friends",
            )
        }
        assertEquals(RulePackGate.ERROR_NODE_NOT_FOUND, e.errorCode)
    }

    /** #10 G5 屏幕断言在企微上下文 → app 门先失败（门序：G4 先于 G5 匹配）。 */
    @Test
    fun `G5 - 屏幕断言在企微上下文先被 app 门拒绝`() {
        assertThrows(PackAppMismatchException::class.java) {
            RulePackGate.assertScreen(
                wechat,
                RulePackGate.Context("com.tencent.wework"),
                enabled = true,
                root = weworkHomeTree(),
                screenId = "wechat.contacts",
            )
        }
    }

    /** #11 多命中拒绝：两个「接受」按钮 → 拒绝取首个（T4.4 接入包门）。 */
    @Test
    fun `多命中 - 包门严格解析拒绝取首个敏感目标`() {
        val root = FakeUiNode(className = "android.widget.FrameLayout")
        root.add(FakeUiNode(text = "接受", className = "android.widget.Button", bounds = NodeRect(800, 300, 950, 380)))
        root.add(FakeUiNode(text = "接受", className = "android.widget.Button", bounds = NodeRect(800, 600, 950, 680)))
        val e = assertThrows(RulePackSelectorException::class.java) {
            RulePackGate.resolveSingle(
                wechat,
                RulePackGate.Context("com.tencent.mm"),
                enabled = true,
                root = root,
                selectorId = "wechat.contacts.friend_accept",
            )
        }
        assertEquals(RulePackGate.ERROR_SELECTOR_AMBIGUOUS, e.errorCode)
    }

    /** #12 零命中拒绝：目标缺失返回错误而非猜测目标（201001 语义）。 */
    @Test
    fun `零命中 - 包门返回错误而非误配`() {
        val e = assertThrows(RulePackSelectorException::class.java) {
            RulePackGate.resolveSingle(
                wechat,
                RulePackGate.Context("com.tencent.mm"),
                enabled = true,
                root = wechatContactsTree(),
                selectorId = "wechat.chat.send",
            )
        }
        assertEquals(RulePackGate.ERROR_NODE_NOT_FOUND, e.errorCode)
    }

    /** #13 引用未定义选择器 → 显式错误（清单与选择器集一致性兜底）。 */
    @Test
    fun `引用缺失 - 未定义选择器 id 报错`() {
        val e = assertThrows(RulePackSelectorException::class.java) {
            RulePackGate.resolveSingle(
                wechat,
                RulePackGate.Context("com.tencent.mm"),
                enabled = true,
                root = wechatContactsTree(),
                selectorId = "wechat.nav.not_defined",
            )
        }
        assertTrue(e.message!!.contains("wechat.nav.not_defined"))
    }
}
