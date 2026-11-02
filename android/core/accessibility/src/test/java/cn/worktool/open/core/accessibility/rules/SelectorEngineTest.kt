/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.fake.FakeUiNode
import cn.worktool.open.core.accessibility.tree.NodeRect
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 选择器匹配矩阵（docs/03 T2.4 DoD：≥15 例；本类实际覆盖 match 条件 × chain 关系 22 例）。
 *
 * 测试树 fixture（构建见 [tree]）：
 * ```
 * root (LinearLayout)
 * ├── header (TextView, text="消息")
 * ├── list (RecyclerView, scrollable)
 * │   ├── item1 (ViewGroup, res=app:id/item)
 * │   │   ├── name1 (TextView, text="张三")
 * │   │   └── preview1 (TextView, text="发货单:12")
 * │   └── item2 (ViewGroup, res=app:id/item)
 * │       ├── name2 (TextView, text="李四")
 * │       └── preview2 (TextView, text="发货单:34")
 * ├── input (EditText, editable, res=app:id/input)
 * └── send (Button, clickable, text="发送", res=app:id/send)
 * ```
 */
class SelectorEngineTest {

    private val engine = SelectorEngine()

    private val name1 get() = FakeUiNode(text = "张三", className = TV)
    private val name2 get() = FakeUiNode(text = "李四", className = TV)

    private fun tree(): FakeUiNode {
        val root = FakeUiNode(className = "android.widget.LinearLayout", bounds = NodeRect(0, 0, 1080, 2400))
        val header = FakeUiNode(text = "消息", className = TV)
        val list = FakeUiNode(className = "androidx.recyclerview.widget.RecyclerView", isScrollable = true)
        val item1 = FakeUiNode(viewIdResourceName = "app:id/item", className = VG)
        val preview1 = FakeUiNode(text = "发货单:12", className = TV)
        val item2 = FakeUiNode(viewIdResourceName = "app:id/item", className = VG)
        val preview2 = FakeUiNode(text = "发货单:34", className = TV)
        val input = FakeUiNode(
            className = "android.widget.EditText",
            isEditable = true,
            viewIdResourceName = "app:id/input",
        )
        val send = FakeUiNode(
            text = "发送",
            className = "android.widget.Button",
            isClickable = true,
            viewIdResourceName = "app:id/send",
            bounds = NodeRect(900, 2200, 1060, 2320),
        )
        root.add(header)
        root.add(list)
        list.add(item1)
        item1.add(name1)
        item1.add(preview1)
        list.add(item2)
        item2.add(name2)
        item2.add(preview2)
        root.add(input)
        root.add(send)
        return root
    }

    // ---------- match 条件矩阵 ----------

