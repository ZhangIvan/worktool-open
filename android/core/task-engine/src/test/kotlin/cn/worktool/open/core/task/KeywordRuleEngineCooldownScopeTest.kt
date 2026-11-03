/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-39 关键词规则引擎增强：cooldown 作用域（RULE / RULE_AND_ROOM）、
 * 多规则优先级正交性、配置解析（新字段 + 严格性保持）。
 */
class KeywordRuleEngineCooldownScopeTest {

    private var now = 1_000_000L
    private val clock = { now }
    private val engine = KeywordRuleEngine(clock)

    private fun message(room: String?, text: String = "价格") = MessageEvent(
        roomType = 1,
        textType = 1,
        targetRoom = room,
        senderNames = listOf("张三"),
        text = text,
        eventTimeMillis = now,
    )

    private fun rule(
        id: String,
        cooldown: Long,
        scope: CooldownScope = CooldownScope.RULE,
        priority: Int = 0,
        keyword: String = "价格",
    ) = KeywordRule(
        ruleId = id,
        priority = priority,
        match = KeywordMatch(keywords = listOf(keyword)),
        action = SendMessageAction(content = "回复:$id"),
        cooldownMillis = cooldown,
        cooldownScope = scope,
    )

    /** #1 默认 RULE 作用域：A 会话触发后 B 会话也被冷却（原行为保持，不回归）。 */
    @Test
    fun `默认 RULE - 跨会话互相抑制（原行为保持）`() {
        val r = rule("kr", cooldown = 60_000)
        assertEquals(1, engine.evaluate(message("会话A"), listOf(r)).size)
        now += 10_000
        assertTrue("全局水位抑制 B 会话", engine.evaluate(message("会话B"), listOf(r)).isEmpty())
    }

