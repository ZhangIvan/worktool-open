/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * T2.6 DoD：RFC 8032 §7.1 官方测试向量**全量**（TEST 1 / 2 / 3 / 1024 /
 * SHA(abc)，共 5 条）在**双路径**各自复算通过：
 * - 回退路径（[Ed25519Fallback]，net.i2p.crypto:eddsa——API 24~32 设备实际路径）；
 * - JCA 路径（[Ed25519]，宿主 JDK17 SunEC——CI/JVM 路径与 API 33+ 设备路径）。
 *
 * 每条向量断言四件事：签名逐字节等于官方向量、本路径验签通过、**对侧路径验签
 * 通过**（交叉互认=行为等价的最低门槛）、篡改必败（消息/签名各一字节）。
 * 向量来源与提取方式见 [Rfc8032Ed25519Vectors]。
 */
class Ed25519T26VectorTest {

    @Test
    fun `回退路径官方5向量签名复算与交叉验签`() {
        Rfc8032Ed25519Vectors.all.forEach { v ->
            val seed = Hex.decode(v.seedHex)
            val msg = Hex.decode(v.messageHex)
            val expectedSig = Hex.decode(v.signatureHex)
            val publicRaw = Hex.decode(v.publicHex)

            // 签名复算：回退路径逐字节等于官方向量
            val sig = Ed25519Fallback.sign(seed, msg)
            assertArrayEquals("回退路径签名不符（${v.name}）", expectedSig, sig)

            // 公钥推导：种子 → raw 公钥 = 官方向量公钥
            assertArrayEquals("回退路径公钥推导不符（${v.name}）", publicRaw, Ed25519Fallback.publicFromSeed(seed))

            // 验签：回退路径自洽 + JCA 路径交叉互认（宿主 SunEC）
            assertTrue("回退验签失败（${v.name}）", Ed25519Fallback.verify(publicRaw, msg, sig))
            assertTrue("JCA 交叉验签失败（${v.name}）", Ed25519.verify(publicRaw, msg, sig))

            // 篡改必败（fail-closed，两路径一致）。TEST 1 消息为空 → 篡改=改为非空消息
            val tamperedSig = sig.copyOf().also { it[63] = (it[63].toInt() xor 0x01).toByte() }
            assertFalse(Ed25519Fallback.verify(publicRaw, msg, tamperedSig))
            assertFalse(Ed25519.verify(publicRaw, msg, tamperedSig))
            val tamperedMsg = if (msg.isEmpty()) {
                byteArrayOf(0x01)
            } else {
                msg.copyOf().also { it[0] = (it[0].toInt() xor 0x01).toByte() }
            }
            assertFalse(Ed25519Fallback.verify(publicRaw, tamperedMsg, sig))
            assertFalse(Ed25519.verify(publicRaw, tamperedMsg, sig))
        }
    }

    @Test
    fun `JCA路径官方5向量签名复算与交叉验签`() {
        Rfc8032Ed25519Vectors.all.forEach { v ->
            val seed = Hex.decode(v.seedHex)
            val msg = Hex.decode(v.messageHex)
            val expectedSig = Hex.decode(v.signatureHex)
            val publicRaw = Hex.decode(v.publicHex)

            // 种子 → PKCS#8 → JCA 私钥（RFC 8410 编码，Ed25519.pkcs8FromSeed）
            val priv = Ed25519.decodePrivatePkcs8(Ed25519.pkcs8FromSeed(seed))
            assertEquals(32, Ed25519.publicRaw(Ed25519.importPublic(publicRaw)).size)

            // 签名复算：JCA 路径逐字节等于官方向量（确定性签名 → 与回退路径输出恒等）
            val sig = Ed25519.sign(priv, msg)
            assertArrayEquals("JCA 路径签名不符（${v.name}）", expectedSig, sig)

            // 验签：JCA 自洽 + 回退路径交叉互认
            assertTrue(Ed25519.verify(publicRaw, msg, sig))
            assertTrue("回退交叉验签失败（${v.name}）", Ed25519Fallback.verify(publicRaw, msg, sig))
        }
    }

    @Test
    fun `回退路径fail-closed输入形状`() {
        val v = Rfc8032Ed25519Vectors.all.first { it.name == "2" }
        val publicRaw = Hex.decode(v.publicHex)
        val sig = Hex.decode(v.signatureHex)
        val msg = Hex.decode(v.messageHex)

        // 公钥/签名长度非法 → false（不抛），与 JCA 路径行为等价
        assertFalse(Ed25519Fallback.verify(ByteArray(31), msg, sig))
        assertFalse(Ed25519Fallback.verify(publicRaw, msg, sig.copyOf(63)))
        assertFalse(Ed25519.verify(ByteArray(31), msg, sig))
        // 种子长度非法 → IllegalArgumentException（生成侧响亮失败）
        var threw = false
        try {
            Ed25519Fallback.sign(ByteArray(31), msg)
        } catch (_: IllegalArgumentException) {
            threw = true
        }
        assertTrue(threw)
    }
}
