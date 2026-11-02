/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import java.security.MessageDigest
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/** SHA-256 / HMAC-SHA256 原语（Java Security API，无第三方加密库）。 */
object Digests {

    /** SHA-256(空字节串) 的 hex 常量（protocol/spec.md §5.2.2 冻结值）。 */
    const val SHA256_EMPTY_HEX: String =
        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

    fun sha256(bytes: ByteArray): ByteArray =
        MessageDigest.getInstance("SHA-256").digest(bytes)

    fun sha256Hex(bytes: ByteArray): String = Hex.encode(sha256(bytes))

    fun sha256Hex(utf8Text: String): String = sha256Hex(utf8Text.toByteArray(Charsets.UTF_8))

    /** HMAC-SHA256(key, UTF-8(msg))，输出 32 字节。 */
    fun hmacSha256(key: ByteArray, utf8Msg: String): ByteArray =
        hmacSha256(key, utf8Msg.toByteArray(Charsets.UTF_8))

    fun hmacSha256(key: ByteArray, msg: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        return mac.doFinal(msg)
    }

    fun hmacSha256Hex(key: ByteArray, utf8Msg: String): String = Hex.encode(hmacSha256(key, utf8Msg))

    /** 常数时间字符串比较（鉴权比较防时序侧信道；长度不同直接失败）。 */
    fun constantTimeEquals(a: String, b: String): Boolean {
        if (a.length != b.length) return false
        var diff = 0
        for (i in a.indices) diff = diff or (a[i].code xor b[i].code)
        return diff == 0
    }
}
