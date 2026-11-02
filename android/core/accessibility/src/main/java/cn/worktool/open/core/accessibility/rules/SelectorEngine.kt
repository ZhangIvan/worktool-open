/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 节点定位规则引擎（T2.4）：按 [SelectorDef] 在 [UiNode] 树中解析目标节点，
 * 并提供节点树缩进文本渲染（调试页 D1 真机定位用）。
 *
 * 纯 Kotlin 实现，JVM 单测覆盖（docs/03 T2.4：选择器匹配矩阵 ≥15 例）。
 */
class SelectorEngine {

    /** 解析选择器的全部命中（多锚点各产出一条；不做去重，同节点可能因多锚点重复出现）。 */
    fun resolveAll(root: UiNode, selector: SelectorDef): List<UiNode> {
        val anchors = preOrder(root).filter { selector.match.matches(it) }
        return anchors.mapNotNull { anchor ->
            var cur: UiNode = anchor
            for (step in selector.chain) {
                cur = stepFrom(cur, step) ?: return@mapNotNull null
            }
            cur
        }
    }

    /** 第一个命中（执行器常用形态）。 */
    fun resolveFirst(root: UiNode, selector: SelectorDef): UiNode? = resolveAll(root, selector).firstOrNull()

    /** 按规则 ID 从配置解析（配置缺 ID 时抛 [RuleConfigException]）。 */
    fun resolveById(root: UiNode, config: RuleConfig, selectorId: String): List<UiNode> {
        val selector = config.selector(selectorId)
            ?: throw RuleConfigException("配置中不存在选择器 id='$selectorId'")
        return resolveAll(root, selector)
    }

    /** 单步平移；失败返回 null。 */
    private fun stepFrom(node: UiNode, step: ChainStep): UiNode? {
        return when (step.relation) {
            ChainRelation.CHILD ->
                step.match?.let { m ->
                    (0 until node.childCount).asSequence()
                        .map(node::childAt).filterNotNull()
                        .firstOrNull { m.matches(it) }
                } ?: node.childAt(step.index ?: 0)
            ChainRelation.SIBLING -> {
                val parent = node.parent ?: return null
                if (step.match != null) {
                    (0 until parent.childCount).asSequence()
                        .map(parent::childAt).filterNotNull()
                        .firstOrNull { it !== node && step.match.matches(it) }
                } else {
                    val offset = step.offset ?: 1
                    if (offset == 0) return null // 自引用无意义，配置语义错误
                    val selfIndex = (0 until parent.childCount).firstOrNull { parent.childAt(it) === node }
                        ?: return null
                    val target = selfIndex + offset
                    if (target < 0 || target >= parent.childCount) null else parent.childAt(target)
                }
            }
        }
    }

    /** 先序 DFS 序列（含根；防御环/超深：节点数上限 [MAX_TRAVERSE_NODES]）。 */
    private fun preOrder(root: UiNode): List<UiNode> {
        val out = ArrayList<UiNode>(64)
        val stack = ArrayDeque<UiNode>()
        stack.addLast(root)
        while (stack.isNotEmpty()) {
            if (out.size >= MAX_TRAVERSE_NODES) break
            val n = stack.removeLast()
            out += n
            for (i in n.childCount - 1 downTo 0) {
                n.childAt(i)?.let { stack.addLast(it) }
            }
        }
        return out
    }

    /**
     * 节点树缩进渲染（调试页 dump 用）。
     * 行格式：`<缩进>[#i ]className text=".." desc=".." res=.. [C][S][E] [l,t,r,b]`
     * （`#i` 为父节点内下标，根节点不标。）
     * 深度/节点数设限（超限输出一行「截断」标记），避免巨树卡顿调试页。
     */
    fun dumpTree(root: UiNode, maxDepth: Int = 50, maxNodes: Int = 2000): String {
        val sb = StringBuilder()
        var truncated = false
        fun walk(node: UiNode, depth: Int, indexInParent: Int) {
            if (truncated) return
            if (depth > maxDepth || sb.lineCount() >= maxNodes) {
                truncated = true
                sb.append("  ".repeat(depth))
                    .append("…（达到 maxNodes=").append(maxNodes)
                    .append(" 或 maxDepth=").append(maxDepth).append(" 截断）\n")
                return
            }
            sb.append("  ".repeat(depth)).append(describeNode(node, indexInParent)).append('\n')
            for (i in 0 until node.childCount) {
                if (truncated) return
                node.childAt(i)?.let { walk(it, depth + 1, i) }
            }
        }
        walk(root, 0, -1)
        return sb.toString().trimEnd('\n')
    }

    /** 单节点单行描述（试掷结果与节点树共用同一格式）。 */
    fun describeNode(node: UiNode, indexInParent: Int = -1): String {
        val sb = StringBuilder()
        if (indexInParent >= 0) sb.append('#').append(indexInParent).append(' ')
        sb.append(node.className ?: "(null)")
        node.text?.let { sb.append(" text=\"").append(it.replace("\n", "\\n")).append('"') }
        node.contentDescription?.let { sb.append(" desc=\"").append(it.replace("\n", "\\n")).append('"') }
        node.viewIdResourceName?.let { sb.append(" res=").append(it) }
        sb.append(" [")
        if (node.isClickable) sb.append('C')
        if (node.isScrollable) sb.append('S')
        if (node.isEditable) sb.append('E')
        sb.append("] ").append(node.bounds)
        return sb.toString()
    }

    private fun CharSequence.lineCount(): Int = count { it == '\n' }

    companion object {
        private const val MAX_TRAVERSE_NODES = 5000
    }
}
