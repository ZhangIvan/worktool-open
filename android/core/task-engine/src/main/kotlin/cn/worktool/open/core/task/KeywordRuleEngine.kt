/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 关键词自动回复规则引擎（FR-39：端侧规则引擎，无服务端也可用）。
 *
 * 语义：
 * - 仅评估 `enabled` 规则；scope 过滤（roomTypes/textTypes/senders，null=不限）先于文本匹配；
 * - 文本匹配支持 ANY/ALL 两种模式与大小写开关（默认忽略大小写）；
 * - 命中的全部规则按 priority 降序（同级按配置顺序稳定）产出动作意图——多规则命中
 *   逐一产出，由引擎侧 (ruleId,eventHash) 去重与暂停门禁兜底；
 * - cooldown：同规则两次触发最小间隔（防刷屏/账号风控 R2）；窗口内的重复命中跳过
 *   （不重置冷却计时）。作用域 [KeywordRule.cooldownScope]（FR-39 增强）：
 *   默认 [CooldownScope.RULE] 全局计；[CooldownScope.RULE_AND_ROOM] 按规则+会话独立计
 *   （修复原实现「A 会话触发把 B 会话的回复也冷却掉」的跨会话误抑制）。
 *
 * 线程模型：由 [LocalTaskEngine] 在统一锁内调用（无独立内部锁）。
 */
class KeywordRuleEngine(private val clock: () -> Long) {

    private val lastFiredAt = HashMap<String, Long>()

    /** 命中结果：规则 + 对应动作意图。 */
    data class Matched(val rule: KeywordRule, val intent: ActionIntent)

    /** 评估一条入站消息事件，返回全部命中（已过 scope/文本/cooldown 门禁）。 */
    fun evaluate(event: MessageEvent, rules: List<KeywordRule>): List<Matched> {
        val now = clock()
        return rules.asSequence()
            .filter { it.enabled }
            .filter { r -> scopeMatches(r.match, event) }
            .filter { r -> textMatches(r.match, event.text) }
            .filter { r -> cooldownAllows(r, event, now) }
            .sortedByDescending { it.priority } // stable：同级保持配置顺序
            .map { r -> Matched(r, r.action.toIntent(r.ruleId, fallbackTargetRoom = event.targetRoom)) }
            .onEach { m -> if (m.rule.cooldownMillis > 0) lastFiredAt[cooldownKey(m.rule, event)] = now }
            .toList()
    }

    /** 清空冷却水位（规则集热更新/测试用）。 */
    fun resetCooldowns() = lastFiredAt.clear()

    private fun scopeMatches(m: KeywordMatch, event: MessageEvent): Boolean {
        m.roomTypes?.let { if (event.roomType !in it) return false }
        m.textTypes?.let { if (event.textType !in it) return false }
        m.senders?.let { allowed ->
            if (event.senderNames.none { sender -> allowed.any { eq(sender, it, m.caseSensitive) } }) return false
        }
        return true
    }

    private fun textMatches(m: KeywordMatch, text: String): Boolean {
        if (m.keywords.isEmpty()) return false
        return when (m.mode) {
            KeywordMatchMode.ANY -> m.keywords.any { contains(text, it, m.caseSensitive) }
            KeywordMatchMode.ALL -> m.keywords.all { contains(text, it, m.caseSensitive) }
        }
    }

    /** 冷却水位键：RULE=规则 ID；RULE_AND_ROOM=规则 ID+会话（会话 null 归并全局桶）。 */
    private fun cooldownKey(rule: KeywordRule, event: MessageEvent): String =
        when (rule.cooldownScope) {
            CooldownScope.RULE -> rule.ruleId
            CooldownScope.RULE_AND_ROOM -> rule.ruleId + "@" + (event.targetRoom ?: "<global>")
        }

    private fun cooldownAllows(rule: KeywordRule, event: MessageEvent, now: Long): Boolean {
        if (rule.cooldownMillis <= 0) return true
        val last = lastFiredAt[cooldownKey(rule, event)] ?: return true
        return now - last >= rule.cooldownMillis
    }

    private fun contains(text: String, keyword: String, caseSensitive: Boolean): Boolean {
        if (caseSensitive) return text.contains(keyword)
        return text.lowercase().contains(keyword.lowercase())
    }

    private fun eq(a: String, b: String, caseSensitive: Boolean): Boolean =
        if (caseSensitive) a == b else a.equals(b, ignoreCase = true)
}
