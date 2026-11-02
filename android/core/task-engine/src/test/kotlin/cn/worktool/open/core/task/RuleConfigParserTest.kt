/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** 规则/本地模式配置解析（T3.3 ①：规则=JSON 配置）。 */
class RuleConfigParserTest {

    private val validRules = """
        {
          "keywordRules": [
            {
              "ruleId": "kr-price",
              "priority": 100,
              "cooldownMillis": 60000,
              "match": {
                "keywords": ["价格", "报价"],
                "mode": "ANY",
                "roomTypes": [1, 2],
                "textTypes": [1]
              },
              "action": {"type": "SEND_MESSAGE", "content": "报价请联系人工客服"}
            }
          ],
          "friendAcceptRules": [
            {
              "ruleId": "fr-coop",
              "match": {"leavingMsgKeywords": ["合作"]},
              "action": {"type": "PASS_FRIEND"}
            }
          ],
          "cronJobs": [
            {
              "jobId": "cron-morning",
              "expression": "0 9 * * 1-5",
              "action": {"type": "SEND_MESSAGE", "targetRoom": "早报群", "content": "早上好"}
            }
          ]
        }
    """.trimIndent()

    @Test
    fun `解析合法规则集 - 全类型字段映射`() {
        val config = RuleConfigParser.parseRules(validRules)
        assertEquals(1, config.keywordRules.size)
        val rule = config.keywordRules[0]
        assertEquals("kr-price", rule.ruleId)
        assertEquals(100, rule.priority)
        assertEquals(60000L, rule.cooldownMillis)
        assertEquals(KeywordMatchMode.ANY, rule.match.mode)
        assertEquals(listOf("价格", "报价"), rule.match.keywords)
        assertEquals(listOf(1, 2), rule.match.roomTypes)
        assertEquals(listOf(1), rule.match.textTypes)
        assertTrue(rule.action is SendMessageAction)
        assertEquals("报价请联系人工客服", (rule.action as SendMessageAction).content)

        assertEquals(1, config.friendAcceptRules.size)
        assertTrue(config.friendAcceptRules[0].action is PassFriendAction)

        assertEquals(1, config.cronJobs.size)
        assertEquals("0 9 * * 1-5", config.cronJobs[0].expression)
    }

    @Test
    fun `未知动作 type 被拒绝`() {
        val bad = """
            {"keywordRules": [{"ruleId": "kr-x", "match": {"keywords": ["a"]},
              "action": {"type": "DELETE_EVERYTHING"}}]}
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(bad) }
    }

    @Test
    fun `ruleId 跨规则类型重复被拒绝`() {
        val dup = """
            {
              "keywordRules": [{"ruleId": "same-id", "match": {"keywords": ["a"]},
                "action": {"type": "SEND_MESSAGE", "content": "x"}}],
              "friendAcceptRules": [{"ruleId": "same-id", "action": {"type": "PASS_FRIEND"}}]
            }
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(dup) }
    }

    @Test
    fun `空 ruleId 与空关键词被拒绝`() {
        val blankId = """
            {"keywordRules": [{"ruleId": "  ", "match": {"keywords": ["a"]},
              "action": {"type": "SEND_MESSAGE", "content": "x"}}]}
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(blankId) }

        val noKeyword = """
            {"keywordRules": [{"ruleId": "kr-y", "match": {"keywords": []},
              "action": {"type": "SEND_MESSAGE", "content": "x"}}]}
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(noKeyword) }
    }

    @Test
    fun `非法 cron 表达式在解析期被拒绝`() {
        val badCron = """
            {"cronJobs": [{"jobId": "c1", "expression": "99 * * * *",
              "action": {"type": "SEND_MESSAGE", "content": "x"}}]}
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(badCron) }
    }

    @Test
    fun `好友规则 action 非 PASS_FRIEND 被拒绝（FR-15 语义）`() {
        val bad = """
            {"friendAcceptRules": [{"ruleId": "fr-x",
              "action": {"type": "SEND_MESSAGE", "content": "hi"}}]}
        """.trimIndent()
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseRules(bad) }
    }

    @Test
    fun `本地模式配置默认 fallbackLocalScheduling 为关（02 §5·2）`() {
        val config = RuleConfigParser.parseConfig("{}")
        assertFalse(config.fallbackLocalScheduling)
        assertEquals(LocalModeConfig.DEFAULT_UNREACHABLE_THRESHOLD_MILLIS, config.unreachableThresholdMillis)
        assertFalse(config.dryRun)
    }

    @Test
    fun `未知配置字段被拒绝（strict 防漂移）`() {
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parseConfig("""{"fallbackLocalSchedulingx": true}""")
        }
    }
}
