/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.X25519
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * AES-256-GCM 会话加密测试（protocol/spec.md §5.3 冻结参数）：
 * 密钥派生（X25519+HKDF）、GCM nonce/AAD 派生、加解密往返、篡改必败。
 */
class SessionCryptoTest {

    private val aliceSeed = Hex.decode("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
    private val bobPub = Hex.decode("de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f")
    private val sessionId = "5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80"

    private val crypto: SessionCrypto = SessionCrypto.derive(
        deviceEphemeralPrivate = X25519.decodePrivatePkcs8(
            Hex.decode("302e020100300506032b656e04220420") + aliceSeed,
        ),
        serverX25519PublicKeyRaw = bobPub,
        sessionId = sessionId,
    )

    @Test
    fun `会话密钥派生确定性一致（双方同一共享密钥派生同值）`() {
        val other = SessionCrypto.fromSharedSecret(
            X25519.sharedSecret(
                X25519.decodePrivatePkcs8(Hex.decode("302e020100300506032b656e04220420") + aliceSeed),
                bobPub,
            ),
            sessionId,
        )
        val list = Json.parseToJsonElement("""[{"type":203,"roomType":1}]""")
        val a = crypto.encryptList(list, seq = 7, eventId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", ts = 1793613605000)
        val b = other.encryptList(list, seq = 7, eventId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", ts = 1793613605000)
        assertEquals(a, b)
    }

    @Test
    fun `GCM nonce 与 AAD 派生冻结值`() {
        // nonce = bytes[0:12] of SHA-256(UTF-8(eventId))
        val expectedNonce = Hex.encode(
            cn.worktool.open.core.protocol.crypto.Digests.sha256(
                "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d".toByteArray(Charsets.UTF_8),
            ).copyOfRange(0, 12),
        )
        assertEquals(expectedNonce, Hex.encode(SessionCrypto.gcmNonce("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d")))
        // AAD = ver=2|seq=…|eventId=…|ts=…
        assertEquals(
            "ver=2|seq=413|eventId=1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d|ts=1793613605000",
            String(SessionCrypto.aad(413, "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", 1793613605000)),
        )
    }

    @Test
    fun `加密往返 list 等值`() {
        val listText = """[{"type":203,"roomType":1,"targetRoom":"外部群测试群","at":[],"atList":[]}]"""
        val list = Json.parseToJsonElement(listText)
        val encrypted = crypto.encryptList(list, seq = 414, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
        assertTrue("密文 hex 且长于 tag", encrypted.length > 32 && encrypted.matches(Regex("^[0-9a-f]+$")))
        val decrypted = crypto.decryptList(encrypted, seq = 414, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
        assertEquals(list, decrypted)
    }

    @Test
    fun `篡改密文必败（帧整体拒绝语义）`() {
        val list = Json.parseToJsonElement("""[{"type":11}]""")
        val encrypted = crypto.encryptList(list, seq = 1, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
        val tampered = encrypted.toCharArray().also { it[0] = if (it[0] == '0') '1' else '0' }.concatToString()
        try {
            crypto.decryptList(tampered, seq = 1, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
            fail("篡改密文必须解密失败")
        } catch (_: Exception) {
        }
    }

    @Test
    fun `AAD 不匹配（seq 被改）必败`() {
        val list = Json.parseToJsonElement("""[{"type":11}]""")
        val encrypted = crypto.encryptList(list, seq = 1, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
        try {
            crypto.decryptList(encrypted, seq = 2, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
            fail("seq 变化必须因 AAD 不匹配解密失败")
        } catch (_: Exception) {
        }
    }

    @Test
    fun `密钥不同必败（会话隔离）`() {
        val otherSession = SessionCrypto.fromSharedSecret(ByteArray(32), sessionId)
        val list = Json.parseToJsonElement("""[{"type":11}]""")
        val encrypted = crypto.encryptList(list, seq = 1, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
        try {
            otherSession.decryptList(encrypted, seq = 1, eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b", ts = 1793613606000)
            fail("不同会话密钥必须解密失败")
        } catch (_: Exception) {
        }
    }
}
