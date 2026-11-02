/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import java.security.KeyFactory
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.NoSuchAlgorithmException
import java.security.PrivateKey
import java.security.PublicKey
import java.security.spec.PKCS8EncodedKeySpec
import java.security.spec.X509EncodedKeySpec
import javax.crypto.KeyAgreement

/**
 * X25519（RFC 7748）：临时密钥对生成 / raw 公钥导入导出 / ECDH 共享密钥。
 *
 * 实现 = Java Security API（KeyPairGenerator/KeyFactory/KeyAgreement，JDK17 内置
 * SunEC），不引入任何第三方加密库（docs/03 T2.2 任务卡约束「非加密库堆叠」）。
 * 用途（protocol/spec.md §5.1.2/§5.3）：配对/重协商提交临时公钥，
 * `shared = X25519(设备临时私钥, serverX25519PublicKey)` 后经 HKDF 派生 K_session。
 *
 * 算法名双探测（W-M3R 真机装配实证，D1 Android 12/SDK 31 Conscrypt）：
 * - JDK17 SunEC 直接注册别名 "X25519"；
 * - Android 12-（Conscrypt）仅注册 JCA 标准名 "XDH"（NamedParameterSpec.X25519
 *   参数化；KeyPairGenerator/KeyFactory/KeyAgreement 同此，实测
 *   AndroidOpenSSL 提供者支持）。
 * 两名择一可用即用；均不可用抛原始 NoSuchAlgorithmException。
 * raw 32 字节公钥 ↔ 密钥对象互转统一走 RFC 8410 X.509 SPKI
 * （44 字节 = 12 字节固定头 + 32 字节 little-endian u 坐标）——不依赖
 * java.security.interfaces.XECPublicKey 接口（Conscrypt 公钥类未实现该接口）。
 */
object X25519 {

    /** X25519 SPKI 固定头（OID 1.3.101.110，RFC 8410 §4）。 */
    private val SPKI_PREFIX = Hex.decode("302a300506032b656e032100")

    /** 生成新的 X25519 临时密钥对（每次配对/重协商新生成，spec §5.3）。 */
    fun generateKeyPair(): KeyPair = keyPairGenerator().genKeyPair()

    /** 导出 raw 公钥（32 字节 little-endian u 坐标，RFC 8410 SPKI 尾部）。 */
    fun publicRaw(publicKey: PublicKey): ByteArray {
        val spki = publicKey.encoded
        require(
            spki.size == SPKI_PREFIX.size + 32 &&
                spki.copyOfRange(0, SPKI_PREFIX.size).contentEquals(SPKI_PREFIX),
        ) { "X25519 SPKI 形态异常: len=${spki.size}" }
        return spki.copyOfRange(SPKI_PREFIX.size, spki.size)
    }

    /** 从 raw 32 字节公钥导入 JDK 公钥对象（X509EncodedKeySpec，两提供者通用）。 */
    fun importPublic(raw: ByteArray): PublicKey {
        require(raw.size == 32) { "X25519 公钥必须 32 字节，收到 ${raw.size}" }
        return keyFactory().generatePublic(X509EncodedKeySpec(SPKI_PREFIX + raw))
    }

    /**
     * ECDH 共享密钥（32 字节）。设备侧：本地临时私钥 × 服务端临时公钥（spec §5.3）。
     */
    fun sharedSecret(privateKey: PrivateKey, peerPublicRaw: ByteArray): ByteArray {
        val agreement = keyAgreement()
        agreement.init(privateKey)
        agreement.doPhase(importPublic(peerPublicRaw), true)
        return agreement.generateSecret()
    }

    /** 私钥序列化为 PKCS#8（用于配对窗口内临时保存/恢复；不落明文日志）。 */
    fun encodePrivatePkcs8(privateKey: PrivateKey): ByteArray = privateKey.encoded

    fun decodePrivatePkcs8(pkcs8: ByteArray): PrivateKey =
        keyFactory().generatePrivate(PKCS8EncodedKeySpec(pkcs8))

    // ---- 双名探测（internal：供 JVM 单测直接覆盖 fallback 分支）----

    internal fun keyPairGenerator(
        alias: String = "X25519",
        standard: String = "XDH",
    ): KeyPairGenerator = try {
        KeyPairGenerator.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        KeyPairGenerator.getInstance(standard).also { initXdhParams(it) }
    }

    /**
     * XDH KPG 参数初始化：NamedParameterSpec 为 API 33+（Android）/JDK11+（JVM）类——
     * Android ≤12 无该类（NoClassDefFoundError，W-M3R D1 实证）时依赖提供者默认
     * 参数：Conscrypt XDH 仅实现 X25519 一种曲线，默认即 X25519。若提供者默认非
     * X25519，[publicRaw] 的 SPKI 形状校验将 fail-fast。
     */
    private fun initXdhParams(kpg: KeyPairGenerator) {
        try {
            kpg.initialize(java.security.spec.NamedParameterSpec.X25519)
        } catch (_: NoClassDefFoundError) {
            // Android ≤12：Conscrypt XDH 默认 = X25519
        }
    }

    internal fun keyFactory(
        alias: String = "X25519",
        standard: String = "XDH",
    ): KeyFactory = try {
        KeyFactory.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        KeyFactory.getInstance(standard)
    }

    internal fun keyAgreement(
        alias: String = "X25519",
        standard: String = "XDH",
    ): KeyAgreement = try {
        KeyAgreement.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        KeyAgreement.getInstance(standard)
    }
}
