/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.json

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** 自写 JSON 解析器单测（规则引擎的解析底座）。 */
class JsonTest {

    @Test
    fun parsesScalars() {
        assertEquals("v", (Json.parse("\"v\"") as JsonValue.JsonString).value)
        assertEquals(true, (Json.parse("true") as JsonValue.JsonBool).value)
        assertEquals(JsonValue.JsonNull, Json.parse("null"))
        assertEquals(7, (Json.parse("7") as JsonValue.JsonNumber).int)
        assertEquals(-42, (Json.parse("-42") as JsonValue.JsonNumber).int)
        assertEquals(1.5, (Json.parse("1.5") as JsonValue.JsonNumber).double, 0.0)
        assertEquals(1000, (Json.parse("1e3") as JsonValue.JsonNumber).int)
    }

    @Test
    fun parsesNestedContainers() {
        val v = Json.parse("""{"a":[1,{"b":"c"}],"d":null}""") as JsonValue.JsonObject
        val a = v.array("a")!!
        assertEquals(2, a.size)
        assertEquals(1, (a[0] as JsonValue.JsonNumber).int)
        assertEquals("c", ((a[1] as JsonValue.JsonObject).string("b")))
        assertTrue(v.fields["d"] is JsonValue.JsonNull)
    }

    @Test
    fun parsesEscapes() {
        val v = Json.parse("\"a\\nb\\t\\\"\\\\\\/\\u4e2d\"") as JsonValue.JsonString
        assertEquals("a\nb\t\"\\/\u4e2d", v.value)
    }

    @Test
    fun roundTripsWrite() {
        val src = """{"k":["a",1,true,null],"n":-2.5}"""
        val v = Json.parse(src)
        val out = Json.write(v)
        assertEquals(src, out)
    }

    @Test
    fun rejectsMalformed() {
        assertThrows(Json.JsonParseException::class.java) { Json.parse("{") }
        assertThrows(Json.JsonParseException::class.java) { Json.parse("[1,]") }
        assertThrows(Json.JsonParseException::class.java) { Json.parse("{\"a\":1,}") }
        assertThrows(Json.JsonParseException::class.java) { Json.parse("tru") }
        assertThrows(Json.JsonParseException::class.java) { Json.parse("\"unterminated") }
        assertThrows(Json.JsonParseException::class.java) { Json.parse("01") } // 前导零非法
        assertThrows(Json.JsonParseException::class.java) { Json.parse("1 2") } // 尾随内容
    }

    @Test
    fun rejectsDeepNesting() {
        val deep = "[".repeat(80) + "]".repeat(80)
        assertThrows(Json.JsonParseException::class.java) { Json.parse(deep) }
    }
}
