/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.canonical.Jcs
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test

/**
 * RFC 8785（JCS）规范化测试：键序/转义/数字规则 + 与夹具摘要的一致性
 * （后者在 HmacVectorTest 已逐字节复算，此处覆盖算法分支）。
 */
class JcsTest {

    private val json = Json

    private fun canon(s: String): String = Jcs.canonicalize(json.parseToJsonElement(s))

    @Test
    fun `对象键按 UTF-16 码元排序`() {
        assertEquals(
            """{"":1,"A":2,"a":3,"b":4,"á":5}""",
            canon("""{"b":4,"a":3,"A":2,"á":5,"":1}"""),
        )
    }

    @Test
    fun `字符串最小转义`() {
        assertEquals(
            """{"k":"a\"b\\c\nd e\t"}""",
            canon("""{"k":"a\"b\\c\nd e\t"}"""),
        )
    }

    @Test
    fun `控制字符转义为小写 u 形式`() {
        // JSON 源文本以 \u0001 转义传入（源里不允许裸控制字符）
        val source = "{\"k\":\"\\u0001\"}"
        assertEquals("{\"k\":\"\\u0001\"}", canon(source))
    }

    @Test
    fun `中文与 emoji 不转义`() {
        assertEquals(
            """{"msg":"你好，WorkTool-Open！😀"}""",
            canon("""{"msg":"你好，WorkTool-Open！😀"}"""),
        )
    }

    @Test
    fun `布尔与 null 字面量`() {
        assertEquals("""[true,false,null]""", canon("""[true, false, null]"""))
    }

    @Test
    fun `整数无小数点`() {
        assertEquals("""[0,203,-7,9007199254740993]""", canon("""[0, 203, -7, 9007199254740993]"""))
    }

    @Test
    fun `ES 数字格式化边界`() {
        assertEquals("0", Jcs.esNumberToString(0.0))
        assertEquals("0", Jcs.esNumberToString(-0.0))
        assertEquals("5", Jcs.esNumberToString(5.0))
        assertEquals("-12.5", Jcs.esNumberToString(-12.5))
        assertEquals("123.456", Jcs.esNumberToString(123.456))
        assertEquals("0.000001", Jcs.esNumberToString(0.000001))
        assertEquals("1e-7", Jcs.esNumberToString(0.0000001))
        assertEquals("1e+21", Jcs.esNumberToString(1e21))
        assertEquals("123000000000000000000", Jcs.esNumberToString(1.23e20))
        assertEquals("1.5e+22", Jcs.esNumberToString(1.5e22))
        assertEquals("-1e+21", Jcs.esNumberToString(-1e21))
    }

    @Test
    fun `NaN 与 Infinity 拒绝`() {
        try {
            Jcs.esNumberToString(Double.NaN)
            fail("NaN 必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
        try {
            Jcs.esNumberToString(Double.POSITIVE_INFINITY)
            fail("Infinity 必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
    }

    @Test
    fun `嵌套结构与数组保序`() {
        // 对象键排序递归生效；数组元素保序
        val expected = "{\"a\":[1,{\"m\":0,\"z\":true}],\"b\":{\"x\":[]}}"
        assertEquals(expected, canon("""{"b":{"x":[]},"a":[1,{"z":true,"m":0}]}"""))
    }

    @Test
    fun `非整数 double 走最短表示路径`() {
        // 0.1+0.2 场景：JDK Double.toString(0.30000000000000004) 即最短往返表示
        assertEquals("0.30000000000000004", Jcs.esNumberToString(0.1 + 0.2))
    }
}
