/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.fake.FakeUiNode
import cn.worktool.open.core.accessibility.tree.NodeRect
import cn.worktool.open.core.accessibility.tree.UiNode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 选择器执行加固（T4.4，docs/11 B1 负例验证）：零匹配拒绝（child0 回退移除）、
 * sibling 同一性检查（不同 wrapper 同底层节点不误点）、多命中默认拒绝（不取首个敏感目标）。
 */
class SelectorEngineStrictTest {

    private val engine = SelectorEngine()

    /** 双胞胎节点：值域全同但对象不同（模拟同底层节点重复包装 / 同文案多目标）。 */
    private fun twin(text: String, top: Int) =
        FakeUiNode(text = text, className = "android.widget.Button", bounds = NodeRect(100, top, 240, top + 80))

    private fun friendRequestRow(
        name: String,
        acceptTop: Int,
    ): FakeUiNode {
        val row = FakeUiNode(className = "android.view.ViewGroup", bounds = NodeRect(0, acceptTop - 40, 1080, acceptTop + 120))
        row.add(FakeUiNode(text = name, className = "android.widget.TextView", bounds = NodeRect(40, acceptTop, 300, acceptTop + 60)))
        row.add(twin("接受", acceptTop))
        return row
    }

    private fun mmContactsScreen(): FakeUiNode {
        val root = FakeUiNode(className = "android.widget.FrameLayout", bounds = NodeRect(0, 0, 1080, 2400))
        root.add(FakeUiNode(text = "通讯录", className = "android.widget.TextView", bounds = NodeRect(40, 80, 300, 140)))
        root.add(friendRequestRow("王五", 200))
        root.add(friendRequestRow("赵六", 500))
        return root
    }

    // ---- ① 零匹配拒绝（child0 回退移除） ----

    /** #1 child 步无谓词无 index：旧实现兜底取 0 号子节点（误点），现必须零产出。 */
    @Test
    fun `零匹配拒绝 - child 无 match 无 index 不再兜底 child0`() {
        val row = friendRequestRow("王五", 200)
        val hits = engine.resolveAll(
            row,
            SelectorDef(
                id = "t",
                match = MatchSpec(text = "不存在的锚点文案"),
                chain = listOf(ChainStep(ChainRelation.CHILD)),
            ),
        )
        assertTrue("锚点本身零命中即无产出", hits.isEmpty())
    }

    /** #2 锚点命中但 child 步无 match 无 index：同样拒绝（不得静默滑到 0 号子节点）。 */
    @Test
    fun `零匹配拒绝 - 锚点命中后 child 步拒绝缺省 index`() {
        val row = friendRequestRow("王五", 200)
        val hits = engine.resolveAll(
            row,
            SelectorDef(
                id = "t",
                match = MatchSpec(className = "android.view.ViewGroup"),
                chain = listOf(ChainStep(ChainRelation.CHILD)),
            ),
        )
        assertTrue("child 步缺 index/match = 链步失败（不再取 0 号子节点）", hits.isEmpty())
    }

    /** #3 严格入口对 child0 形态给出显式 Invalid（调用方可区分「目标缺失」与「选择器非法」）。 */
    @Test
    fun `零匹配拒绝 - resolveStrict 返回 Invalid 并说明原因`() {
        val strict = engine.resolveStrict(
            friendRequestRow("王五", 200),
            SelectorDef(
                id = "t",
                match = MatchSpec(text = "王五"),
                chain = listOf(ChainStep(ChainRelation.CHILD)),
            ),
        )
        assertTrue(strict is StrictMatch.Invalid)
        assertTrue((strict as StrictMatch.Invalid).reason.contains("child0"))
    }

    /** #4 解析器在配置层拒绝该形态（配置漂移在装载期暴露，而非运行期静默拒绝）。 */
    @Test
    fun `零匹配拒绝 - RuleConfigParser 拒绝 child 无 index 配置`() {
        val bad = """
            { "version": 1, "selectors": [
              { "id": "x.a", "match": { "text": "王五" }, "chain": [ { "rel": "child" } ] }
            ] }
        """.trimIndent()
        try {
            RuleConfigParser.parse(bad)
            throw AssertionError("expected RuleConfigException")
        } catch (e: RuleConfigException) {
            assertTrue(e.message!!.contains("child0"))
        }
    }

    // ---- ② sibling 同一性检查 ----

    /** #5 sibling-by-match：不同 wrapper 包装的同一底层节点不算兄弟目标（旧 !== 检查失效场景）。 */
    @Test
    fun `sibling 同一性 - 不同 wrapper 的同值节点不被当作兄弟误点`() {
        // 锚点=行内「接受」按钮 A；父节点 children 里另挂一个「同一底层节点」的重复 wrapper B
        val row = FakeUiNode(className = "android.view.ViewGroup")
        val accept = twin("接受", 200)
        val acceptDuplicateWrapper = twin("接受", 200) // 同值不同对象：旧实现 !== 会误判为兄弟
        val other = twin("接受", 900)
        row.add(accept)
        row.add(acceptDuplicateWrapper)
        row.add(other)

        val hits = engine.resolveAll(
            accept,
            SelectorDef(
                id = "t",
                match = MatchSpec(text = "接受"),
                chain = listOf(ChainStep(ChainRelation.SIBLING, match = MatchSpec(text = "接受"))),
            ),
        )
        // 唯一合法兄弟=other（bounds 不同=不同目标）；重复 wrapper 因 identityKey 相同被排除
        assertEquals(1, hits.size)
        assertEquals(other, hits.single())
    }