    /** #2 RULE_AND_ROOM：A 会话触发不冷却 B 会话（跨会话误抑制修复）。 */
    @Test
    fun `RULE_AND_ROOM - 他会话不受本会话冷却影响`() {
        val r = rule("kr", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        assertEquals(1, engine.evaluate(message("会话A"), listOf(r)).size)
        now += 10_000
        assertEquals(1, engine.evaluate(message("会话B"), listOf(r)).size)
    }

    /** #3 RULE_AND_ROOM：同一会话窗口内仍被抑制（防刷屏语义不放松）。 */
    @Test
    fun `RULE_AND_ROOM - 同会话窗口内抑制`() {
        val r = rule("kr", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        assertEquals(1, engine.evaluate(message("会话A"), listOf(r)).size)
        now += 30_000
        assertTrue(engine.evaluate(message("会话A"), listOf(r)).isEmpty())
    }

    /** #4 RULE_AND_ROOM：同会话窗口过后恢复触发。 */
    @Test
    fun `RULE_AND_ROOM - 同会话窗口过后恢复`() {
        val r = rule("kr", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        engine.evaluate(message("会话A"), listOf(r))
        now += 60_001
        assertEquals(1, engine.evaluate(message("会话A"), listOf(r)).size)
    }

    /** #5 会话名 null 归并全局桶：两个 null 会话共享水位（不误判为不同桶）。 */
    @Test
    fun `RULE_AND_ROOM - null 会话归并全局桶`() {
        val r = rule("kr", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        assertEquals(1, engine.evaluate(message(null), listOf(r)).size)
        now += 1_000
        assertTrue(engine.evaluate(message(null), listOf(r)).isEmpty())
        // 与具名会话桶独立
        assertEquals(1, engine.evaluate(message("会话A"), listOf(r)).size)
    }

    /** #6 cooldown=0：任何作用域均无抑制。 */
    @Test
    fun `零冷却 - 不受作用域影响`() {
        val a = rule("kr-a", cooldown = 0, scope = CooldownScope.RULE)
        val b = rule("kr-b", cooldown = 0, scope = CooldownScope.RULE_AND_ROOM)
        repeat(3) {
            assertEquals(2, engine.evaluate(message("会话A"), listOf(a, b)).size)
            now += 1
        }
    }

    /** #7 作用域按规则独立：同引擎内 RULE 规则被抑制时 RULE_AND_ROOM 规则照常触发。 */
    @Test
    fun `混合作用域 - 水位按规则独立`() {
        val global = rule("kr-global", cooldown = 60_000, scope = CooldownScope.RULE)
        val perRoom = rule("kr-room", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        engine.evaluate(message("会话A"), listOf(global, perRoom))
        now += 10_000
        // B 会话：global 被抑制、perRoom 放行
        val hits = engine.evaluate(message("会话B"), listOf(global, perRoom))
        assertEquals(listOf("kr-room"), hits.map { it.rule.ruleId })
    }

    /** #8 resetCooldowns 清空全部作用域水位。 */
    @Test
    fun `重置水位 - 两种作用域一并清空`() {
        val a = rule("kr-a", cooldown = 60_000)
        val b = rule("kr-b", cooldown = 60_000, scope = CooldownScope.RULE_AND_ROOM)
        engine.evaluate(message("会话A"), listOf(a, b))
        now += 1
        engine.resetCooldowns()
        assertEquals(2, engine.evaluate(message("会话A"), listOf(a, b)).size)
    }

    /** #9 多规则优先级：cooldown 作用域不影响 priority 降序稳定输出。 */
    @Test
    fun `多规则优先级 - 降序稳定且与作用域正交`() {
        val low = rule("kr-low", cooldown = 0, priority = 10)
        val high = rule("kr-high", cooldown = 0, priority = 100, scope = CooldownScope.RULE_AND_ROOM)
        val mid = rule("kr-mid", cooldown = 0, priority = 50)
        val hits = engine.evaluate(message("会话A"), listOf(low, mid, high))
        assertEquals(listOf("kr-high", "kr-mid", "kr-low"), hits.map { it.rule.ruleId })
        assertEquals(List(3) { MessageTypes.SEND_MESSAGE }, hits.map { it.intent.commandType })
    }

    /** #10 配置解析：新字段 cooldownScope / decision 可解析且默认值保持原语义。 */
    @Test
    fun `配置解析 - 新字段合法且默认兼容`() {
        val json = """
            {
              "keywordRules": [{
                "ruleId": "kr", "priority": 100, "cooldownMillis": 60000, "cooldownScope": "RULE_AND_ROOM",
                "match": {"keywords": ["价格"]},
                "action": {"type": "SEND_MESSAGE", "content": "回复"}
              }],
              "friendAcceptRules": [{
                "ruleId": "fr", "decision": "REJECT",
                "match": {"namePatterns": ["中介"]},
                "action": {"type": "PASS_FRIEND"}
              }]
            }
        """.trimIndent()
        val config = RuleConfigParser.parseRules(json)
        assertEquals(CooldownScope.RULE_AND_ROOM, config.keywordRules.single().cooldownScope)
        assertEquals(FriendDecisionPolicy.REJECT, config.friendAcceptRules.single().decision)

        // 缺省字段=默认值（旧配置零改动兼容）
        val legacy = RuleConfigParser.parseRules(
            """
            { "keywordRules": [{ "ruleId": "k1", "match": {"keywords": ["x"]},
                 "action": {"type": "SEND_MESSAGE", "content": "r"} }],
              "friendAcceptRules": [{ "ruleId": "f1", "action": {"type": "PASS_FRIEND"} }] }
            """.trimIndent(),
        )
        assertEquals(CooldownScope.RULE, legacy.keywordRules.single().cooldownScope)
        assertEquals(FriendDecisionPolicy.ACCEPT, legacy.friendAcceptRules.single().decision)
    }

    /** #11 严格性保持：未知字段仍拒绝（strict，配置漂移在校验期暴露）。 */
    @Test
    fun `配置解析 - 未知字段仍拒绝`() {
        val bad = """
            { "keywordRules": [{ "ruleId": "kr", "cooldownScopeX": "RULE",
                 "match": {"keywords": ["x"]}, "action": {"type": "SEND_MESSAGE", "content": "r"} }] }
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(bad) }
    }

    /** #12 非法枚举值拒绝（拼错的作用域值在校验期暴露）。 */
    @Test
    fun `配置解析 - 非法作用域枚举值拒绝`() {
        val bad = """
            { "keywordRules": [{ "ruleId": "kr", "cooldownScope": "PER_ROOM",
                 "match": {"keywords": ["x"]}, "action": {"type": "SEND_MESSAGE", "content": "r"} }] }
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(bad) }
    }
}
