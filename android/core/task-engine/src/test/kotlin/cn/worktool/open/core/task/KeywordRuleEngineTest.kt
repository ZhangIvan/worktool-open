/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.RoomTypes
import cn.worktool.open.core.protocol.TextTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 关键词自动回复规则引擎（FR-39）。 */
class KeywordRuleEngineTest {

    private var now = 1_000_000L
    private val clock = { now }
    private val engine = KeywordRuleEngine(clock)

    private fun message(
        text: String,
        roomType: Int = RoomTypes.EXTERNAL_CONTACT,
        textType: Int = TextTypes.TEXT,
        senders: List<String> = listOf("张三"),
        time: Long = now,
    ) = MessageEvent(
        roomType = roomType,
        textType = textType,
        targetRoom = "会话A",
        senderNames = senders,
        text = text,
        eventTimeMillis = time,
    )

    private fun rule(
        id: String,
        vararg keywords: String,
        mode: KeywordMatchMode = KeywordMatchMode.ANY,
        caseSensitive: Boolean = false,
        roomTypes: List<Int>? = null,
        textTypes: List<Int>? = null,
        senders: List<String>? = null,
        priority: Int = 0,
        cooldown: Long = 0,
        enabled: Boolean = true,
    ) = KeywordRule(
        ruleId = id,
        enabled = enabled,
        priority = priority,
        match = KeywordMatch(
            keywords = keywords.toList(),
            mode = mode,
            caseSensitive = caseSensitive,
            roomTypes = roomTypes,
            textTypes = textTypes,
            senders = senders,
        ),
        action = SendMessageAction(content = "回复:$id"),
        cooldownMillis = cooldown,
    )

    @Test
    fun `ANY 模式 - 任一关键词命中`() {
        val r = rule("kr1", "价格", "报价")
        val hit = engine.evaluate(message("请问价格多少"), listOf(r))
        assertEquals(1, hit.size)
        assertEquals("kr1", hit[0].rule.ruleId)
        assertEquals(MessageTypes.SEND_MESSAGE, hit[0].intent.commandType)
        assertEquals("回复:kr1", hit[0].intent.content)
    }

    @Test
    fun `ANY 模式 - 全不命中返回空`() {
        val r = rule("kr1", "价格")
        assertTrue(engine.evaluate(message("今天天气不错"), listOf(r)).isEmpty())
    }

    @Test
    fun `ALL 模式 - 须全部命中`() {
        val r = rule("kr1", "价格", "发货", mode = KeywordMatchMode.ALL)
        assertTrue(engine.evaluate(message("价格怎么样"), listOf(r)).isEmpty())
        assertEquals(1, engine.evaluate(message("价格多少，什么时候发货"), listOf(r)).size)
    }

    @Test
    fun `大小写 - 默认忽略，可开敏感`() {
        val insensitive = rule("kr-i", "PRICE")
        assertEquals(1, engine.evaluate(message("what is the price?"), listOf(insensitive)).size)

        val sensitive = rule("kr-s", "PRICE", caseSensitive = true)
        assertTrue(engine.evaluate(message("what is the price?"), listOf(sensitive)).isEmpty())
        assertEquals(1, engine.evaluate(message("PRICE please"), listOf(sensitive)).size)
    }

    @Test
    fun `scope 过滤 - roomType 不在白名单跳过`() {
        val r = rule("kr1", "价格", roomTypes = listOf(RoomTypes.EXTERNAL_GROUP))
        assertTrue(engine.evaluate(message("价格", roomType = RoomTypes.EXTERNAL_CONTACT), listOf(r)).isEmpty())
        assertEquals(1, engine.evaluate(message("价格", roomType = RoomTypes.EXTERNAL_GROUP), listOf(r)).size)
    }

    @Test
    fun `scope 过滤 - textType 限定文本消息`() {
        val r = rule("kr1", "价格", textTypes = listOf(TextTypes.TEXT))
        assertTrue(engine.evaluate(message("价格", textType = TextTypes.IMAGE), listOf(r)).isEmpty())
    }

    @Test
    fun `scope 过滤 - 发送人白名单`() {
        val r = rule("kr1", "价格", senders = listOf("李四"))
        assertTrue(engine.evaluate(message("价格", senders = listOf("张三")), listOf(r)).isEmpty())
        assertEquals(1, engine.evaluate(message("价格", senders = listOf("李四")), listOf(r)).size)
    }

    @Test
    fun `enabled=false 规则不参与`() {
        val r = rule("kr1", "价格", enabled = false)
        assertTrue(engine.evaluate(message("价格"), listOf(r)).isEmpty())
    }

    @Test
    fun `cooldown - 窗口内重复命中被抑制`() {
        val r = rule("kr1", "价格", cooldown = 60_000)
        assertEquals(1, engine.evaluate(message("价格", time = now), listOf(r)).size)
        now += 30_000
        assertTrue(engine.evaluate(message("价格", time = now), listOf(r)).isEmpty())
        now += 31_000 // 距上次触发 61s ≥ 60s
        assertEquals(1, engine.evaluate(message("价格", time = now), listOf(r)).size)
    }

    @Test
    fun `多规则命中按 priority 降序稳定输出`() {
        val low = rule("kr-low", "价格", priority = 10)
        val high = rule("kr-high", "价格", priority = 100)
        val mid = rule("kr-mid", "价格", priority = 50)
        val result = engine.evaluate(message("价格"), listOf(low, mid, high))
        assertEquals(listOf("kr-high", "kr-mid", "kr-low"), result.map { it.rule.ruleId })
    }

    @Test
    fun `触发会话作为动作目标回填（回复来源会话）`() {
        val r = rule("kr1", "价格")
        val hit = engine.evaluate(message("价格"), listOf(r))
        assertEquals("会话A", hit[0].intent.targetRoom)
    }
}