    /** #6 sibling-by-offset：锚点与父 children 是不同 wrapper 时，offset 定位仍按身份键正确计算。 */
    @Test
    fun `sibling 同一性 - wrapper 不一致时 offset 平移仍命中正确节点`() {
        val row = FakeUiNode(className = "android.view.ViewGroup")
        val name = FakeUiNode(text = "王五", className = "android.widget.TextView")
        val preview = FakeUiNode(text = "你好，想谈合作", className = "android.widget.TextView")
        row.add(name)
        row.add(preview)

        // 用同值新 wrapper 作锚点（模拟真机二次查询 AccessibilityNodeInfo）：
        // parent 指向同一父，但不出现在父 children 里（正是 wrapper 重复的真实形态）。
        val anchorWrapper = FakeUiNode(text = "王五", className = "android.widget.TextView")
        anchorWrapper.parent = row
        val hits = engine.resolveAll(
            anchorWrapper,
            SelectorDef(
                id = "t",
                match = MatchSpec(text = "王五"),
                chain = listOf(ChainStep(ChainRelation.SIBLING, offset = 1)),
            ),
        )
        assertEquals(listOf("你好，想谈合作"), hits.map { it.text })
    }

    /** #7 identityKey：值域相同 → 同键；值域任一差异 → 不同键（键的区分力）。 */
    @Test
    fun `sibling 同一性 - identityKey 值语义`() {
        val a: UiNode = twin("接受", 200)
        val b: UiNode = twin("接受", 200)
        val c: UiNode = twin("接受", 900) // bounds 不同
        val d: UiNode = twin("回复", 200) // text 不同
        assertEquals(a.identityKey(), b.identityKey())
        assertNotEquals(a.identityKey(), c.identityKey())
        assertNotEquals(a.identityKey(), d.identityKey())
    }

    // ---- ③ 多命中默认拒绝 ----

    /** #8 多命中（两个不同「接受」按钮）→ Ambiguous，不得取首个敏感目标。 */
    @Test
    fun `多命中默认拒绝 - 两个不同接受按钮返回 Ambiguous`() {
        val strict = engine.resolveStrict(
            mmContactsScreen(),
            SelectorDef(id = "wechat.contacts.friend_accept", match = MatchSpec(text = "接受", className = "android.widget.Button")),
        )
        assertTrue(strict is StrictMatch.Ambiguous)
        assertEquals(2, (strict as StrictMatch.Ambiguous).matchCount)
    }

    /** #9 唯一命中 → Hit（严格路径正常工作）。 */
    @Test
    fun `多命中默认拒绝 - 唯一命中返回 Hit`() {
        val strict = engine.resolveStrict(
            mmContactsScreen(),
            SelectorDef(id = "t", match = MatchSpec(text = "王五")),
        )
        assertTrue(strict is StrictMatch.Hit)
        assertEquals("王五", (strict as StrictMatch.Hit).node.text)
    }

    /** #10 零命中 → NoMatch（拒绝操作，非 null 回退）。 */
    @Test
    fun `多命中默认拒绝 - 零命中返回 NoMatch`() {
        val strict = engine.resolveStrict(
            mmContactsScreen(),
            SelectorDef(id = "t", match = MatchSpec(text = "不存在的人")),
        )
        assertEquals(StrictMatch.NoMatch, strict)
    }

    /** #11 多锚点收敛到同一目标：两个同值锚点各自产出同值目标 → 按身份键去重后=Hit 而非 Ambiguous。 */
    @Test
    fun `多命中默认拒绝 - 多锚点同目标去重后为 Hit`() {
        // 同值双子树：两个「行」值域全同（同 bounds=同屏位），各自 0 号子节点也同值
        // ——真机语义=同一节点的重复包装，不是两个可点目标。
        val root = FakeUiNode(className = "android.widget.FrameLayout")
        val rowA = FakeUiNode(className = "android.view.ViewGroup", bounds = NodeRect(0, 200, 1080, 400))
        rowA.add(twin("王五", 220))
        val rowB = FakeUiNode(className = "android.view.ViewGroup", bounds = NodeRect(0, 200, 1080, 400))
        rowB.add(twin("王五", 220))
        root.add(rowA)
        root.add(rowB)

        val strict = engine.resolveStrict(
            root,
            SelectorDef(
                id = "t",
                match = MatchSpec(className = "android.view.ViewGroup"),
                chain = listOf(ChainStep(ChainRelation.CHILD, index = 0)),
            ),
        )
        assertTrue("同值目标去重后应唯一命中: $strict", strict is StrictMatch.Hit)
    }

    /** #12 敏感目标场景复验：批量接受好友意图用 findAll（多目标合法），单点点击必须走严格拒绝。 */
    @Test
    fun `多命中默认拒绝 - findAll 多目标合法与 strict 拒绝并存`() {
        val root = mmContactsScreen()
        val all = engine.resolveAll(root, SelectorDef(id = "t", match = MatchSpec(text = "接受", className = "android.widget.Button")))
        assertEquals(2, all.size) // findAll：批量路径合法
        assertTrue(engine.resolveStrict(root, SelectorDef(id = "t", match = MatchSpec(text = "接受", className = "android.widget.Button"))) is StrictMatch.Ambiguous)
    }
}