    /** #1 text 精确命中单节点。 */
    @Test
    fun matchTextExact() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(text = "张三")))
        assertEquals(listOf("张三"), hits.map { it.text })
    }

    /** #2 text 精确不匹配（全树无该文本）。 */
    @Test
    fun matchTextExactMiss() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(text = "张三丰")))
        assertTrue(hits.isEmpty())
    }

    /** #3 textContains 子串命中多节点。 */
    @Test
    fun matchTextContainsMultiple() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(textContains = "发货单")))
        assertEquals(listOf("发货单:12", "发货单:34"), hits.map { it.text })
    }

    /** #4 textContains 对 text=null 的节点不命中。 */
    @Test
    fun matchTextContainsSkipsNullText() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(textContains = "")))
        // text 为 null 的节点（root/list/item/input）不参与 contains；空串包含一切非空文本
        assertEquals(listOf("消息", "张三", "发货单:12", "李四", "发货单:34", "发送"), hits.map { it.text })
    }

    /** #5 resourceId 命中同 ID 的多个节点。 */
    @Test
    fun matchResourceIdMultiple() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(resourceId = "app:id/item")))
        assertEquals(2, hits.size)
    }

    /** #6 resourceId 不匹配。 */
    @Test
    fun matchResourceIdMiss() {
        assertTrue(engine.resolveAll(tree(), SelectorDef("t", MatchSpec(resourceId = "app:id/none"))).isEmpty())
    }

    /** #7 className 命中。 */
    @Test
    fun matchClassName() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(className = "android.widget.EditText")))
        assertEquals("app:id/input", hits.single().viewIdResourceName)
    }

    /** #8 组合条件全满足 → 命中。 */
    @Test
    fun matchCombinedAllSatisfied() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef("t", MatchSpec(text = "发送", className = "android.widget.Button", resourceId = "app:id/send")),
        )
        assertEquals(1, hits.size)
    }

    /** #9 组合条件其一不满足 → 不命中（AND 语义）。 */
    @Test
    fun matchCombinedOneFails() {
        assertTrue(
            engine.resolveAll(
                tree(),
                SelectorDef("t", MatchSpec(text = "发送", className = "android.widget.TextView")),
            ).isEmpty(),
        )
    }

    /** #10 空 MatchSpec 匹配一切节点（含根）。 */
    @Test
    fun matchEmptyMatchesAll() {
        val all = engine.resolveAll(tree(), SelectorDef("t", MatchSpec()))
        assertEquals(11, all.size) // root+header+list+2item+2name+2preview+input+send
    }

    /** #11 根节点自身可被锚定。 */
    @Test
    fun matchRootItself() {
        val hits = engine.resolveAll(tree(), SelectorDef("t", MatchSpec(className = "android.widget.LinearLayout")))
        assertEquals(1, hits.size)
        assertNull(hits.single().parent)
    }

    // ---------- chain 关系矩阵 ----------

    /** #12 child 链默认 index=0。 */
    @Test
    fun chainChildDefaultIndexZero() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef("t", MatchSpec(resourceId = "app:id/item"), listOf(ChainStep(ChainRelation.CHILD))),
        )
        assertEquals(listOf("张三", "李四"), hits.map { it.text })
    }

    /** #13 child 链显式 index=1。 */
    @Test
    fun chainChildIndexOne() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(resourceId = "app:id/item"),
                listOf(ChainStep(ChainRelation.CHILD, index = 1)),
            ),
        )
        assertEquals(listOf("发货单:12", "发货单:34"), hits.map { it.text })
    }

    /** #14 child 链越界 → 无产出。 */
    @Test
    fun chainChildIndexOutOfBounds() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(resourceId = "app:id/item"),
                listOf(ChainStep(ChainRelation.CHILD, index = 5)),
            ),
        )
        assertTrue(hits.isEmpty())
    }

    /** #15 child 链按 match 取第一个命中子节点。 */
    @Test
    fun chainChildByMatch() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(resourceId = "app:id/item"),
                listOf(ChainStep(ChainRelation.CHILD, match = MatchSpec(textContains = "发货"))),
            ),
        )
        assertEquals(listOf("发货单:12", "发货单:34"), hits.map { it.text })
    }

    /** #16 sibling 链默认 offset=+1。 */
    @Test
    fun chainSiblingDefaultPlusOne() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef("t", MatchSpec(text = "张三"), listOf(ChainStep(ChainRelation.SIBLING))),
        )
        assertEquals(listOf("发货单:12"), hits.map { it.text })
    }

    /** #17 sibling 链 offset=-1。 */
    @Test
    fun chainSiblingMinusOne() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(text = "发货单:34"),
                listOf(ChainStep(ChainRelation.SIBLING, offset = -1)),
            ),
        )
        assertEquals(listOf("李四"), hits.map { it.text })
    }

    /** #18 sibling 链越界 → 无产出（send 之后无兄弟）。 */
    @Test
    fun chainSiblingOutOfBounds() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef("t", MatchSpec(resourceId = "app:id/send"), listOf(ChainStep(ChainRelation.SIBLING))),
        )
        assertTrue(hits.isEmpty())
    }

    /** #19 sibling 无父节点（根为锚）→ 无产出。 */
    @Test
    fun chainSiblingOnRootFails() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(className = "android.widget.LinearLayout"),
                listOf(ChainStep(ChainRelation.SIBLING)),
            ),
        )
        assertTrue(hits.isEmpty())
    }

    /** #20 sibling 按 match 排除自身。 */
    @Test
    fun chainSiblingByMatchExcludesSelf() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(text = "张三"),
                listOf(ChainStep(ChainRelation.SIBLING, match = MatchSpec(className = TV))),
            ),
        )
        // 不允许 sibling-by-match 命中锚点自身 → 命中右侧 preview
        assertEquals(listOf("发货单:12"), hits.map { it.text })
    }

    /** #21 多级链（child→sibling→child）跨层平移。 */
    @Test
    fun chainMultiHop() {
        val hits = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(resourceId = "app:id/item"),
                listOf(
                    ChainStep(ChainRelation.CHILD, index = 0), // → name
                    ChainStep(ChainRelation.SIBLING), // → preview
                    ChainStep(ChainRelation.CHILD), // preview 无子 → 失败
                ),
            ),
        )
        assertTrue(hits.isEmpty())
        // 两级则成功
        val ok = engine.resolveAll(
            tree(),
            SelectorDef(
                "t",
                MatchSpec(resourceId = "app:id/item"),
                listOf(
                    ChainStep(ChainRelation.CHILD, index = 0),
                    ChainStep(ChainRelation.SIBLING),
                ),
            ),
        )
        assertEquals(listOf("发货单:12", "发货单:34"), ok.map { it.text })
    }

    /** #22 resolveById + 配置联动 + 未知 id 报错。 */
    @Test
    fun resolveByIdFromConfig() {
        val config = RuleConfig(
            1,
            listOf(SelectorDef("send", MatchSpec(text = "发送"))),
        )
        val hits = engine.resolveById(tree(), config, "send")
        assertEquals("app:id/send", hits.single().viewIdResourceName)
        try {
            engine.resolveById(tree(), config, "nope")
            throw AssertionError("expected RuleConfigException")
        } catch (e: RuleConfigException) {
            assertTrue(e.message!!.contains("nope"))
        }
    }

    // ---------- dumpTree 渲染 ----------

    /** dumpTree 缩进层级渲染（调试页用）。 */
    @Test
    fun dumpTreeRendersIndentationAndFields() {
        val dump = engine.dumpTree(tree())
        val lines = dump.split('\n')
        assertEquals(11, lines.size)
        // 根顶格
        assertTrue(lines[0].startsWith("android.widget.LinearLayout"))
        // send 是 root 第 4 子 → 1 级缩进；含 flags 与 bounds
        val sendLine = lines.first { it.contains("app:id/send") }
        assertTrue(sendLine.startsWith("  "))
        assertTrue(sendLine.contains("text=\"发送\""))
        assertTrue(sendLine.contains("[C]"))
        assertTrue(sendLine.contains("[900,2200,1060,2320]"))
        // preview 是 2 级缩进
        val previewLine = lines.first { it.contains("发货单:12") }
        assertTrue(previewLine.startsWith("    "))
    }

    @Test
    fun dumpTreeRespectsMaxNodes() {
        val dump = engine.dumpTree(tree(), maxNodes = 3)
        assertEquals(4, dump.split('\n').size) // 3 行节点 + 1 行截断提示
        assertTrue(dump.contains("截断"))
    }

    companion object {
        private const val TV = "android.widget.TextView"
        private const val VG = "android.view.ViewGroup"
    }
}
