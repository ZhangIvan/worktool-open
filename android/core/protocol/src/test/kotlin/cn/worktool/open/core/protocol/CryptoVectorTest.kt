/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.crypto.Ed25519
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.HkdfSha256
import cn.worktool.open.core.protocol.crypto.X25519
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.security.KeyFactory
import java.security.spec.PKCS8EncodedKeySpec

/**
 * 加密原语 RFC 向量测试（Java Security API 实现正确性证明）：
 * - HKDF-SHA256：RFC 5869 Test Case 1；
 * - X25519：RFC 7748 §6.1（Diffie-Hellman）+ §5.2（单次迭代）；
 * - Ed25519：RFC 8032 §7.1 TEST 1（空消息）。
 */
class CryptoVectorTest {

    // ---- RFC 5869 Test Case 1（SHA-256）----

    @Test
    fun `HKDF-SHA256 RFC5869 TC1`() {
        val ikm = ByteArray(22) { 0x0b }
        val salt = Hex.decode("000102030405060708090a0b0c")
        val info = Hex.decode("f0f1f2f3f4f5f6f7f8f9")
        val prk = HkdfSha256.extract(salt, ikm)
        assertEquals(
            "077709362c2e32df0ddc3f0dc47bba6390b6c73bb50f9c3122ec844ad7c2b3e5",
            Hex.encode(prk),
        )
        val okm = HkdfSha256.expand(prk, info, 42)
        assertEquals(
            "3cb25f25faacd57a90434f64d0362f2a" +
                "2d2d0a90cf1a5a4c5db02d56ecc4c5bf" +
                "34007208d5b887185865",
            Hex.encode(okm),
        )
    }

    // ---- RFC 7748 §5.2（X25519 单次迭代）----

    @Test
    fun `X25519 RFC7748 单次迭代`() {
        val scalar = Hex.decode("a546e36bf0527c9d3b16154b82465edd62144c0ac1fc5a18506a2244ba449ac4")
        val u = Hex.decode("e6db6867583030db3594c1a424b15f7c726624ec26b3353b10a903a6d0ab1c4c")
        val shared = X25519.sharedSecret(X25519.decodePrivatePkcs8(pkcs8X25519(scalar)), u)
        assertEquals(
            "c3da55379de9c6908e94ea4df28d084f32eccf03491c71f754b4075577a28552",
            Hex.encode(shared),
        )
    }

    // ---- RFC 7748 §6.1（X25519 DH 密钥交换 + raw 导入导出）----

    @Test
    fun `X25519 RFC7748 DH 双方共享密钥一致`() {
        val aliceSeed = Hex.decode("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
        val bobSeed = Hex.decode("5dab087e624a8a4b79e17f8b83800ee66f3bb1292618b6fd1c2f8b27ff88e0eb")

        val alicePriv = X25519.decodePrivatePkcs8(pkcs8X25519(aliceSeed))
        val bobPriv = X25519.decodePrivatePkcs8(pkcs8X25519(bobSeed))

        // 双方各自生成密钥对后导出 raw 公钥
        val alicePair = java.security.KeyPairGenerator.getInstance("X25519").genKeyPair()
        val bobPair = java.security.KeyPairGenerator.getInstance("X25519").genKeyPair()
        assertEquals(32, X25519.publicRaw(alicePair.public).size)
        assertEquals(32, X25519.publicRaw(bobPair.public).size)

        // raw 公钥导入导出往返
        assertArrayEquals(
            X25519.publicRaw(alicePair.public),
            X25519.publicRaw(X25519.importPublic(X25519.publicRaw(alicePair.public))),
        )

        // 固定向量：alice 私钥 × bob 公钥 == K（双方一致）
        val k1 = X25519.sharedSecret(alicePriv, Hex.decode("de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"))
        val k2 = X25519.sharedSecret(bobPriv, Hex.decode("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a"))
        assertEquals("4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742", Hex.encode(k1))
        assertArrayEquals(k1, k2)
    }

    // ---- RFC 8032 §7.1 TEST 1（Ed25519 空消息）----

    @Test
    fun `Ed25519 RFC8032 TEST1 与 tamper 必败`() {
        val seed = Hex.decode("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
        val priv = Ed25519.decodePrivatePkcs8(pkcs8Ed25519(seed))
        val pubRaw = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
        assertEquals(pubRaw, Hex.encode(Ed25519.publicRaw(Ed25519.importPublic(Hex.decode(pubRaw)))))

        val sig = Ed25519.sign(priv, ByteArray(0))
        assertEquals(
            "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
            Hex.encode(sig),
        )
        assertTrue(Ed25519.verify(Hex.decode(pubRaw), ByteArray(0), sig))

        // 篡改消息/签名必须验签失败
        assertFalse(Ed25519.verify(Hex.decode(pubRaw), byteArrayOf(1), sig))
        val tampered = sig.copyOf().also { it[0] = (it[0].toInt() xor 1).toByte() }
        assertFalse(Ed25519.verify(Hex.decode(pubRaw), ByteArray(0), tampered))
        // 长度非法公钥 → verify 返回 false（不抛）
        assertFalse(Ed25519.verify(ByteArray(31), ByteArray(0), sig))
    }

    @Test
    fun `Ed25519 生成-签名-验签 roundtrip`() {
        val pair = Ed25519.generateKeyPair()
        val msg = "deviceId|1793613605000|4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d".toByteArray()
        val sig = Ed25519.sign(pair.private, msg)
        assertEquals(64, sig.size)
        assertTrue(Ed25519.verify(Ed25519.publicRaw(pair.public), msg, sig))
    }

    // ---- PKCS#8 头构造（测试导入 raw 种子）----

    private fun pkcs8X25519(seed: ByteArray): ByteArray =
        Hex.decode("302e020100300506032b656e04220420") + seed

    private fun pkcs8Ed25519(seed: ByteArray): ByteArray =
        Hex.decode("302e020100300506032b657004220420") + seed
}
