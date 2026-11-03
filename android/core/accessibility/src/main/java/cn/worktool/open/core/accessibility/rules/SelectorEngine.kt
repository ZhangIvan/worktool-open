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

    /**
     * 严格单目标解析（T4.4 加固路径，FR-56 规则包/敏感目标点击应使用本入口）：
     *
     * - **零匹配回退 child0 禁止**：child 链步既无 `match` 也无 `index` → [StrictMatch.Invalid]
     *   （docs/11 B1：原「兜底取 0 号子节点」会在目标缺失时静默误点首个子节点）；
     * - **多命中默认拒绝**：按 [UiNode.identityKey] 去重后仍有 >1 个不同目标 → [StrictMatch.Ambiguous]
     *   （不默认取首个敏感目标；多锚点收敛到同一节点的重复产出不算多命中）；
     * - 0 命中 → [StrictMatch.NoMatch]；恰好 1 个不同目标 → [StrictMatch.Hit]。
     *
     * 旧入口 [resolveAll]/[resolveFirst] 保持返回形态不变（多目标批量步骤 findAll 依赖），
     * 但 child0 兜底与 sibling 同一性检查为**全局修复**，对旧入口同样生效。
     */
    fun resolveStrict(root: UiNode, selector: SelectorDef): StrictMatch {
        selector.chain.forEachIndexed { i, step ->
            if (step.relation == ChainRelation.CHILD && step.match == null && step.index == null) {
                return StrictMatch.Invalid(
                    "chain[$i] child 链步既无 match 也无 index：零匹配回退 child0 已禁止（T4.4，docs/11 B1）",
                )
            }
        }
        val distinct = resolveAll(root, selector).distinctBy { it.identityKey() }
        return when (distinct.size) {
            0 -> StrictMatch.NoMatch
            1 -> StrictMatch.Hit(distinct.single())
            else -> StrictMatch.Ambiguous(distinct.size)
        }
    }

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
                } ?: node.childAt(
                    // T4.4（docs/11 B1）：child 步无谓词时必须显式 index；
                    // 原「缺省回退 0 号子节点」会在零匹配时静默误点，已改为拒绝（null）。
                    step.index ?: return null,
                )
            ChainRelation.SIBLING -> {
                val parent = node.parent ?: return null
                if (step.match != null) {
                    // T4.4 同一性检查：以值身份键排除「自身」（含不同 wrapper 包装的同一底层节点），
                    // 引用不等（!==）对多次查询产生的重复 wrapper 失效。
                    val selfKey = node.identityKey()
                    (0 until parent.childCount).asSequence()
                        .map(parent::childAt).filterNotNull()
                        .firstOrNull { it.identityKey() != selfKey && step.match.matches(it) }
                } else {
                    val offset = step.offset ?: 1
                    if (offset == 0) return null // 自引用无意义，配置语义错误
                    // 自身下标按身份键定位（非 ===）：offset 平移在 wrapper 重复/换包装时仍正确。
                    val selfKey = node.identityKey()
                    val selfIndex = (0 until parent.childCount)
                        .firstOrNull { parent.childAt(it)?.identityKey() == selfKey }
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

/**
 * 严格单目标解析结果（[SelectorEngine.resolveStrict]，T4.4）。
 *
 * 失败三态均为**拒绝操作**语义：调用方不得回退到「取首个/取 0 号」等猜测性目标。
 */
sealed interface StrictMatch {

    /** 唯一命中（按 [UiNode.identityKey] 去重后恰一个不同目标）。 */
    data class Hit(val node: UiNode) : StrictMatch

    /** 零命中（目标不存在于当前界面树）。 */
    object NoMatch : StrictMatch

    /** 多命中（去重后 >1 个不同目标；不默认取首个敏感目标，T4.4）。 */
    data class Ambiguous(val matchCount: Int) : StrictMatch

    /** 选择器定义非法（如 child 链步无 match 无 index 的 child0 兜底形态）。 */
    data class Invalid(val reason: String) : StrictMatch
}
