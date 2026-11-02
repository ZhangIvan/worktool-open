/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import cn.worktool.open.core.protocol.canonical.Jcs
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement

/**
 * v2 逐帧鉴权（protocol/spec.md §5.2，2026-11-02 AUD-03 C-9 修订版输入式）。
 *
 * 冻结输入式：
 * ```
 * hmac = HMAC-SHA256(key = deviceToken 原始字节,
 *                    msg = UTF-8( commandId "|" eventId "|" seq "|" ts "|" nonce "|" payloadDigest ))
 * payloadDigest:
 *   encryptType=0 → hex(SHA-256( JCS(RFC 8785) of `list` ))；list 缺失或空数组按空字节串计
 *   encryptType=2 → hex(SHA-256( UTF-8 bytes of `encryptedList` 字符串原样 ))  ← 先验签后解密
 * ```
 * 金标准向量 = protocol/fixtures/hmac-vector.json（正例 + tamper_seq 必败反例），
 * L1 单测逐字节复算（关闭 AUD-03 C-4）。
 */
object FrameAuth {

    /** HMAC 输入串（C-9：seq 插在 eventId 之后）。 */
    fun inputString(
        commandId: String,
        eventId: String,
        seq: Long,
        ts: Long,
        nonce: String,
        payloadDigest: String,
    ): String = "$commandId|$eventId|$seq|$ts|$nonce|$payloadDigest"

    /** encryptType=0 的载荷摘要：JCS(list) 的 SHA-256 hex；缺失或空数组按空字节串计。 */
    fun payloadDigestOfList(list: JsonElement?): String =
        if (list == null || (list is JsonArray && list.isEmpty())) {
            Digests.SHA256_EMPTY_HEX
        } else {
            Digests.sha256Hex(Jcs.canonicalize(list).toByteArray(Charsets.UTF_8))
        }

    /** encryptType=2 的载荷摘要：`encryptedList` 字符串原样 UTF-8 字节的 SHA-256 hex。 */
    fun payloadDigestOfEncrypted(encryptedList: String): String = Digests.sha256Hex(encryptedList)

    /** 按帧字段计算 HMAC（小写 hex 64 字符）。 */
    fun hmacHex(
        deviceToken: ByteArray,
        commandId: String,
        eventId: String,
        seq: Long,
        ts: Long,
        nonce: String,
        payloadDigest: String,
    ): String = Digests.hmacSha256Hex(
        deviceToken,
        inputString(commandId, eventId, seq, ts, nonce, payloadDigest),
    )

    /** 校验声明 hmac：常数时间比较（无时序侧信道），不匹配 = 鉴权失败（4401）。 */
    fun verifyHmac(
        deviceToken: ByteArray,
        declaredHmac: String,
        commandId: String,
        eventId: String,
        seq: Long,
        ts: Long,
        nonce: String,
        payloadDigest: String,
    ): Boolean = Digests.constantTimeEquals(
        declaredHmac,
        hmacHex(deviceToken, commandId, eventId, seq, ts, nonce, payloadDigest),
    )
}
