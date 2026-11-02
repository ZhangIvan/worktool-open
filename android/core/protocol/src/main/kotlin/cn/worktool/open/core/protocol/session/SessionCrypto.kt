/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.crypto.Digests
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.HkdfSha256
import cn.worktool.open.core.protocol.crypto.X25519
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import java.security.PrivateKey
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * encryptType=2 会话加密（protocol/spec.md §5.3，全部冻结参数）。
 *
 * - 会话密钥：`K_session = HKDF-SHA256(ikm=X25519(设备临时私钥, serverX25519PublicKey),
 *   salt=UTF-8(sessionId), info="worktool-open/v2/session", L=32)`
 * - GCM nonce（12 字节）：`bytes[0:12] of SHA-256(UTF-8(eventId))`
 * - GCM AAD：`UTF-8("ver=2|seq=" + seq + "|eventId=" + eventId + "|ts=" + ts)`
 *   （seq 自 C-9 起已由 HMAC 覆盖全部帧，AAD 对加密帧保留为纵深防御）
 * - `encryptedList = hex( 密文 || 16字节tag )`，明文为与 v1 `list` 同构的 JSON 数组
 * - 解密/验签失败 = 帧整体拒绝（不得部分接受）
 */
class SessionCrypto private constructor(private val sessionKey: ByteArray) {

    companion object {
        private const val HKDF_INFO = "worktool-open/v2/session"
        private const val KEY_LEN = 32
        private const val GCM_TAG_BITS = 128

        /** 由配对/重协商材料派生会话密钥（每次协商新 X25519 对 → 新 K_session）。 */
        fun derive(
            deviceEphemeralPrivate: PrivateKey,
            serverX25519PublicKeyRaw: ByteArray,
            sessionId: String,
        ): SessionCrypto {
            require(serverX25519PublicKeyRaw.size == 32) {
                "serverX25519PublicKey 必须 32 字节，收到 ${serverX25519PublicKeyRaw.size}"
            }
            val shared = X25519.sharedSecret(deviceEphemeralPrivate, serverX25519PublicKeyRaw)
            val key = HkdfSha256.derive(
                ikm = shared,
                salt = sessionId.toByteArray(Charsets.UTF_8),
                info = HKDF_INFO.toByteArray(Charsets.UTF_8),
                outLen = KEY_LEN,
            )
            return SessionCrypto(key)
        }

        /** 测试/服务端对端复算用：直接以共享密钥材料派生。 */
        fun fromSharedSecret(shared: ByteArray, sessionId: String): SessionCrypto =
            SessionCrypto(
                HkdfSha256.derive(
                    ikm = shared,
                    salt = sessionId.toByteArray(Charsets.UTF_8),
                    info = HKDF_INFO.toByteArray(Charsets.UTF_8),
                    outLen = KEY_LEN,
                ),
            )

        fun gcmNonce(eventId: String): ByteArray =
            Digests.sha256(eventId.toByteArray(Charsets.UTF_8)).copyOfRange(0, 12)

        fun aad(seq: Long, eventId: String, ts: Long): ByteArray =
            "ver=2|seq=$seq|eventId=$eventId|ts=$ts".toByteArray(Charsets.UTF_8)
    }

    /** 加密 list（JsonElement 数组）为 encryptedList hex（密文||tag）。 */
    fun encryptList(listElement: JsonElement, seq: Long, eventId: String, ts: Long): String {
        val plain = Json.encodeToString(
            kotlinx.serialization.json.JsonElement.serializer(),
            listElement,
        ).toByteArray(Charsets.UTF_8)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(sessionKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, gcmNonce(eventId)))
        cipher.updateAAD(aad(seq, eventId, ts))
        return Hex.encode(cipher.doFinal(plain))
    }

    /** 解密 encryptedList hex；验签失败抛 [javax.crypto.AEADBadTagException]。 */
    fun decryptList(encryptedListHex: String, seq: Long, eventId: String, ts: Long): JsonElement {
        val cipherText = Hex.decode(encryptedListHex)
        require(cipherText.size > 16) { "encryptedList 短于 GCM tag 长度" }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(sessionKey, "AES"), GCMParameterSpec(GCM_TAG_BITS, gcmNonce(eventId)))
        cipher.updateAAD(aad(seq, eventId, ts))
        val plain = cipher.doFinal(cipherText)
        return Json.parseToJsonElement(plain.toString(Charsets.UTF_8))
    }
}
