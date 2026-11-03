/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-15 增强：通讯录通知事件 → 过滤 → **通过/拒绝**。
 * 覆盖 [FriendAutoAcceptHook.decide]（deny-wins）、通知解析过滤链、引擎接线零副作用路径。
 */
class FriendAutoAcceptDecisionTest {

    private val hook = FriendAutoAcceptHook()
    private val parser = FriendRequestNotificationParser()

    private fun event(name: String = "王五", leavingMsg: String? = "你好，想谈合作") =
        FriendRequestEvent(friendName = name, leavingMsg = leavingMsg, eventTimeMillis = 1_000L)

    private fun acceptRule(id: String, namePatterns: List<String> = emptyList()) = FriendAcceptRule(
        ruleId = id,
        match = FriendAcceptMatch(namePatterns = namePatterns),
        action = PassFriendAction(),
    )

    private fun rejectRule(id: String, namePatterns: List<String> = emptyList()) = FriendAcceptRule(
        ruleId = id,
        decision = FriendDecisionPolicy.REJECT,
        match = FriendAcceptMatch(namePatterns = namePatterns),
        action = PassFriendAction(),
    )

    // ---- decide（通过/拒绝决策） ----

    /** #1 默认 ACCEPT：命中 → Accept + 212 意图。 */
    @Test
    fun `decide - 默认通过产出 212 意图`() {
        val d = hook.decide(event(), listOf(acceptRule("fr-all")))
        assertTrue(d is FriendAutoAcceptHook.Decision.Accept)
        d as FriendAutoAcceptHook.Decision.Accept
        assertEquals("fr-all", d.ruleId)
        assertEquals(MessageTypes.PASS_ALL_FRIEND_REQUEST, d.intent.commandType)
    }

    /** #2 deny-wins：REJECT 与 ACCEPT 同命中 → 拒绝（黑名单优先，安全侧）。 */
    @Test
    fun `decide - 拒绝规则与通过规则同命中时拒绝优先`() {
        val d = hook.decide(
            event(name = "中介-小王"),
            listOf(acceptRule("fr-all"), rejectRule("fr-black", namePatterns = listOf("中介"))),
        )
        assertTrue(d is FriendAutoAcceptHook.Decision.Reject)
        d as FriendAutoAcceptHook.Decision.Reject
        assertEquals("fr-black", d.ruleId)
    }

    /** #3 无命中 → NoMatch（默认不动该请求）。 */
    @Test
    fun `decide - 全不命中 NoMatch`() {
        assertEquals(
            FriendAutoAcceptHook.Decision.NoMatch,
            hook.decide(event(), listOf(acceptRule("fr-vip", namePatterns = listOf("VIP")))),
        )
    }

    /** #4 旧入口 evaluate：REJECT 规则绝不产出 PASS_FRIEND 意图（防黑名单误放行）。 */
    @Test
    fun `evaluate - 拒绝规则不产出意图`() {
        val hits = hook.evaluate(
            event(name = "中介-小王"),
            listOf(rejectRule("fr-black", namePatterns = listOf("中介")), acceptRule("fr-all")),
        )
        assertEquals(listOf("fr-all"), hits.map { it.rule.ruleId }) // 黑名单规则无意图
    }

    /** #5 多 ACCEPT 命中逐一产出（原语义保留）。 */
    @Test
    fun `evaluate - 多通过规则逐一产出`() {
        val hits = hook.evaluate(
            event(name = "王五"),
            listOf(acceptRule("fr-a"), acceptRule("fr-b", namePatterns = listOf("王"))),
        )
        assertEquals(listOf("fr-a", "fr-b"), hits.map { it.rule.ruleId })
    }

    /** #6 enabled=false 规则不参与决策（含 REJECT——停用黑名单即不拦截）。 */
    @Test
    fun `decide - 停用规则不参与`() {
        val disabled = rejectRule("fr-black", namePatterns = listOf("王")).copy(enabled = false)
        val d = hook.decide(event(name = "王五"), listOf(disabled))
        assertEquals(FriendAutoAcceptHook.Decision.NoMatch, d)
    }

    // ---- 通知过滤链（包名门 → 文案形态 → 提取） ----

    /** #7 合法微信好友请求通知 → 事件（好友名提取）。 */
    @Test
    fun `通知过滤 - 微信好友请求通知解析`() {
        val e = parser.parse(
            ContactNotification(
                packageName = "com.tencent.mm",
                text = "李经理 请求添加你为好友",
                postedAtMillis = 5_000L,
            ),
        )
        assertEquals("李经理", e!!.friendName)
        assertEquals(5_000L, e.eventTimeMillis)
        assertNull(e.leavingMsg)
    }

    /** #8 企微变体文案 + 打招呼提取（留言：xxx）。 */
    @Test
    fun `通知过滤 - 企微变体与打招呼提取`() {
        val e = parser.parse(
            ContactNotification(
                packageName = "com.tencent.wework",
                text = "张三 请求添加好友 留言：请问可以合作吗",
                postedAtMillis = 5_000L,
            ),
        )
        assertEquals("张三", e!!.friendName)
        assertEquals("请问可以合作吗", e.leavingMsg)
    }

