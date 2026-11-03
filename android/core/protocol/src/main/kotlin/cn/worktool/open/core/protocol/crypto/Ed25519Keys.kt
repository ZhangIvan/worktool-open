/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import java.security.KeyPair
import java.security.PrivateKey

/**
 * Ed25519 签名密钥**双路径提供者**（T2.6 产品级回退，W-M4）。
 *
 * 背景（产品缺陷，W-M3R D1 实证 + 编排者裁决 dwfq-f63d4173-1）：API 24~32
 * 平台无任何 Ed25519 JCA/Keystore 路径（KeyPairGenerator/KeyFactory/Signature
 * 全缺，Conscrypt 未注册），spec §5.1.2 配对必填字段 `ed25519PublicKey` 在
 * Android 7~12 设备不可生成。
 *
 * 路径优先级（冻结，勿改序）：
 * 1. **设备 JCA**（API 33+ Conscrypt / AndroidKeystore / 宿主 JVM SunEC）——
 *    优先级不变（T2.6 约束「仅作 JCA 不可用时的回退」）；
 * 2. **外部注入公钥**（W-M3R 裁决 B：lab 一次性设备外生成密钥，资产存在则
 *    **优先于**回退生成——保持 D1 lab 连续性：同设备重复配对注册公钥不变）；
 * 3. **纯 JVM 回退生成**（[Ed25519Fallback]，仅上述两者皆不可用时）。
 *
 * 用法：`Ed25519Keys.resolve(externalPublicKeyHex = labAssetHex)`（lab 场景，
 * 与 LabPairingActivity 既有「JCA try/catch → lab 资产」语义一致并升级为
 * 三级）；产品场景 `resolve(persistedFallbackSeedHex = 加密存档的种子)`。
 *
 * SR-05 注意：API<33 AndroidKeystore 同样无 Ed25519（无法 KeyStore 托管回退
 * 私钥）——回退种子的加密持久化归 app 层（Keystore AES-GCM 包裹或等价），
 * 本层仅提供 `fallbackSeedHex` 导出与恢复入口，不落任何存储。
 */
object Ed25519Keys {

    /** 密钥来源（决定可签名性与审计标注）。 */
    enum class Origin {
        /** 设备 JCA 生成（API 33+/JVM；可签名，可 Keystore 托管）。 */
        DEVICE_JCA,

        /** 外部注入公钥（lab 裁决 B；**无私钥，不可签名**→重协商 NOT_RUN）。 */
        EXTERNAL_PUBLIC_ONLY,

        /** 纯 JVM 回退生成/恢复（API 24~32；可签名，种子由调用方加密持久化）。 */
        DEVICE_FALLBACK,
    }

    /** 解析产物：raw 公钥 + 来源 +（可签名路径的）私钥材料。 */
    class SigningKey internal constructor(
        /** raw 32 字节签名公钥（pair-request `ed25519PublicKey` 的字节源）。 */
        val publicRaw: ByteArray,
        /** 公钥小写 hex（直接填 [cn.worktool.open.core.protocol.pair.PairRequest]）。 */
        val publicHex: String,
        val origin: Origin,
        internal val jcaPrivate: PrivateKey?,
        internal val fallbackSeed: ByteArray?,
    ) {
        /** EXTERNAL_PUBLIC_ONLY = false（无私钥；重协商 NOT_RUN，见 [sign]）。 */
        val signCapable: Boolean get() = origin != Origin.EXTERNAL_PUBLIC_ONLY

        /** 回退种子 hex（origin=DEVICE_FALLBACK 时非空；调用方加密持久化后可经
         *  [resolve] 的 persistedFallbackSeedHex 复用，保证重复配对公钥稳定）。 */
        val fallbackSeedHex: String? get() = fallbackSeed?.let(Hex::encode)
    }

    @Volatile
    private var jcaProbe: Boolean? = null

    /** JCA Ed25519 能力探针（进程内缓存；true=设备/宿主具备平台路径）。 */
    fun jcaAvailable(): Boolean = jcaProbe ?: probe().also { jcaProbe = it }

    /**
     * 解析签名密钥（三级优先级，见对象头注）。
     *
     * @param externalPublicKeyHex 外部注入公钥（lab 资产；null=无）。形状非法
     *   （非 64 位小写 hex）**fail-closed 抛 [IllegalArgumentException]**，绝不
     *   静默降级（对齐 lab 资产装载语义）。
     * @param persistedFallbackSeedHex 加密存档恢复的回退种子（产品路径；null=无，
     *   回退路径将新生成）。形状非法同样 fail-closed。
     * @throws IllegalStateException 三级全部不可用（回退库自身初始化失败——
     *   理论不可达，防御性响亮失败，不静默返回不可用密钥）。
     */
    fun resolve(
        externalPublicKeyHex: String? = null,
        persistedFallbackSeedHex: String? = null,
    ): SigningKey = resolveInternal(jcaAvailable(), externalPublicKeyHex, persistedFallbackSeedHex)

