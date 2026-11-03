/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.crypto

import java.security.SecureRandom
import net.i2p.crypto.eddsa.EdDSAEngine
import net.i2p.crypto.eddsa.EdDSAPrivateKey
import net.i2p.crypto.eddsa.EdDSAPublicKey
import net.i2p.crypto.eddsa.spec.EdDSANamedCurveTable
import net.i2p.crypto.eddsa.spec.EdDSAParameterSpec
import net.i2p.crypto.eddsa.spec.EdDSAPrivateKeySpec
import net.i2p.crypto.eddsa.spec.EdDSAPublicKeySpec

/**
 * Ed25519 纯 JVM 回退提供者（T2.6，W-M4；docs/03 任务卡 + 编排者裁决
 * dwfq-f63d4173-1：对 T2.2「非加密库堆叠」约束的**定点豁免**——唯一加密回退依赖）。
 *
 * 载体：`net.i2p.crypto:eddsa:0.3.0`（str4d/ed25519-java，CC0 1.0 Universal）。
 * 选型审计理由（对 T2.6 DoD「AUD-07 复核依赖面」）：
 * 1. **零传递依赖**（POM 无 compile 依赖；构建脚本 `pinEd25519Fallback` 任务
 *    机械断言解析结果恰好 1 个工件）——依赖面不随引入扩张；
 * 2. **纯 JVM**（jar 仅 class 文件，无 native/.so/动态类加载面；Java 6
 *    字节码 → API 24+ 全覆盖），不经 JCA Provider 注册即可直接调用；
 * 3. **单一用途**：仅 Ed25519（非 BouncyCastle 式全栈加密库，符合豁免的
 *    「窄」边界）；
 * 4. **钉死校验和**：版本与 sha256 在模块 build.gradle.kts 集中钉死，解析
 *    即校验，篡改/漂移构建失败；
 * 5. RFC 8032 §7.1 官方向量全过（`Ed25519T26VectorTest`，双路径 × 5 向量；
 *    向量夹具机械提取自 RFC Editor 官方文本，见 `Rfc8032Ed25519Vectors`）。
 *
 * **本对象不得被产品路径直接引用**：唯一入口 = [Ed25519Keys]（JCA 优先，
 * 仅 API<33 等 JCA 不可用场景回落至此）。JCA 行为等价性由
 * `Ed25519T26DualPathTest` 证明（交叉签名/交叉验签 + 官方向量双侧复算）。
 */
object Ed25519Fallback {

    private val spec: EdDSAParameterSpec =
        EdDSANamedCurveTable.getByName(EdDSANamedCurveTable.ED_25519)

    /** 回退路径密钥对：raw 32 字节种子 + raw 32 字节公钥（无 JCA 密钥对象）。 */
    class KeyPair internal constructor(
        /** 签名种子（= RFC 8032 私钥；须由调用方加密持久化，见 [Ed25519Keys]）。 */
        val seed: ByteArray,
        /** raw 签名公钥（pair-request `ed25519PublicKey` hex 的字节源）。 */
        val publicRaw: ByteArray,
    )

    /** 回退路径生成新密钥对（每次配对/重协商新生成，spec §5.5.1）。 */
    fun generateKeyPair(random: SecureRandom = SecureRandom()): KeyPair {
        val kpg = net.i2p.crypto.eddsa.KeyPairGenerator()
        kpg.initialize(256, random)
        val pair = kpg.generateKeyPair()
        val priv = pair.private as EdDSAPrivateKey
        val pub = pair.public as EdDSAPublicKey
        return KeyPair(priv.seed.copyOf(), pub.abyte.copyOf())
    }

    /** 由种子推导 raw 公钥（持久化种子恢复路径：公钥不落盘、按需推导）。 */
    fun publicFromSeed(seed: ByteArray): ByteArray {
        require(seed.size == 32) { "Ed25519 种子必须 32 字节，收到 ${seed.size}" }
        return EdDSAPrivateKey(EdDSAPrivateKeySpec(seed, spec)).abyte.copyOf()
    }

    /** 签名（64 字节 detached，RFC 8032 §5.1.6）。种子非法抛 [IllegalArgumentException]。 */
    fun sign(seed: ByteArray, message: ByteArray): ByteArray {
        require(seed.size == 32) { "Ed25519 种子必须 32 字节，收到 ${seed.size}" }
        val engine = EdDSAEngine()
        engine.initSign(EdDSAPrivateKey(EdDSAPrivateKeySpec(seed, spec)))
        engine.update(message, 0, message.size)
        return engine.sign()
    }

    /**
     * 验签。输入形状非法（公钥/签名长度 ≠ 32/64）返回 false（fail-closed，
     * 不抛）——与 JCA 路径 [Ed25519.verify] 行为等价。
     */
    fun verify(publicRawKey: ByteArray, message: ByteArray, signature: ByteArray): Boolean {
        if (publicRawKey.size != 32 || signature.size != 64) return false
        return try {
            val engine = EdDSAEngine()
            engine.initVerify(EdDSAPublicKey(EdDSAPublicKeySpec(publicRawKey, spec)))
            engine.update(message, 0, message.size)
            engine.verify(signature)
        } catch (_: Exception) {
            false
        }
    }
}
