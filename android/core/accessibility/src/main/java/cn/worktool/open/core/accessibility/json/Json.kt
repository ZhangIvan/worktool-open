/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.json

/**
 * 极简 JSON 解析/序列化（T2.4 规则引擎用）。
 *
 * 为什么自写而不用 org.json：① 仓库依赖策略零第三方运行时依赖
 * （android/build.gradle.kts：除 Kotlin 标准库与 JUnit 外不引入任何运行时依赖）；
 * ② org.json 在 JVM 单测中是 android.jar 桩（默认抛 "not mocked"），
 * 会破坏 docs/03 T2.4 "规则引擎 JVM 单测" 的要求。
 *
 * 支持子集：RFC 8259 对象/数组/字符串/数字/布尔/null；对象键唯一性不强制（后者覆盖前者）；
 * 嵌套深度上限 [MAX_DEPTH] 防御恶意配置导致栈溢出。
 */
sealed class JsonValue {

    object JsonNull : JsonValue()

    data class JsonBool(val value: Boolean) : JsonValue()

    /** 保留原文以避免精度损失；`int` 对非整型形态按 Double 收敛。 */
    class JsonNumber(val raw: String) : JsonValue() {
        val double: Double get() = raw.toDouble()
        val int: Int get() = double.toInt()
        val isIntegralText: Boolean get() = raw.none { it == '.' || it == 'e' || it == 'E' }
    }

    data class JsonString(val value: String) : JsonValue()

    class JsonObject(val fields: LinkedHashMap<String, JsonValue> = LinkedHashMap()) : JsonValue() {
        operator fun get(key: String): JsonValue? = fields[key]
        fun string(key: String): String? = (fields[key] as? JsonString)?.value
        fun int(key: String): Int? = (fields[key] as? JsonNumber)?.int
        fun bool(key: String): Boolean? = (fields[key] as? JsonBool)?.value
        fun obj(key: String): JsonObject? = fields[key] as? JsonObject
        fun array(key: String): JsonArray? = fields[key] as? JsonArray
    }

    class JsonArray(val items: MutableList<JsonValue> = mutableListOf()) : JsonValue() {
        val size: Int get() = items.size
        operator fun get(index: Int): JsonValue? = items.getOrNull(index)
    }
}

object Json {

    class JsonParseException(message: String) : Exception(message)

    private const val MAX_DEPTH = 64

    fun parse(text: String): JsonValue {
        val p = Parser(text)
        p.skipWs()
        val v = p.value(0)
        p.skipWs()
        if (!p.atEnd()) p.fail("trailing characters at offset ${p.pos}")
        return v
    }

    fun write(value: JsonValue): String = StringBuilder().also { writeTo(it, value, 0) }.toString()

    private fun writeTo(sb: StringBuilder, value: JsonValue, depth: Int) {
        when (value) {
            is JsonValue.JsonNull -> sb.append("null")
            is JsonValue.JsonBool -> sb.append(if (value.value) "true" else "false")
            is JsonValue.JsonNumber -> sb.append(value.raw)
            is JsonValue.JsonString -> writeString(sb, value.value)
            is JsonValue.JsonObject -> {
                sb.append('{')
                var first = true
                for ((k, v) in value.fields) {
                    if (!first) sb.append(',')
                    first = false
                    writeString(sb, k)
                    sb.append(':')
                    writeTo(sb, v, depth + 1)
                }
                sb.append('}')
            }
            is JsonValue.JsonArray -> {
                sb.append('[')
                var first = true
                for (v in value.items) {
                    if (!first) sb.append(',')
                    first = false
                    writeTo(sb, v, depth + 1)
                }
                sb.append(']')
            }
        }
    }

    private fun writeString(sb: StringBuilder, s: String) {
        sb.append('"')
        for (c in s) {
            when (c) {
                '"' -> sb.append("\\\"")
                '\\' -> sb.append("\\\\")
                '\n' -> sb.append("\\n")
                '\r' -> sb.append("\\r")
                '\t' -> sb.append("\\t")
                '\b' -> sb.append("\\b")
                '\u000C' -> sb.append("\\f")
                else -> if (c < ' ') sb.append("\\u%04x".format(c.code)) else sb.append(c)
            }
        }
        sb.append('"')
    }

    private class Parser(private val text: String) {
        var pos = 0

        fun atEnd(): Boolean = pos >= text.length

        fun fail(msg: String): Nothing = throw JsonParseException("$msg (offset $pos)")

        fun skipWs() {
            while (pos < text.length && text[pos].let { it == ' ' || it == '\t' || it == '\n' || it == '\r' }) pos++
        }

        fun value(depth: Int): JsonValue {
            if (depth > MAX_DEPTH) fail("nesting deeper than $MAX_DEPTH")
            skipWs()
            if (atEnd()) fail("unexpected end of input")
            return when (val c = text[pos]) {
                '{' -> obj(depth)
                '[' -> arr(depth)
                '"' -> JsonValue.JsonString(str())
                't' -> lit("true", JsonValue.JsonBool(true))
                'f' -> lit("false", JsonValue.JsonBool(false))
                'n' -> lit("null", JsonValue.JsonNull)
                else ->
                    if (c == '-' || c in '0'..'9') number()
                    else fail("unexpected character '$c'")
            }
        }

