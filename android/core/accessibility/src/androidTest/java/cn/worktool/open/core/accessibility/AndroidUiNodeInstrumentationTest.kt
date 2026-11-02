/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility

import android.view.accessibility.AccessibilityNodeInfo
import cn.worktool.open.core.accessibility.android.AndroidUiNode
import cn.worktool.open.core.accessibility.rules.MatchSpec
import cn.worktool.open.core.accessibility.rules.SelectorDef
import cn.worktool.open.core.accessibility.rules.SelectorEngine
import cn.worktool.open.core.accessibility.tree.NodeAction
import junit.framework.Assert
import junit.framework.TestCase

/**
 * Instrumentation 冒烟（T2.4 DoD：编译通过即可；运行需连接设备/模拟器——当前未执行，如实标注）。
 *
 * 验证 AndroidUiNode 对真实框架 `AccessibilityNodeInfo` 的字段/动作映射可被规则引擎消费。
 *
 * 依赖说明：JUnit3 风格（junit.framework.TestCase，junit:junit 自带兼容层）+
 * 框架自带 runner（android.test.InstrumentationTestRunner）——不为 androidTest 引入
 * androidx.test 依赖（仓库零第三方依赖策略，android/build.gradle.kts 头注）。
 * 节点获取用 [AccessibilityNodeInfo.obtain]（无需 attach 的 View，也不依赖
 * android.test.* 可选库——后者已从 compileSdk android.jar 移除）。
 */
@Suppress("DEPRECATION")
class AndroidUiNodeInstrumentationTest : TestCase() {

    /** 空节点：默认值与动作语义（不支持任何动作 → false）。 */
    fun testWrapFrameworkNodeDefaults() {
        val node = AndroidUiNode(AccessibilityNodeInfo.obtain())
        Assert.assertNull(node.text)
        Assert.assertNull(node.className)
        Assert.assertNull(node.viewIdResourceName)
        Assert.assertEquals(0, node.childCount)
        Assert.assertNull(node.childAt(0))
        Assert.assertNull(node.parent)
        Assert.assertFalse(node.isClickable)
        Assert.assertFalse(node.isScrollable)
        Assert.assertFalse(node.isEditable)
        Assert.assertTrue(node.bounds.isEmpty)
        Assert.assertFalse(node.performAction(NodeAction.CLICK))
        Assert.assertFalse(node.performAction(NodeAction.SET_TEXT, "x"))
        Assert.assertFalse(node.performAction(NodeAction.SCROLL_FORWARD))
    }

    /** 带初值的节点：字段映射 + 引擎匹配。 */
    fun testEngineOnWrappedFrameworkNode() {
        val info = AccessibilityNodeInfo.obtain().apply {
            packageName = "cn.worktool.open"
            className = "android.widget.TextView"
            text = "消息"
            viewIdResourceName = "cn.worktool.open:id/title"
        }
        val node = AndroidUiNode(info)
        Assert.assertEquals("消息", node.text)
        Assert.assertEquals("android.widget.TextView", node.className)
        Assert.assertEquals("cn.worktool.open:id/title", node.viewIdResourceName)

        val engine = SelectorEngine()
        val hits = engine.resolveAll(
            node,
            SelectorDef("t", MatchSpec(text = "消息", className = "android.widget.TextView")),
        )
        Assert.assertEquals(1, hits.size)
        Assert.assertSame(node, hits[0])

        Assert.assertEquals(0, engine.resolveAll(node, SelectorDef("t2", MatchSpec(text = "不存在"))).size)
    }
}
