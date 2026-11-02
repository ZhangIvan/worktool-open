/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 新好友自动通过钩子（FR-15：纯端侧规则，无需服务端）。
 *
 * 输入新好友请求事件（好友名 + 打招呼消息，对应 spec §4.3 Friend.newFriend/leavingMsg），
 * 命中规则即产出 PASS_FRIEND 动作意图（协议 type 212，PASS_ALL_FRIEND_REQUEST）。
 * 意图交由 T3.1 的 PASS_FRIEND 执行器（[ActionExecutor]，supportedType=212）执行——
 * 本钩子不直接触达无障碍层（车道分工：task-engine 产意图，feature:* 执行）。
 *
 * 匹配语义（全部过滤项 AND）：
 * - namePatterns 空 = 不限好友名；否则好友名**包含**任一模式即命中；
 * - leavingMsgKeywords 空 = 不限打招呼内容；否则打招呼消息包含任一关键词即命中。
 */
class FriendAutoAcceptHook {

    /** 命中结果：规则 + PASS_FRIEND 动作意图。 */
    data class Matched(val rule: FriendAcceptRule, val intent: ActionIntent)

    fun evaluate(event: FriendRequestEvent, rules: List<FriendAcceptRule>): List<Matched> =
        rules.asSequence()
            .filter { it.enabled }
            .filter { r -> nameMatches(r.match, event.friendName) }
            .filter { r -> leavingMsgMatches(r.match, event.leavingMsg) }
            .map { r -> Matched(r, r.action.toIntent(r.ruleId)) }
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