    /** #9 包名门：非通讯录应用的通知丢弃（文案再像也不认）。 */
    @Test
    fun `通知过滤 - 无关包名丢弃`() {
        assertNull(
            parser.parse(
                ContactNotification(
                    packageName = "com.random.app",
                    text = "李经理 请求添加你为好友",
                    postedAtMillis = 5_000L,
                ),
            ),
        )
    }

    /** #10 包名不可得 = 丢弃（fail-closed，身份证据缺失不猜）。 */
    @Test
    fun `通知过滤 - 包名不可得丢弃`() {
        assertNull(
            parser.parse(
                ContactNotification(packageName = null, text = "李经理 请求添加你为好友", postedAtMillis = 5_000L),
            ),
        )
    }

    /** #11 非好友请求文案（如已添加回执）丢弃。 */
    @Test
    fun `通知过滤 - 非请求文案丢弃`() {
        assertNull(
            parser.parse(
                ContactNotification(
                    packageName = "com.tencent.mm",
                    text = "你已添加了李经理，现在可以开始聊天了",
                    postedAtMillis = 5_000L,
                ),
            ),
        )
    }

    /** #12 自定义模式集替换默认（文案漂移改配置不改代码）。 */
    @Test
    fun `通知过滤 - 模式集可配置`() {
        val custom = FriendRequestNotificationParser(
            requestPatterns = listOf(Regex("^(.+?)\\s*wants to add you")),
        )
        val e = custom.parse(
            ContactNotification(
                packageName = "com.tencent.mm",
                text = "John wants to add you as a friend",
                postedAtMillis = 1L,
            ),
        )
        assertEquals("John", e!!.friendName)
        // 默认模式不认识英文 → null
        assertNull(
            parser.parse(
                ContactNotification(
                    packageName = "com.tencent.mm",
                    text = "John wants to add you as a friend",
                    postedAtMillis = 1L,
                ),
            ),
        )
    }

    // ---- 引擎接线（通知 → 过滤 → 通过/拒绝） ----

    private class RecordingExecutor(
        override val supportedType: Int,
        var outcome: ExecOutcome = ExecOutcome.OK,
    ) : ActionExecutor {
        val calls = ArrayList<ActionIntent>()
        override fun execute(intent: ActionIntent): ExecOutcome {
            calls.add(intent)
            return outcome
        }
    }

    /** #13 引擎：通过决策 → 212 意图分发执行。 */
    @Test
    fun `引擎接线 - 通过通知分发 212`() {
        val pass = RecordingExecutor(MessageTypes.PASS_ALL_FRIEND_REQUEST)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = RuleSetConfig(friendAcceptRules = listOf(acceptRule("fr-all"))),
            pureLocalMode = true,
            clock = { 1_000L },
        )
        engine.registerExecutor(pass)
        val outcomes = engine.onContactNotification(
            ContactNotification(
                packageName = "com.tencent.mm",
                text = "李经理 请求添加你为好友",
                postedAtMillis = 1_000L,
            ),
        )
        assertEquals(1, outcomes.size)
        assertTrue(outcomes.single() is LocalTaskEngine.Outcome.Executed)
        assertEquals(1, pass.calls.size)
    }

    /** #14 引擎：拒绝决策（黑名单命中）→ 零副作用空结果。 */
    @Test
    fun `引擎接线 - 拒绝决策零副作用`() {
        val pass = RecordingExecutor(MessageTypes.PASS_ALL_FRIEND_REQUEST)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = RuleSetConfig(
                friendAcceptRules = listOf(
                    rejectRule("fr-black", namePatterns = listOf("中介")),
                    acceptRule("fr-all"),
                ),
            ),
            pureLocalMode = true,
            clock = { 1_000L },
        )
        engine.registerExecutor(pass)
        val outcomes = engine.onContactNotification(
            ContactNotification(
                packageName = "com.tencent.mm",
                text = "中介-小王 请求添加你为好友",
                postedAtMillis = 1_000L,
            ),
        )
        assertTrue(outcomes.isEmpty())
        assertTrue(pass.calls.isEmpty()) // 执行器一次都没被调
    }

    /** #15 引擎：非好友请求通知（过滤丢弃）→ 零副作用空结果。 */
    @Test
    fun `引擎接线 - 非请求通知零副作用`() {
        val pass = RecordingExecutor(MessageTypes.PASS_ALL_FRIEND_REQUEST)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = RuleSetConfig(friendAcceptRules = listOf(acceptRule("fr-all"))),
            pureLocalMode = true,
            clock = { 1_000L },
        )
        engine.registerExecutor(pass)
        val outcomes = engine.onContactNotification(
            ContactNotification(
                packageName = "com.android.systemui",
                text = "李经理 请求添加你为好友",
                postedAtMillis = 1_000L,
            ),
        )
        assertTrue(outcomes.isEmpty())
        assertTrue(pass.calls.isEmpty())
    }
}
