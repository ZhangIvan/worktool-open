/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 新好友自动通过钩子（FR-15：纯端侧规则，无需服务端）。
 *
 * 输入新好友请求事件（好友名 + 打招呼消息，对应 spec §4.3 Friend.newFriend/leavingMsg；
 * 事件源两形态——企微侧无障碍事件 / 微信侧「新的朋友」页轮询（FR-56 设计 §2.4）/
 * 通讯录通知解析 [FriendRequestNotificationParser]），命中规则即产出 PASS_FRIEND
 * 动作意图（协议 type 212，PASS_ALL_FRIEND_REQUEST）。意图交由 T3.1 的 PASS_FRIEND
 * 执行器（[ActionExecutor]，supportedType=212）执行——本钩子不直接触达无障碍层
 * （车道分工：task-engine 产意图，feature:* 执行）。
 *
 * 匹配语义（全部过滤项 AND）：
 * - namePatterns 空 = 不限好友名；否则好友名**包含**任一模式即命中；
 * - leavingMsgKeywords 空 = 不限打招呼内容；否则打招呼消息包含任一关键词即命中。
 *
 * 处置（FR-15 增强：通过/拒绝）：
 * - [decide] 单决策入口（deny-wins：任一 REJECT 规则命中 → 拒绝通过，不产意图；
 *   否则取首个 ACCEPT 命中）——执行面（点「接受」/「拒绝」按钮）归 feature:* 车道；
 * - [evaluate] 批量意图入口（原语义保留）：只产出 ACCEPT 规则的意图，REJECT 规则
 *   **绝不**产出 PASS_FRIEND 意图（防黑名单规则经旧入口误放行）。
 */
class FriendAutoAcceptHook {

    /** 命中结果：规则 + PASS_FRIEND 动作意图。 */
    data class Matched(val rule: FriendAcceptRule, val intent: ActionIntent)

    /**
     * 单决策结果（FR-15 通过/拒绝）。
     * - [Accept]：产出 212 意图；
     * - [Reject]：拒绝通过（黑名单命中；无协议指令，端侧拒绝动作归执行器车道）；
     * - [NoMatch]：无规则命中（默认不动该请求）。
     */
    sealed interface Decision {
        data class Accept(val ruleId: String, val intent: ActionIntent) : Decision
        data class Reject(val ruleId: String, val reason: String) : Decision
        object NoMatch : Decision
    }

    /**
     * 单决策（deny-wins）：任一命中且 policy=REJECT 的规则 → [Decision.Reject]
     * （安全优先：黑名单与白名单同命中时拒绝）；否则首个 ACCEPT 命中 → [Decision.Accept]；
     * 全不命中 → [Decision.NoMatch]。
     */
    fun decide(event: FriendRequestEvent, rules: List<FriendAcceptRule>): Decision {
        val matches = matches(event, rules)
        matches.firstOrNull { it.decision == FriendDecisionPolicy.REJECT }?.let {
            return Decision.Reject(it.ruleId, reason = "命中拒绝规则（好友名/打招呼匹配）")
        }
        val accept = matches.firstOrNull { it.decision == FriendDecisionPolicy.ACCEPT }
            ?: return Decision.NoMatch
        return Decision.Accept(accept.ruleId, accept.action.toIntent(accept.ruleId))
    }

    /** 批量意图（原语义保留；REJECT 规则不产出意图）。 */
    fun evaluate(event: FriendRequestEvent, rules: List<FriendAcceptRule>): List<Matched> =
        matches(event, rules)
            .filter { it.decision == FriendDecisionPolicy.ACCEPT }
            .map { r -> Matched(r, r.action.toIntent(r.ruleId)) }

    /** 全部命中规则（enabled + 名字/打招呼过滤；配置顺序稳定）。 */
    private fun matches(event: FriendRequestEvent, rules: List<FriendAcceptRule>): List<FriendAcceptRule> =
        rules.asSequence()
            .filter { it.enabled }
            .filter { r -> nameMatches(r.match, event.friendName) }
            .filter { r -> leavingMsgMatches(r.match, event.leavingMsg) }
            .toList()

    private fun nameMatches(m: FriendAcceptMatch, friendName: String): Boolean {
        if (m.namePatterns.isEmpty()) return true
        return m.namePatterns.any { p ->
            if (m.caseSensitive) friendName.contains(p) else friendName.lowercase().contains(p.lowercase())
        }
    }

    private fun leavingMsgMatches(m: FriendAcceptMatch, leavingMsg: String?): Boolean {
        if (m.leavingMsgKeywords.isEmpty()) return true
        if (leavingMsg == null) return false
        return m.leavingMsgKeywords.any { k ->
            if (m.caseSensitive) leavingMsg.contains(k) else leavingMsg.lowercase().contains(k.lowercase())
        }
    }
}
