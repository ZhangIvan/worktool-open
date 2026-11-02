/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.PrivateKey
import java.security.PublicKey
import java.security.interfaces.XECPublicKey
import java.security.spec.PKCS8EncodedKeySpec
import java.security.spec.X509EncodedKeySpec
import java.security.spec.XECPublicKeySpec
import java.security.spec.NamedParameterSpec

/**
 * X25519（RFC 7748）：临时密钥对生成 / raw 公钥导入导出 / ECDH 共享密钥。
 *
 * 实现 = Java Security API（KeyPairGenerator/KeyFactory/KeyAgreement，JDK17 内置
 * SunEC），不引入任何第三方加密库（docs/03 T2.2 任务卡约束「非加密库堆叠」）。
 * 用途（protocol/spec.md §5.1.2/§5.3）：配对/重协商提交临时公钥，
 * `shared = X25519(设备临时私钥, serverX25519PublicKey)` 后经 HKDF 派生 K_session。
 *
 * raw 32 字节公钥 ↔ JDK 密钥对象的互转按 RFC 7748 little-endian 编码。
 */
object X25519 {

    private val PARAM = NamedParameterSpec.X25519

    /** 生成新的 X25519 临时密钥对（每次配对/重协商新生成，spec §5.3）。 */
    fun generateKeyPair(): KeyPair = KeyPairGenerator.getInstance("X25519").genKeyPair()

    /** 导出 raw 公钥（32 字节 little-endian u 坐标）。 */
    fun publicRaw(publicKey: PublicKey): ByteArray {
        val u = (publicKey as XECPublicKey).u
        return uToLittleEndian32(u)
    }

    /** 从 raw 32 字节公钥导入 JDK 公钥对象。 */
    fun importPublic(raw: ByteArray): PublicKey {
        require(raw.size == 32) { "X25519 公钥必须 32 字节，收到 ${raw.size}" }
        val u = littleEndian32ToU(raw)
        return KeyFactory.getInstance("X25519")
            .generatePublic(XECPublicKeySpec(PARAM, u))
    }

    /**
     * ECDH 共享密钥（32 字节）。设备侧：本地临时私钥 × 服务端临时公钥（spec §5.3）。
     */
    fun sharedSecret(privateKey: PrivateKey, peerPublicRaw: ByteArray): ByteArray {
        val kf = KeyFactory.getInstance("X25519")
        val agreement = javax.crypto.KeyAgreement.getInstance("X25519")
        agreement.init(privateKey)
        agreement.doPhase(importPublic(peerPublicRaw), true)
        return agreement.generateSecret()
    }

    /** 私钥序列化为 PKCS#8（用于配对窗口内临时保存/恢复；不落明文日志）。 */
    fun encodePrivatePkcs8(privateKey: PrivateKey): ByteArray = privateKey.encoded

    fun decodePrivatePkcs8(pkcs8: ByteArray): PrivateKey =
        KeyFactory.getInstance("X25519").generatePrivate(PKCS8EncodedKeySpec(pkcs8))

    private fun uToLittleEndian32(u: BigInteger): ByteArray {
        var big = u.toByteArray() // big-endian，可能带符号位前导 0
        if (big.size > 1 && big[0].toInt() == 0) big = big.copyOfRange(1, big.size)
        require(big.size <= 32) { "u 坐标超长: ${big.size}" }
        val out = ByteArray(32)
        for (i in big.indices) out[i] = big[big.size - 1 - i]
        return out
    }

    private fun littleEndian32ToU(raw: ByteArray): BigInteger = BigInteger(1, raw.reversedArray())
}
