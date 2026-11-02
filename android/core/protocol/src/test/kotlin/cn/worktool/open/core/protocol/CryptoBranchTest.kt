/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.crypto.Digests
import cn.worktool.open.core.protocol.crypto.Ed25519
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.HkdfSha256
import cn.worktool.open.core.protocol.crypto.X25519
import cn.worktool.open.core.protocol.session.SessionCrypto
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** 加密原语分支覆盖：Hex/摘要/HKDF/曲线导入导出错误路径、FrameAuth 分支。 */
class CryptoBranchTest {

    // ---- Hex ----

    @Test
    fun `hex 编码解码与错误分支`() {
        assertArrayEquals(byteArrayOf(0x0a, 0xff.toByte(), 0x10), Hex.decode("0aff10"))
        assertArrayEquals(byteArrayOf(0x0a, 0xff.toByte(), 0x10), Hex.decode("0AFF10")) // 大写容忍
        assertEquals("0aff10", Hex.encode(byteArrayOf(10, -1, 16)))
        try {
            Hex.decode("abc") // 奇数长度
            fail("奇数长度必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
        try {
            Hex.decode("0g") // 非 hex 字符
            fail("非 hex 字符必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
        try {
            Hex.decode("zz")
            fail()
        } catch (_: IllegalArgumentException) {
        }
    }

    // ---- 摘要 / 常数时间比较 ----

    @Test
    fun `sha256 与常数时间比较分支`() {
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            Digests.sha256Hex(""),
        )
        assertEquals(Digests.sha256Hex("abc"), Digests.sha256Hex("abc".toByteArray()))
        assertTrue(Digests.constantTimeEquals("aa", "aa"))
        assertFalse(Digests.constantTimeEquals("aa", "ab"))
        assertFalse(Digests.constantTimeEquals("aa", "a"))
        assertTrue(Digests.hmacSha256(ByteArray(32), "x").size == 32)
        assertEquals(Digests.hmacSha256Hex(ByteArray(32), "m"), Digests.hmacSha256Hex(ByteArray(32), "m"))
        assertNotEquals(Digests.hmacSha256Hex(ByteArray(32), "m"), Digests.hmacSha256Hex(ByteArray(31) { 9 }, "m"))
    }

    // ---- HKDF ----

    @Test
    fun `hkdf 空 salt 分支与超限拒绝`() {
        val okm = HkdfSha256.derive(ikm = ByteArray(32) { 1 }, salt = ByteArray(0), info = ByteArray(0), outLen = 32)
        assertEquals(32, okm.size)
        assertEquals(okm.size, HkdfSha256.expand(HkdfSha256.extract(ByteArray(0), ByteArray(32) { 1 }), ByteArray(0), 32).size)
        try {
            HkdfSha256.expand(ByteArray(32), ByteArray(0), 255 * 32 + 1)
            fail("超限必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
    }

    // ---- X25519 导入导出边界 ----

    @Test
    fun `x25519 长度校验与私钥编解码往返`() {
        try {
            X25519.importPublic(ByteArray(31))
            fail("非 32 字节公钥必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
        val pair = X25519.generateKeyPair()
        val pkcs8 = X25519.encodePrivatePkcs8(pair.private)
        val restored = X25519.decodePrivatePkcs8(pkcs8)
        // 恢复的私钥与原私钥产出相同共享密钥
        val peerPub = X25519.generateKeyPair().public
        assertArrayEquals(
            X25519.sharedSecret(pair.private, X25519.publicRaw(peerPub)),
            X25519.sharedSecret(restored, X25519.publicRaw(peerPub)),
        )
    }

    @Test
    fun `ed25519 私钥编解码往返与验签异常分支`() {
        val pair = Ed25519.generateKeyPair()
        val restored = Ed25519.decodePrivatePkcs8(Ed25519.encodePrivatePkcs8(pair.private))
        val msg = "x".toByteArray()
        assertArrayEquals(Ed25519.sign(pair.private, msg), Ed25519.sign(restored, msg))
        // SPKI 长度异常拒绝（publicRaw）
        try {
            Ed25519.publicRaw(object : java.security.PublicKey {
                override fun getAlgorithm() = "Ed25519"
                override fun getFormat() = "X.509"
                override fun getEncoded() = ByteArray(10)
            })
            fail("SPKI 长度异常必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
    }

    // ---- FrameAuth 分支 ----

    @Test
    fun `payloadDigest 分支：非空数组-空数组-null-encrypted`() {
        val nonEmpty = buildJsonArray { add(JsonPrimitive(1)) }
        assertNotEquals(Digests.SHA256_EMPTY_HEX, FrameAuth.payloadDigestOfList(nonEmpty))
        assertEquals(Digests.SHA256_EMPTY_HEX, FrameAuth.payloadDigestOfList(buildJsonArray { }))
        assertEquals(Digests.SHA256_EMPTY_HEX, FrameAuth.payloadDigestOfList(null))
        // encryptedList 原样摘要（不是 JCS）
        assertEquals(Digests.sha256Hex("aabb"), FrameAuth.payloadDigestOfEncrypted("aabb"))
        // verifyHmac 正反
        val token = ByteArray(32) { 7 }
        val mac = FrameAuth.hmacHex(token, "", "e", 1, 2, "n", "d")
        assertTrue(FrameAuth.verifyHmac(token, mac, "", "e", 1, 2, "n", "d"))
        assertFalse(FrameAuth.verifyHmac(token, mac.uppercase(), "", "e", 1, 2, "n", "d"))
        assertFalse(FrameAuth.verifyHmac(token, mac, "x", "e", 1, 2, "n", "d"))
    }

    // ---- SessionCrypto 分支 ----

    @Test
    fun `session crypto 长度校验分支`() {
        try {
            SessionCrypto.derive(
                deviceEphemeralPrivate = X25519.generateKeyPair().private,
                serverX25519PublicKeyRaw = ByteArray(31),
                sessionId = "s",
            )
            fail("服务端公钥长度必须校验")
        } catch (_: IllegalArgumentException) {
        }
        val crypto = SessionCrypto.fromSharedSecret(ByteArray(32) { 3 }, "sess")
        try {
            crypto.decryptList("aabb", seq = 1, eventId = "e", ts = 1) // 短于 tag
            fail("密文过短必须拒绝")
        } catch (_: IllegalArgumentException) {
        }
        try {
            crypto.decryptList("zz", seq = 1, eventId = "e", ts = 1)
            fail("非 hex 必须拒绝")
        } catch (_: Exception) {
        }
    }
}
