/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import cn.worktool.open.core.protocol.FixtureFiles
import cn.worktool.open.core.protocol.pair.PairingClient
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.util.concurrent.TimeUnit

/**
 * T2.6 双路径等价 + 三级优先级（JCA 优先不变 / lab 资产次之 / 回退生成兜底）
 * + 与重协商链路（spec §5.5.1）的集成。
 *
 * 宿主 JVM（JDK17 SunEC）恒有 JCA 路径 → 「JCA 缺席」分支经
 * [Ed25519Keys.resolveInternal]（jcaPreferred=false）确定性直打；
 * 「JCA 在场」分支即 [Ed25519Keys.resolve] 默认路径（真实宿主/真机 API 33+ 形态）。
 */
class Ed25519T26DualPathTest {

    private val vector1 = Rfc8032Ed25519Vectors.all.first { it.name == "1" }

    // ---- 三级优先级（冻结序：设备 JCA > 外部注入公钥 > 回退生成）----

    @Test
    fun `resolve 默认路径=设备JCA（宿主SunEC在场，JCA优先不变）`() {
        assertTrue("宿主 JVM 必须具备 JCA Ed25519（否则本测试环境异常）", Ed25519Keys.jcaAvailable())

        val key = Ed25519Keys.resolve()
        assertEquals(Ed25519Keys.Origin.DEVICE_JCA, key.origin)
        assertTrue(key.signCapable)
        assertTrue(key.publicHex.matches(Regex("^[0-9a-f]{64}$")))
        assertEquals(null, key.fallbackSeedHex)

        val msg = "device-9f2c7b41|1793613605000|4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d".toByteArray()
        val sig = Ed25519Keys.sign(key, msg)
        assertEquals(64, sig.size)
        // 验签走 JCA（宿主在场）；回退路径交叉互认
        assertTrue(Ed25519Keys.verify(Hex.decode(key.publicHex), msg, sig))
        assertTrue(Ed25519Fallback.verify(Hex.decode(key.publicHex), msg, sig))
    }

    @Test
    fun `JCA缺席+资产在场=外部注入公钥（裁决B，优先于回退生成）`() {
        val key = Ed25519Keys.resolveInternal(
            jcaPreferred = false,
            externalPublicKeyHex = vector1.publicHex,
            persistedFallbackSeedHex = null,
        )
        assertEquals(Ed25519Keys.Origin.EXTERNAL_PUBLIC_ONLY, key.origin)
        assertEquals(vector1.publicHex, key.publicHex)
        assertFalse(key.signCapable)

        var threw = false
        try {
            Ed25519Keys.sign(key, ByteArray(0))
        } catch (_: UnsupportedOperationException) {
            threw = true // lab 裁决 B：注入公钥无私钥 → 重协商 NOT_RUN 的显式契约
        }
        assertTrue("EXTERNAL_PUBLIC_ONLY 签名必须抛 UnsupportedOperationException", threw)
    }

    @Test
    fun `JCA缺席+无资产=回退生成（API 24~32 产品路径）`() {
        val key = Ed25519Keys.resolveInternal(jcaPreferred = false, externalPublicKeyHex = null, persistedFallbackSeedHex = null)
        assertEquals(Ed25519Keys.Origin.DEVICE_FALLBACK, key.origin)
        assertTrue(key.signCapable)
        assertEquals(64, key.fallbackSeedHex!!.length)

        val msg = ByteArray(200) { it.toByte() } // 重协商签名串量级
        val sig = Ed25519Keys.sign(key, msg)
        // 服务端（独立实现）视角：JCA 验签必须认可回退路径签名（互操作硬门槛）
        assertTrue(Ed25519.verify(Hex.decode(key.publicHex), msg, sig))
        assertTrue(Ed25519Keys.verify(Hex.decode(key.publicHex), msg, sig))
        assertFalse(Ed25519.verify(Hex.decode(key.publicHex), ByteArray(200) { 0 }, sig))
    }

    @Test
    fun `回退种子恢复=公钥稳定（重复配对不换钥）`() {
        val first = Ed25519Keys.resolveInternal(
            jcaPreferred = false,
            externalPublicKeyHex = null,
            persistedFallbackSeedHex = vector1.seedHex,
        )
        assertEquals(Ed25519Keys.Origin.DEVICE_FALLBACK, first.origin)
        assertEquals(vector1.publicHex, first.publicHex) // 种子 → 公钥 = 官方向量公钥
        assertEquals(vector1.seedHex, first.fallbackSeedHex)

        val second = Ed25519Keys.resolveInternal(
            jcaPreferred = false,
            externalPublicKeyHex = null,
            persistedFallbackSeedHex = first.fallbackSeedHex,
        )
        assertEquals(second.publicHex, first.publicHex) // 恢复路径确定性
    }

    @Test
    fun `JCA在场时资产与种子均不抢占（优先级序冻结）`() {
        val key = Ed25519Keys.resolveInternal(
            jcaPreferred = true,
            externalPublicKeyHex = vector1.publicHex,
            persistedFallbackSeedHex = vector1.seedHex,
        )
        assertEquals(Ed25519Keys.Origin.DEVICE_JCA, key.origin)
        assertNotEquals(vector1.publicHex, key.publicHex) // 设备自有新钥，非注入值
    }

