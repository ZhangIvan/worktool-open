/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.json.Json
import cn.worktool.open.core.accessibility.json.JsonValue
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** 规则配置解析器单测（外部 JSON → 规则模型）。 */
class RuleConfigParserTest {

    private val validConfig = """
        {
          "version": 1,
          "selectors": [
            {
              "id": "s1",
              "match": { "text": "发送", "className": "android.widget.TextView" }
            },
            {
              "id": "s2",
              "match": { "resourceId": "com.tencent.wework:id/chat_send" },
              "chain": [
                { "rel": "child", "index": 2 },
                { "rel": "sibling", "offset": -1 },
                { "rel": "child", "match": { "textContains": "备注" } }
              ]
            }
          ]
        }
    """.trimIndent()

    @Test
    fun parsesValidConfig() {
        val config = RuleConfigParser.parse(validConfig)
        assertEquals(1, config.version)
        assertEquals(2, config.selectors.size)
        val s1 = config.selector("s1")!!
        assertEquals(MatchSpec(text = "发送", className = "android.widget.TextView"), s1.match)
        assertTrue(s1.chain.isEmpty())
        val s2 = config.selector("s2")!!
        assertEquals(
            listOf(ChainRelation.CHILD, ChainRelation.SIBLING, ChainRelation.CHILD),
            s2.chain.map { it.relation },
        )
        assertEquals(2, s2.chain[0].index)
        assertEquals(-1, s2.chain[1].offset)
        assertEquals("备注", s2.chain[2].match?.textContains)
        // 缺省语义：child 无 index 默认 0、sibling 无 offset 默认 +1（引擎侧缺省，模型保持 null）
        assertEquals(null, s2.chain[0].offset)
        assertEquals(null, s2.chain[1].index)
    }

    @Test
    fun underscoreKeysAreIgnored() {
        val withComment = validConfig.replace("{\n", "{\n  \"_comment\": \"注释\",\n")
        assertEquals(2, RuleConfigParser.parse(withComment).selectors.size)
    }

    @Test
    fun rejectsUnknownKeys() {
        val typo = validConfig.replace("\"textContains\"", "\"textConains\"")
        val ex = assertThrows(RuleConfigException::class.java) { RuleConfigParser.parse(typo) }
        assertTrue(ex.message!!.contains("textConains"))
    }

    @Test
    fun rejectsBadVersion() {
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"version\": 1", "\"version\": 2"))
        }
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"version\": 1,", ""))
        }
    }

    @Test
    fun rejectsEmptySelectorsAndDuplicateIds() {
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replaceAfterLast("\"selectors\": [", "]"))
        }
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"id\": \"s2\"", "\"id\": \"s1\""))
        }
    }

    @Test
    fun rejectsChainStepViolations() {
        // rel 非法
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"rel\": \"child\", \"index\": 2", "\"rel\": \"parent\""))
        }
        // child 带 offset / sibling 带 index
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"rel\": \"child\", \"index\": 2", "\"rel\": \"child\", \"offset\": 1"))
        }
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(validConfig.replace("\"rel\": \"sibling\", \"offset\": -1", "\"rel\": \"sibling\", \"index\": 1"))
        }
        // match 与 index 互斥
        assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse(
                validConfig.replace(
                    "{ \"rel\": \"child\", \"match\": { \"textContains\": \"备注\" } }",
                    "{ \"rel\": \"child\", \"index\": 0, \"match\": { \"textContains\": \"备注\" } }",
                ),
            )
        }
    }

    @Test
    fun rejectsInvalidJsonSyntax() {
        val ex = assertThrows(RuleConfigException::class.java) {
            RuleConfigParser.parse("""{"version": 1,""")
        }
        assertTrue(ex.message!!.contains("JSON 语法错误"))
    }

    @Test
    fun parseSelectorObjectForTrial() {
        val obj = Json.parse(
            """{"id":"t","match":{"textContains":"x"},"chain":[{"rel":"sibling"}]}""",
        ) as JsonValue.JsonObject
        val def = RuleConfigParser.parseSelectorObject(obj)
        assertEquals("t", def.id)
        assertEquals(ChainRelation.SIBLING, def.chain.single().relation)
        // 单对象拒绝空 match（无 chain 时）
        val empty = Json.parse("""{"id":"t2","match":{}}""") as JsonValue.JsonObject
        assertThrows(RuleConfigException::class.java) { RuleConfigParser.parseSelectorObject(empty) }
        // 链步 match 允许空条件（任意子节点/兄弟）
        val chainEmptyMatch = Json.parse(
            """{"id":"t3","match":{"text":"x"},"chain":[{"rel":"child","match":{}}]}""",
        ) as JsonValue.JsonObject
        assertEquals(MatchSpec(), RuleConfigParser.parseSelectorObject(chainEmptyMatch).chain.single().match)
    }

    /** 随包样例（src/main/assets）必须可被解析器接受——防「样例与解析器漂移」。 */
    @Test
    fun parsesShippedSampleAssets() {
        val sample = java.io.File("src/main/assets/accessibility_rules.sample.json")
        assertTrue("样例文件不存在: ${sample.absolutePath}", sample.exists())
        val config = RuleConfigParser.parse(sample.readText(Charsets.UTF_8))
        assertEquals(1, config.version)
        assertTrue(config.selectors.size >= 8)
        assertNotNull(config.selector("worktool.debug.title"))
        assertNotNull(config.selector("wework.chat.send"))
    }
}
