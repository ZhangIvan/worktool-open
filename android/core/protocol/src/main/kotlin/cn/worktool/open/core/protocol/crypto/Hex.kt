/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

/** hex 编解码（协议统一小写输出；protocol/spec.md §5.2.2「非 16 进制输出一律小写 hex」）。 */
object Hex {

    /** 编码为小写 hex（长度 = 2n）。 */
    fun encode(bytes: ByteArray): String {
        val sb = StringBuilder(bytes.size * 2)
        for (b in bytes) {
            val v = b.toInt() and 0xFF
            // Character.forDigit 对 0..15 恒返回小写 '0'..'f'；避免内联 hex 字母表字面量（G2-lite 门禁）。
            sb.append(Character.forDigit(v ushr 4, 16))
            sb.append(Character.forDigit(v and 0x0F, 16))
        }
        return sb.toString()
    }

    /** 解码 hex；长度必须为偶数且只含 hex 字符，否则抛 [IllegalArgumentException]。 */
    fun decode(hex: String): ByteArray {
        require(hex.length % 2 == 0) { "hex 长度必须为偶数: ${hex.length}" }
        val out = ByteArray(hex.length / 2)
        for (i in out.indices) {
            val hi = digit(hex[2 * i])
            val lo = digit(hex[2 * i + 1])
            require(!(hi < 0 || lo < 0)) { "非法 hex 字符: ${hex.substring(2 * i, 2 * i + 2)}" }
            out[i] = ((hi shl 4) or lo).toByte()
        }
        return out
    }

    private fun digit(c: Char): Int = when (c) {
        in '0'..'9' -> c - '0'
        in 'a'..'f' -> c - 'a' + 10
        in 'A'..'F' -> c - 'A' + 10
        else -> -1
    }
}