    @Test
    fun `资产与种子形状非法fail-closed拒绝`() {
        listOf("XYZ", "d75a980182b10ab7d54bfed3c964073a", "D75A980182B10AB7D54BFED3C964073A0EE172F3DAA62325AF021A68F707511A").forEach { bad ->
            try {
                Ed25519Keys.resolveInternal(jcaPreferred = false, externalPublicKeyHex = bad, persistedFallbackSeedHex = null)
                fail("非法资产必须 fail-closed: $bad")
            } catch (_: IllegalArgumentException) {
            }
            try {
                Ed25519Keys.resolveInternal(jcaPreferred = false, externalPublicKeyHex = null, persistedFallbackSeedHex = bad)
                fail("非法种子必须 fail-closed: $bad")
            } catch (_: IllegalArgumentException) {
            }
        }
    }

    // ---- 双路径行为等价（随机密钥交叉；官方向量级见 Ed25519T26VectorTest）----

    @Test
    fun `回退生成密钥对与JCA密钥对交叉签名验签互认`() {
        val msg = "cross-path-equivalence".toByteArray()

        // 回退生成 → 回退签名 → JCA 验签（服务端视角）
        val fb = Ed25519Fallback.generateKeyPair()
        assertArrayEquals(fb.publicRaw, Ed25519Fallback.publicFromSeed(fb.seed))
        val fbSig = Ed25519Fallback.sign(fb.seed, msg)
        assertTrue(Ed25519.verify(fb.publicRaw, msg, fbSig))

        // JCA 生成 → JCA 签名 → 回退验签（API<33 设备验对端/自验视角）
        val jca = Ed25519.generateKeyPair()
        val jcaSig = Ed25519.sign(jca.private, msg)
        assertTrue(Ed25519Fallback.verify(Ed25519.publicRaw(jca.public), msg, jcaSig))

        // 门面 verify（路径无关入口）：两路径签名均认可
        assertTrue(Ed25519Keys.verify(fb.publicRaw, msg, fbSig))
        assertTrue(Ed25519Keys.verify(Ed25519.publicRaw(jca.public), msg, jcaSig))
        // 门面 verify 输入形状非法 → false（fail-closed 等价）
        assertFalse(Ed25519Keys.verify(ByteArray(31), msg, fbSig))
        assertFalse(Ed25519Keys.verify(fb.publicRaw, msg, ByteArray(63)))
    }

    @Test
    fun `EdDSA标准名fallback分支等价（算法名双探测）`() {
        // 以不存在别名强制走标准名 "EdDSA" 分支（SunEC 同样提供）——覆盖
        // keyPairGenerator/keyFactory/signature 的 fallback 分支（X25519 同范式）
        val kpg = Ed25519.keyPairGenerator(alias = "Ed25519-nonexistent", standard = "EdDSA")
        val pair = kpg.genKeyPair()
        val pubRaw = Ed25519.publicRaw(pair.public)

        val msg = "dual-name-probe".toByteArray()
        val sig = Ed25519.sign(pair.private, msg)
        // 标准名分支签名 → 别名路径验签 + 回退路径验签（三方一致）
        assertTrue(Ed25519.verify(pubRaw, msg, sig))
        assertTrue(Ed25519Fallback.verify(pubRaw, msg, sig))

        // 标准名 KeyFactory 分支：种子向量导入 → 公钥往返
        val v = vector1
        val priv = Ed25519.decodePrivatePkcs8(Ed25519.pkcs8FromSeed(Hex.decode(v.seedHex)))
        val pub = Ed25519.keyFactory(alias = "Ed25519-nonexistent", standard = "EdDSA")
            .generatePublic(java.security.spec.X509EncodedKeySpec(Hex.decode("302a300506032b6570032100") + Hex.decode(v.publicHex)))
        assertArrayEquals(Hex.decode(v.publicHex), Ed25519.publicRaw(pub))
        assertArrayEquals(Hex.decode(v.signatureHex), Ed25519.sign(priv, ByteArray(0)))
    }

    // ---- 重协商集成（spec §5.5.1：Ed25519 签名(deviceId|ts|nonce)）----

