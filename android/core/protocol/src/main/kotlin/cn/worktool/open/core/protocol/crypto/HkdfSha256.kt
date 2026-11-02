/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * HKDF-SHA256（RFC 5869 extract+expand），纯 Java Security API 实现。
 *
 * 用途（protocol/spec.md §5.3 冻结式）：
 * `K_session = HKDF-SHA256(ikm=X25519 共享密钥, salt=UTF-8(sessionId), info="worktool-open/v2/session", L=32)`
 */
object HkdfSha256 {

    private const val HASH_LEN = 32

    fun extract(salt: ByteArray, ikm: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(if (salt.isEmpty()) ByteArray(HASH_LEN) else salt, "HmacSHA256"))
        return mac.doFinal(ikm)
    }

    fun expand(prk: ByteArray, info: ByteArray, outLen: Int): ByteArray {
        require(outLen <= 255 * HASH_LEN) { "HKDF 输出超限: $outLen" }
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(prk, "HmacSHA256"))
        val out = ByteArray(outLen)
        var t = ByteArray(0)
        var pos = 0
        var counter = 1
        while (pos < outLen) {
            mac.reset()
            mac.update(t)
            mac.update(info)
            mac.update(counter.toByte())
            t = mac.doFinal()
            val n = minOf(t.size, outLen - pos)
            System.arraycopy(t, 0, out, pos, n)
            pos += n
            counter++
        }
        return out
    }

    fun derive(ikm: ByteArray, salt: ByteArray, info: ByteArray, outLen: Int): ByteArray =
        expand(extract(salt, ikm), info, outLen)
}
