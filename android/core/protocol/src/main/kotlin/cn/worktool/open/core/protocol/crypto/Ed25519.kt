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
import java.security.Signature
import java.security.spec.PKCS8EncodedKeySpec
import java.security.spec.X509EncodedKeySpec

/**
 * Ed25519（RFC 8032）签名：配对时注册的签名公钥 + 凭据重协商签名
 * （protocol/spec.md §5.1.2 / §5.5.1：`Ed25519 签名(deviceId|ts|nonce)`）。
 *
 * 实现 = Java Security API（JDK17 内置 SunEC），不引入第三方加密库。
 * **本对象 = JCA 路径**（API 33+ 设备 / 宿主 JVM）；API 24~32 无平台 Ed25519
 * （W-M3R D1 Android 12 实证：KeyPairGenerator/KeyFactory/Signature 全缺，
 * 产品缺陷 → T2.6），纯 JVM 回退提供者与双路径选择见 [Ed25519Keys] /
 * [Ed25519Fallback]（JCA 优先不变，回退仅 JCA 不可用时启用）。
 * raw 32 字节公钥 ↔ JDK 密钥对象互转走 X.509 SubjectPublicKeyInfo 编码
 * （SunEC EdDSA KeyFactory 不支持 XECPublicKeySpec）。
 * 生产端侧密钥应从 AndroidKeystore 注入（SR-05/G5，接口见 core:protocol-android）。
 */
object Ed25519 {

    /** Ed25519 公钥 X.509 SPKI 前缀（12 字节）+ 32 字节 raw key。 */
    private val SPKI_PREFIX = Hex.decode("302a300506032b6570032100")

    /** Ed25519 私钥 PKCS#8 固定头（RFC 8410 §7：curvePrivateKey OCTET STRING）。 */
    internal val PKCS8_PREFIX = Hex.decode("302e020100300506032b657004220420")

    /** 生成新的 Ed25519 签名密钥对（JCA 路径）。 */
    fun generateKeyPair(): KeyPair = keyPairGenerator().genKeyPair()

    /** 导出 raw 签名公钥（32 字节，pair-request 的 `ed25519PublicKey` hex 字段）。 */
    fun publicRaw(publicKey: PublicKey): ByteArray {
        val spki = publicKey.encoded
        require(spki.size == SPKI_PREFIX.size + 32) { "Ed25519 SPKI 长度异常: ${spki.size}" }
        return spki.copyOfRange(SPKI_PREFIX.size, spki.size)
    }

    /** 从 raw 32 字节签名公钥导入 JDK 公钥对象（服务端/对端验签用）。 */
    fun importPublic(raw: ByteArray): PublicKey {
        require(raw.size == 32) { "Ed25519 公钥必须 32 字节，收到 ${raw.size}" }
        return keyFactory()
            .generatePublic(X509EncodedKeySpec(SPKI_PREFIX + raw))
    }

    /** 签名（64 字节 detached，JCA 路径）。 */
    fun sign(privateKey: PrivateKey, message: ByteArray): ByteArray {
        val sig = signature()
        sig.initSign(privateKey)
        sig.update(message)
        return sig.sign()
    }

    fun verify(publicRawKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean {
        return try {
            val sig = signature()
            sig.initVerify(importPublic(publicRawKey))
            sig.update(message)
            sig.verify(signature)
        } catch (_: Exception) {
            false
        }
    }

    /** 私钥 PKCS#8 序列化（重协商窗口保存；生产应存 Keystore，不落盘明文）。 */
    fun encodePrivatePkcs8(privateKey: PrivateKey): ByteArray = privateKey.encoded

    fun decodePrivatePkcs8(pkcs8: ByteArray): PrivateKey =
        keyFactory().generatePrivate(PKCS8EncodedKeySpec(pkcs8))

    /** raw 32 字节种子 → PKCS#8（RFC 8410：测试导入固定向量种子 / 跨路径构造）。 */
    internal fun pkcs8FromSeed(seed: ByteArray): ByteArray {
        require(seed.size == 32) { "Ed25519 种子必须 32 字节，收到 ${seed.size}" }
        return PKCS8_PREFIX + seed
    }

    // ---- 算法名双探测（X25519 同范式；internal 供 JVM 单测直打 fallback 分支）----
    // JDK17 SunEC 注册别名 "Ed25519"；部分 JCA 提供者仅注册标准名 "EdDSA"
    // （RFC 8032/JCA 惯例，如未来 Conscrypt 版本）。两名均不可用 → 抛原始
    // NoSuchAlgorithmException（由 [Ed25519Keys] 捕获转入回退路径）。

    internal fun keyPairGenerator(
        alias: String = "Ed25519",
        standard: String = "EdDSA",
    ): KeyPairGenerator = try {
        KeyPairGenerator.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        KeyPairGenerator.getInstance(standard)
    }

    internal fun keyFactory(
        alias: String = "Ed25519",
        standard: String = "EdDSA",
    ): KeyFactory = try {
        KeyFactory.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        KeyFactory.getInstance(standard)
    }

    internal fun signature(
        alias: String = "Ed25519",
        standard: String = "EdDSA",
    ): Signature = try {
        Signature.getInstance(alias)
    } catch (_: NoSuchAlgorithmException) {
        Signature.getInstance(standard)
    }
}
