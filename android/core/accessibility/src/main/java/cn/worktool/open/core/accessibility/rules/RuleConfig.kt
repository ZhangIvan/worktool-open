/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 节点定位规则模型（T2.4，外部 JSON 配置驱动 —— docs/01 §2.3 R1 缓解：企微改版只改配置不改代码）。
 *
 * 选择器语义：
 * 1. `match` 在全树（先序 DFS，含根）中找出所有「锚点」节点；
 * 2. 锚点依次经过 `chain` 的每一步（child/sibling）平移到目标节点；
 * 3. 任一步失败（越界/无父节点/无匹配子节点或兄弟节点）则该锚点无产出。
 *
 * 匹配条件之间为 AND；字段缺省（null）= 不约束。
 */
data class MatchSpec(
    /** 精确匹配 [UiNode.text]。 */
    val text: String? = null,
    /** 子串匹配 [UiNode.text]（text 为 null 的节点不命中）。 */
    val textContains: String? = null,
    /** 精确匹配 [UiNode.viewIdResourceName]（如 `com.tencent.wework:id/chat_send`）。 */
    val resourceId: String? = null,
    /** 精确匹配 [UiNode.className]（如 `android.widget.TextView`）。 */
    val className: String? = null,
    /**
     * 精确匹配 [UiNode.contentDescription]（2026-11-04 真机校准新增）：
     * 企微部分入口为纯 desc 节点（消息页搜索钮 desc=「搜索，按钮」、聊天页
     * 「更多」desc=「更多，按钮」），text/resourceId 均空——无此字段则无法定位。
     */
    val contentDesc: String? = null,
) {
    val isEmpty: Boolean
        get() = text == null && textContains == null && resourceId == null && className == null && contentDesc == null

    /** 空条件匹配一切节点（视为合法：便于「任意子节点」链步）。 */
    fun matches(node: UiNode): Boolean {
        if (text != null && node.text != text) return false
        if (textContains != null) {
            val t = node.text ?: return false
            if (!t.contains(textContains)) return false
        }
        if (resourceId != null && node.viewIdResourceName != resourceId) return false
        if (className != null && node.className != className) return false
        if (contentDesc != null && node.contentDescription != contentDesc) return false
        return true
    }
}

/** 链步关系。 */
enum class ChainRelation { CHILD, SIBLING }

/**
 * 锚点→目标 的单步平移。
 *
 * - CHILD：`match` 优先（第一个命中的子节点）；否则取 `index`（**必填**——T4.4/docs/11 B1：
 *   原「缺省 0」零匹配回退会在目标缺失时静默误点 0 号子节点，解析期与引擎期双重拒绝）。
 * - SIBLING：`match` 优先（同父下第一个命中且**身份键不同于自身**的兄弟节点——T4.4 同一性
 *   检查：同底层节点不同 wrapper 不算兄弟目标）；否则按 `offset`（缺省 +1）。
 * - `match` 与 `index`/`offset` 互斥（解析期校验），避免歧义配置。
 */
data class ChainStep(
    val relation: ChainRelation,
    val index: Int? = null,
    val offset: Int? = null,
    val match: MatchSpec? = null,
)

/** 一条完整选择器定义（配置文件中 `selectors[]` 的元素）。 */
data class SelectorDef(
    /** 规则 ID（执行器引用键，如 `wework.chat.send`）。 */
    val id: String,
    val match: MatchSpec,
    val chain: List<ChainStep> = emptyList(),
)

/** 规则配置根对象。 */
data class RuleConfig(
    /** 配置格式版本（当前 1；不识别的版本应报错）。 */
    val version: Int,
    val selectors: List<SelectorDef>,
) {
    fun selector(id: String): SelectorDef? = selectors.firstOrNull { it.id == id }
}

/** 配置解析错误（含位置信息，便于在调试页与日志中定位）。 */
class RuleConfigException(message: String, cause: Throwable? = null) : Exception(message, cause)