        private fun lit(word: String, v: JsonValue): JsonValue {
            if (pos + word.length > text.length || text.substring(pos, pos + word.length) != word) {
                fail("invalid literal (expected '$word')")
            }
            pos += word.length
            return v
        }

        private fun obj(depth: Int): JsonValue.JsonObject {
            pos++ // '{'
            val out = JsonValue.JsonObject()
            skipWs()
            if (!atEnd() && text[pos] == '}') { pos++; return out }
            while (true) {
                skipWs()
                if (atEnd() || text[pos] != '"') fail("expected object key string")
                val key = str()
                skipWs()
                if (atEnd() || text[pos] != ':') fail("expected ':' after object key")
                pos++
                out.fields[key] = value(depth + 1)
                skipWs()
                if (atEnd()) fail("unterminated object")
                when (text[pos]) {
                    ',' -> pos++
                    '}' -> { pos++; return out }
                    else -> fail("expected ',' or '}' in object")
                }
            }
        }

        private fun arr(depth: Int): JsonValue.JsonArray {
            pos++ // '['
            val out = JsonValue.JsonArray()
            skipWs()
            if (!atEnd() && text[pos] == ']') { pos++; return out }
            while (true) {
                out.items += value(depth + 1)
                skipWs()
                if (atEnd()) fail("unterminated array")
                when (text[pos]) {
                    ',' -> pos++
                    ']' -> { pos++; return out }
                    else -> fail("expected ',' or ']' in array")
                }
            }
        }

        private fun str(): String {
            pos++ // '"'
            val sb = StringBuilder()
            while (true) {
                if (atEnd()) fail("unterminated string")
                when (val c = text[pos]) {
                    '"' -> { pos++; return sb.toString() }
                    '\\' -> {
                        pos++
                        if (atEnd()) fail("unterminated escape")
                        when (val e = text[pos]) {
                            '"' -> sb.append('"')
                            '\\' -> sb.append('\\')
                            '/' -> sb.append('/')
                            'b' -> sb.append('\b')
                            'f' -> sb.append('\u000C')
                            'n' -> sb.append('\n')
                            'r' -> sb.append('\r')
                            't' -> sb.append('\t')
                            'u' -> {
                                val cp1 = unicodeEscape()
                                var codePoint = cp1
                                // 代理对：高位代理后紧跟低位代理转义则合并
                                if (cp1 in 0xD800..0xDBFF && pos + 1 < text.length &&
                                    text[pos] == '\\' && text[pos + 1] == 'u'
                                ) {
                                    val save = pos
                                    pos += 2
                                    val cp2 = unicodeEscape()
                                    if (cp2 in 0xDC00..0xDFFF) {
                                        codePoint = 0x10000 + ((cp1 - 0xD800) shl 10) + (cp2 - 0xDC00)
                                    } else {
                                        pos = save
                                    }
                                }
                                sb.append(codePoint.toCharSequence())
                            }
                            else -> fail("invalid escape '\\$e'")
                        }
                        pos++
                    }
                    else -> {
                        if (c.code < 0x20) fail("unescaped control character in string")
                        sb.append(c)
                        pos++
                    }
                }
            }
        }

        private fun unicodeEscape(): Int {
            // 调用时 pos 位于 'u'，读取后 4 位 hex；返回后 pos 位于最后 hex 位
            if (pos + 4 >= text.length) fail("truncated \\u escape")
            val hex = text.substring(pos + 1, pos + 5)
            val v = hex.toIntOrNull(16) ?: fail("invalid \\u escape '$hex'")
            pos += 4
            return v
        }

        private fun Int.toCharSequence(): CharSequence {
            return if (this in 0x10000..0x10FFFF) {
                val base = this - 0x10000
                val hi = ((base shr 10) + 0xD800).toChar()
                val lo = ((base and 0x3FF) + 0xDC00).toChar()
                StringBuilder(2).append(hi).append(lo)
            } else {
                this.toChar().toString()
            }
        }

        private fun number(): JsonValue.JsonNumber {
            val start = pos
            if (text[pos] == '-') pos++
            if (atEnd()) fail("truncated number")
            if (text[pos] == '0') {
                pos++
            } else if (text[pos] in '1'..'9') {
                while (!atEnd() && text[pos] in '0'..'9') pos++
            } else {
                fail("invalid number")
            }
            if (!atEnd() && text[pos] == '.') {
                pos++
                if (atEnd() || text[pos] !in '0'..'9') fail("invalid number fraction")
                while (!atEnd() && text[pos] in '0'..'9') pos++
            }
            if (!atEnd() && (text[pos] == 'e' || text[pos] == 'E')) {
                pos++
                if (!atEnd() && (text[pos] == '+' || text[pos] == '-')) pos++
                if (atEnd() || text[pos] !in '0'..'9') fail("invalid number exponent")
                while (!atEnd() && text[pos] in '0'..'9') pos++
            }
            val raw = text.substring(start, pos)
            raw.toDoubleOrNull() ?: fail("unparseable number '$raw'")
            return JsonValue.JsonNumber(raw)
        }
    }
}
