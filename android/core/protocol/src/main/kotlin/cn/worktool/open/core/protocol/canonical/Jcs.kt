/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.canonical

import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.longOrNull

/**
 * RFC 8785（JSON Canonicalization Scheme，JCS）规范化 JSON 序列化。
 *
 * 用途（protocol/spec.md §5.2.2 冻结）：
 * `payloadDigest = hex(SHA-256( JCS(RFC 8785) of list ))`——逐帧 HMAC 的载荷摘要
 * 必须对规范化字节流计算，与键序/空白无关，双方（Kotlin/Go）逐字节一致。
 *
 * 实现要点：
 * - 对象键按 UTF-16 码元升序（Java String.compareTo 语义，与 RFC 8785 §3.2.3 一致）；
 * - 字符串仅转义 `"`、`\` 与 U+0000..U+001F（短转义 \b\t\n\f\r，其余 \u00xx 小写）；
 * - 数字按 ECMAScript Number::toString(10)（整数无小数点、1e21 起指数式、
 *   -0 → "0"）。
 *
 * 已知边界（记录于 android/README.md）：非整数 double 的「最短十进制」取自
 * java.lang.Double.toString；JDK<19 在极少数值上可能多出一位尾数（JDK-4511638）。
 * 本协议载荷（fixtures 与 v1/v2 帧字段）均为整数，不触及该边界；
 * 服务端（Go）实现侧如出现非整数数值载荷需以同一 RFC 算法对齐复核。
 */
object Jcs {

    /** 序列化 JsonElement 为 RFC 8785 规范化字符串。 */
    fun canonicalize(element: JsonElement): String {
        val sb = StringBuilder()
        write(element, sb)
        return sb.toString()
    }

    private fun write(element: JsonElement, sb: StringBuilder) {
        when (element) {
            is JsonObject -> {
                sb.append('{')
                var first = true
                for ((key, value) in element.entries.sortedWith(compareBy { it.key })) {
                    if (!first) sb.append(',')
                    first = false
                    writeString(key, sb)
                    sb.append(':')
                    write(value, sb)
                }
                sb.append('}')
            }
            is JsonArray -> {
                sb.append('[')
                var first = true
                for (item in element) {
                    if (!first) sb.append(',')
                    first = false
                    write(item, sb)
                }
                sb.append(']')
            }
            is JsonNull -> sb.append("null")
            is JsonPrimitive -> writePrimitive(element, sb)
        }
    }

    private fun writePrimitive(p: JsonPrimitive, sb: StringBuilder) {
        if (p.isString) {
            writeString(p.content, sb)
            return
        }
        val content = p.content
        if (content == "true" || content == "false") {
            sb.append(content)
            return
        }
        val asLong = p.longOrNull
        if (asLong != null) {
            sb.append(asLong.toString())
            return
        }
        val asDouble = p.doubleOrNull
        if (asDouble != null) {
            sb.append(esNumberToString(asDouble))
            return
        }
        throw IllegalArgumentException("JCS 不支持的数字字面量: $content")
    }

    internal fun writeString(s: String, sb: StringBuilder) {
        sb.append('"')
        for (c in s) {
            when (c) {
                '"' -> sb.append("\\\"")
                '\\' -> sb.append("\\\\")
                '\b' -> sb.append("\\b")
                '\t' -> sb.append("\\t")
                '\n' -> sb.append("\\n")
                '\u000C' -> sb.append("\\f")
                '\r' -> sb.append("\\r")
                else ->
                    if (c.code < 0x20) {
                        sb.append("\\u")
                        val hex = c.code.toString(16)
                        repeat(4 - hex.length) { sb.append('0') }
                        sb.append(hex)
                    } else {
                        sb.append(c)
                    }
            }
        }
        sb.append('"')
    }

    /**
     * ECMAScript Number::toString(10)（ECMA-262 §7.1.12.1）：输入有限 double，
     * 输出十进制或 `d.ddde±x` 指数形式。
     */
    internal fun esNumberToString(d: Double): String {
        if (d.isNaN() || d.isInfinite()) {
            throw IllegalArgumentException("JSON 不支持 NaN/Infinity: $d")
        }
        val negative = d < 0 || 1.0 / d == Double.NEGATIVE_INFINITY // 含 -0.0
        val javaText = java.lang.Double.toString(if (negative) -d else d)

        // 拆解 Java 形态 [<digits>][.<digits>][eE<exp>]
        var mantissa = javaText
        var exp = 0
        val eIdx = javaText.indexOfLast { it == 'e' || it == 'E' }
        if (eIdx >= 0) {
            mantissa = javaText.substring(0, eIdx)
            exp = javaText.substring(eIdx + 1).toInt()
        }
        val dotIdx = mantissa.indexOf('.')
        val intPart = if (dotIdx >= 0) mantissa.substring(0, dotIdx) else mantissa
        val fracPart = if (dotIdx >= 0) mantissa.substring(dotIdx + 1) else ""
        var digits = (intPart + fracPart)
        var n = intPart.length + exp // value = digits × 10^(n-k)

        // 去前导零（n 随左移递减），去尾随零（k 减小、n 不变）
        var lead = 0
        while (lead < digits.length - 1 && digits[lead] == '0') lead++
        digits = digits.substring(lead)
        n -= lead
        var end = digits.length
        while (end > 1 && digits[end - 1] == '0') end--
        digits = digits.substring(0, end)
        val k = digits.length

        val body = when {
            k == 1 && digits == "0" -> "0" // ±0 → "0"
            k <= n && n <= 21 -> digits + "0".repeat(n - k)
            0 < n && n <= 21 -> digits.substring(0, n) + "." + digits.substring(n)
            -6 < n && n <= 0 -> "0." + "0".repeat(-n) + digits
            k == 1 -> digits + "e" + exponentSuffix(n - 1)
            else -> digits.substring(0, 1) + "." + digits.substring(1) + "e" + exponentSuffix(n - 1)
        }
        return if (negative && body != "0") "-$body" else body
    }

    private fun exponentSuffix(e: Int): String =
        if (e >= 0) "+$e" else e.toString()
}