    @Test
    fun `重协商双路径重载-回退密钥签名可被服务端视角验证`() {
        val server = MockWebServer()
        server.enqueue(
            MockResponse()
                .setResponseCode(200)
                .setHeader("Content-Type", "application/json")
                .setBody(FixtureFiles.text("pair-response.valid.json")),
        )
        server.start(java.net.InetAddress.getByName("127.0.0.1"), 0)

        // API<33 设备形态：回退路径密钥（JCA 缺席 + 无 lab 资产 + 持久化种子恢复）
        val key = Ed25519Keys.resolveInternal(
            jcaPreferred = false,
            externalPublicKeyHex = null,
            persistedFallbackSeedHex = vector1.seedHex,
        )
        val client = PairingClient("http://127.0.0.1:" + server.port)
        val response = client.renegotiate("device-9f2c7b41", key)
        assertEquals("5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80", response.sessionId)

        // 服务端（独立实现）重算：签名串 deviceId|ts|nonce，公钥=官方向量公钥
        val recorded = server.takeRequest(5, TimeUnit.SECONDS)!!
        assertEquals("/api/v2/device-sessions", recorded.path)
        val body = Json.parseToJsonElement(recorded.body.readUtf8()).jsonObject
        val proof = body["renegotiate"]!!.jsonObject
        val signed = "${body["deviceId"]!!.jsonPrimitive.content}|" +
            "${proof["ts"]!!.jsonPrimitive.content}|" +
            "${proof["nonce"]!!.jsonPrimitive.content}"
        val sig = Hex.decode(proof["signature"]!!.jsonPrimitive.content)
        assertTrue(
            "回退路径重协商签名必须可被独立实现验证",
            Ed25519.verify(Hex.decode(vector1.publicHex), signed.toByteArray(Charsets.UTF_8), sig),
        )
        server.shutdown()
    }

    @Test
    fun `重协商双路径重载-外部注入密钥被前置拒绝（不发网络）`() {
        val key = Ed25519Keys.resolveInternal(
            jcaPreferred = false,
            externalPublicKeyHex = vector1.publicHex,
            persistedFallbackSeedHex = null,
        )
        val client = PairingClient("http://127.0.0.1:1/") // 端口 1 = 不可达；断言根本不发网络
        try {
            client.renegotiate("device-9f2c7b41", key)
            fail("EXTERNAL_PUBLIC_ONLY 必须被 require 拒绝")
        } catch (e: IllegalArgumentException) {
            assertTrue(e.message!!.contains("不可签名"))
        }
    }

    // ---- fail-closed / 防御分支（T2.6 新增分支全覆盖）----

    @Test
    fun `门面verify-JCA缺席分支走回退且结果一致`() {
        // API<33 设备形态：verifyInternal(false, …) = 回退路径验签（与 JCA 路径同果）
        val v = vector1
        val msg = "facade-verify-fallback-branch".toByteArray()
        val sig = Ed25519Fallback.sign(Hex.decode(v.seedHex), msg)
        assertTrue(Ed25519Keys.verifyInternal(false, Hex.decode(v.publicHex), msg, sig))
        assertFalse(Ed25519Keys.verifyInternal(false, Hex.decode(v.publicHex), ByteArray(0), sig))
    }

    @Test
    fun `签名路由防御分支-密钥材料缺失响亮失败`() {
        // internal 构造器直接构造非法态（同模块测试可及）：
        // DEVICE_JCA 无私钥 / DEVICE_FALLBACK 无种子 = 内部不变量破坏 →
        // IllegalStateException（绝不静默产生空签名）
        val brokenJca = Ed25519Keys.SigningKey(ByteArray(32), "00".repeat(32), Ed25519Keys.Origin.DEVICE_JCA, null, null)
        try {
            Ed25519Keys.sign(brokenJca, ByteArray(0))
            fail("DEVICE_JCA 缺私钥必须响亮失败")
        } catch (_: IllegalStateException) {
        }
        val brokenFallback = Ed25519Keys.SigningKey(ByteArray(32), "00".repeat(32), Ed25519Keys.Origin.DEVICE_FALLBACK, null, null)
        try {
            Ed25519Keys.sign(brokenFallback, ByteArray(0))
            fail("DEVICE_FALLBACK 缺种子必须响亮失败")
        } catch (_: IllegalStateException) {
        }
    }

    @Test
    fun `种子与PKCS8构造fail-closed`() {
        // Ed25519.pkcs8FromSeed / Ed25519Fallback.publicFromSeed：非 32 字节种子 → IAE
        listOf(ByteArray(31), ByteArray(33)).forEach { bad ->
            try {
                Ed25519.pkcs8FromSeed(bad)
                fail("pkcs8FromSeed 非 32 字节必须拒绝")
            } catch (_: IllegalArgumentException) {
            }
            try {
                Ed25519Fallback.publicFromSeed(bad)
                fail("publicFromSeed 非 32 字节必须拒绝")
            } catch (_: IllegalArgumentException) {
            }
        }
        // 32 字节正常路径：PKCS#8 头（RFC 8410）+ 种子
        val v = vector1
        assertArrayEquals(
            Hex.decode("302e020100300506032b657004220420" + v.seedHex),
            Ed25519.pkcs8FromSeed(Hex.decode(v.seedHex)),
        )
    }

    @Test
    fun `Signature标准名EdDSA fallback分支产出与别名路径恒等`() {
        // Ed25519.signature(alias=不存在) 强制走标准名 catch 分支；
        // Ed25519 签名确定性 → 与官方向量（别名路径）输出逐字节一致
        val v = vector1
        val priv = Ed25519.decodePrivatePkcs8(Ed25519.pkcs8FromSeed(Hex.decode(v.seedHex)))
        val engine = Ed25519.signature(alias = "Ed25519-nonexistent", standard = "EdDSA")
        engine.initSign(priv)
        engine.update(ByteArray(0))
        assertArrayEquals(Hex.decode(v.signatureHex), engine.sign())
    }
}