    // internal：供 JVM 单测直打 JCA 缺席分支（宿主机 SunEC 恒在，无法自然走到）。
    internal fun resolveInternal(
        jcaPreferred: Boolean,
        externalPublicKeyHex: String?,
        persistedFallbackSeedHex: String?,
    ): SigningKey {
        // 1. 设备 JCA 优先（不变）
        if (jcaPreferred) {
            try {
                val pair: KeyPair = Ed25519.generateKeyPair()
                return signingKeyFromJca(pair)
            } catch (_: java.security.NoSuchAlgorithmException) {
                // 探针与生成竞态（提供者被卸载等）：落入下一级，不静默失败
            }
        }
        // 2. 外部注入公钥（lab 裁决 B：资产存在 → 优先于回退生成）
        if (externalPublicKeyHex != null) {
            require(externalPublicKeyHex.matches(HEX_32B)) { "外部注入 Ed25519 公钥形状非法（须 64 位小写 hex）" }
            return SigningKey(
                publicRaw = Hex.decode(externalPublicKeyHex),
                publicHex = externalPublicKeyHex,
                origin = Origin.EXTERNAL_PUBLIC_ONLY,
                jcaPrivate = null,
                fallbackSeed = null,
            )
        }
        // 3. 纯 JVM 回退：持久化种子恢复优先，否则新生成
        if (persistedFallbackSeedHex != null) {
            require(persistedFallbackSeedHex.matches(HEX_32B)) { "回退 Ed25519 种子形状非法（须 64 位小写 hex）" }
            val seed = Hex.decode(persistedFallbackSeedHex)
            return signingKeyFromFallback(seed)
        }
        val fresh = Ed25519Fallback.generateKeyPair()
        return SigningKey(
            publicRaw = fresh.publicRaw,
            publicHex = Hex.encode(fresh.publicRaw),
            origin = Origin.DEVICE_FALLBACK,
            jcaPrivate = null,
            fallbackSeed = fresh.seed,
        )
    }

    /**
     * 签名（spec §5.5.1 重协商：`Ed25519 签名(deviceId|ts|nonce)`）。
     * 按密钥来源路由实现（JCA / 回退），两路径 RFC 8032 官方向量全过且行为
     * 等价（交叉验证互认，见 `Ed25519T26DualPathTest`）。
     *
     * @throws UnsupportedOperationException origin=EXTERNAL_PUBLIC_ONLY（无私钥，
     *   lab 裁决 B 设备重协商 NOT_RUN 的显式契约，调用方须前置检查 signCapable）。
     */
    fun sign(key: SigningKey, message: ByteArray): ByteArray = when (key.origin) {
        Origin.DEVICE_JCA ->
            Ed25519.sign(key.jcaPrivate ?: error("DEVICE_JCA 密钥缺少私钥材料"), message)
        Origin.DEVICE_FALLBACK ->
            Ed25519Fallback.sign(key.fallbackSeed ?: error("DEVICE_FALLBACK 密钥缺少种子"), message)
        Origin.EXTERNAL_PUBLIC_ONLY ->
            throw UnsupportedOperationException(
                "外部注入公钥无私钥（lab 裁决 B）：Ed25519 签名不可用（重协商 NOT_RUN）",
            )
    }

    /**
     * 验签（公钥 raw 字节输入，路径无关）。JCA 可用走 JCA，否则回退——
     * 输入形状非法一律 false（fail-closed，两路径行为等价）。
     */
    fun verify(publicRawKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean =
        verifyInternal(jcaAvailable(), publicRawKey, message, signature)

    // internal：供 JVM 单测直打 JCA 缺席分支（宿主 SunEC 恒在）。
    internal fun verifyInternal(
        jcaAvailable: Boolean,
        publicRawKey: ByteArray,
        message: ByteArray,
        signature: ByteArray,
    ): Boolean = if (jcaAvailable) {
        Ed25519.verify(publicRawKey, message, signature)
    } else {
        Ed25519Fallback.verify(publicRawKey, message, signature)
    }

    private fun probe(): Boolean = try {
        Ed25519.keyPairGenerator()
        true
    } catch (_: java.security.NoSuchAlgorithmException) {
        false
    }

    private fun signingKeyFromJca(pair: KeyPair): SigningKey = SigningKey(
        publicRaw = Ed25519.publicRaw(pair.public),
        publicHex = Hex.encode(Ed25519.publicRaw(pair.public)),
        origin = Origin.DEVICE_JCA,
        jcaPrivate = pair.private,
        fallbackSeed = null,
    )

    private fun signingKeyFromFallback(seed: ByteArray): SigningKey {
        val publicRaw = Ed25519Fallback.publicFromSeed(seed)
        return SigningKey(
            publicRaw = publicRaw,
            publicHex = Hex.encode(publicRaw),
            origin = Origin.DEVICE_FALLBACK,
            jcaPrivate = null,
            fallbackSeed = seed,
        )
    }

    private val HEX_32B = Regex("^[0-9a-f]{64}$")
}
