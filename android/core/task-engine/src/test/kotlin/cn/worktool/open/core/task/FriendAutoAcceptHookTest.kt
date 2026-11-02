/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 新好友自动通过钩子（FR-15：纯端侧规则；意图 → T3.1 PASS_FRIEND 执行器）。 */
class FriendAutoAcceptHookTest {

    private val hook = FriendAutoAcceptHook()

    private fun event(
        name: String = "王五",
        leavingMsg: String? = "你好，想谈合作",
        time: Long = 1_000L,
    ) = FriendRequestEvent(friendName = name, leavingMsg = leavingMsg, eventTimeMillis = time)

    @Test
    fun `无过滤条件 - 全部通过（默认放行）`() {
        val r = FriendAcceptRule(ruleId = "fr-all", action = PassFriendAction())
        val hit = hook.evaluate(event(), listOf(r))
        assertEquals(1, hit.size)
        assertEquals(MessageTypes.PASS_ALL_FRIEND_REQUEST, hit[0].intent.commandType)
        assertEquals("fr-all", hit[0].intent.ruleId)
    }

    @Test
    fun `好友名模式 - 包含匹配命中`() {
        val r = FriendAcceptRule(
            ruleId = "fr-vip",
            match = FriendAcceptMatch(namePatterns = listOf("VIP", "客服")),
            action = PassFriendAction(),
        )
        assertTrue(hook.evaluate(event(name = "普通用户"), listOf(r)).isEmpty())
        assertEquals(1, hook.evaluate(event(name = "VIP渠道-李经理"), listOf(r)).size)
    }

    @Test
    fun `打招呼关键词过滤 - 未命中不放行`() {
        val r = FriendAcceptRule(
            ruleId = "fr-coop",
            match = FriendAcceptMatch(leavingMsgKeywords = listOf("合作")),
            action = PassFriendAction(),
        )
        assertEquals(1, hook.evaluate(event(leavingMsg = "请问可以合作吗"), listOf(r)).size)
        assertTrue(hook.evaluate(event(leavingMsg = "随便加加"), listOf(r)).isEmpty())
        assertTrue(hook.evaluate(event(leavingMsg = null), listOf(r)).isEmpty()) // 无打招呼 = 不命中
    }

    @Test
    fun `大小写开关`() {
        val r = FriendAcceptRule(
            ruleId = "fr-case",
            match = FriendAcceptMatch(namePatterns = listOf("vip"), caseSensitive = true),
            action = PassFriendAction(),
        )
        assertTrue(hook.evaluate(event(name = "VIP客户"), listOf(r)).isEmpty())
        assertEquals(1, hook.evaluate(event(name = "vip客户"), listOf(r)).size)
    }

    @Test
    fun `enabled=false 不参与`() {
        val r = FriendAcceptRule(ruleId = "fr-off", enabled = false, action = PassFriendAction())
        assertTrue(hook.evaluate(event(), listOf(r)).isEmpty())
    }

    @Test
    fun `多规则全命中逐一产出意图`() {
        val r1 = FriendAcceptRule(ruleId = "fr-a", action = PassFriendAction())
        val r2 = FriendAcceptRule(ruleId = "fr-b", match = FriendAcceptMatch(namePatterns = listOf("王")), action = PassFriendAction())
        val hit = hook.evaluate(event(name = "王五"), listOf(r1, r2))
        assertEquals(listOf("fr-a", "fr-b"), hit.map { it.rule.ruleId })
    }
}
