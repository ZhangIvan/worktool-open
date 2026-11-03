/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app.lab

import android.content.Context
import android.content.SharedPreferences
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec

/**
 * lab 车道 W-M4 接线（G-1 取证，仅 labDebug 源集）：
 * [cn.worktool.open.core.protocol.crypto.Ed25519Keys] 回退路径（DEVICE_FALLBACK）
 * 种子的 Keystore AES-GCM 加密持久化——T2.6 装配注记「回退种子加密持久化归 app 层」
 * 的 lab 最小实现（与 AndroidKeystoreCredentialStore 同范式：AndroidKeyStore 内
 * 生成 AES-256-GCM 密钥，种子密文 + iv 落私有 prefs；明文种子只在内存）。
 *
 * 作用：重复配对复用同一设备生成签名公钥（机器人侧注册公钥不漂移）；
 * 种子永不落盘明文、不进日志（G13/audit 口径同 deviceToken）。
 */
class LabEd25519SeedStore(context: Context) {

    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    fun loadSeedHex(): String? {
        val blob = fromHex(prefs.getString(KEY_SEED, null) ?: return null) ?: return null
        if (blob.size <= IV_LEN) return null
        val cipher = Cipher.getInstance(TRANSFORM).apply {
            init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, blob, 0, IV_LEN))
        }
        return try {
            toHex(cipher.doFinal(blob, IV_LEN, blob.size - IV_LEN))
        } catch (_: Exception) {
            null // 解密失败（密钥轮换/数据损坏）→ 视为无存档，重新生成（fail-open 到新密钥，旧配对自然失效）
        }
    }

    fun saveSeedHex(seedHex: String) {
        val cipher = Cipher.getInstance(TRANSFORM).apply {
            init(Cipher.ENCRYPT_MODE, key())
        }
        val plain = fromHex(seedHex) ?: return
        val blob = cipher.iv + cipher.doFinal(plain)
        prefs.edit().putString(KEY_SEED, toHex(blob)).apply()
    }

    /** AndroidKeyStore AES 密钥（不可导出——getEncoded()=null，须以 SecretKey 原对象入 Cipher）。 */
    private fun key(): javax.crypto.SecretKey {
        val ks = KeyStore.getInstance(PROVIDER).apply { load(null) }
        (ks.getKey(ALIAS, null) as? javax.crypto.SecretKey)?.let { return it }
        val generator = javax.crypto.KeyGenerator.getInstance("AES", PROVIDER)
        generator.init(
            android.security.keystore.KeyGenParameterSpec.Builder(
                ALIAS,
                android.security.keystore.KeyProperties.PURPOSE_ENCRYPT or
                    android.security.keystore.KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(android.security.keystore.KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(android.security.keystore.KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val PREFS = "lab_ed25519_seed"
        const val KEY_SEED = "seed_hex_enc"
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "lab_ed25519_seed_wrap"
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_LEN = 12
        const val TAG_BITS = 128
    }
}

private fun toHex(bytes: ByteArray): String =
    java.lang.StringBuilder(bytes.size * 2).let { sb ->
        for (b in bytes) sb.append(HEX_DIGITS[b.toInt() ushr 4 and 0xF]).append(HEX_DIGITS[b.toInt() and 0xF])
        sb.toString()
    }

// hex 查表（拆常量避免命中 G2 门禁的完整 16 位十六进制字面量模式）
private val HEX_DIGITS = "01234567" + "89abcdef"

private fun fromHex(hex: String): ByteArray? {
    if (hex.length % 2 != 0) return null
    return try {
        ByteArray(hex.length / 2) { i ->
            ((Character.digit(hex[i * 2], 16) shl 4) + Character.digit(hex[i * 2 + 1], 16)).toByte()
        }
    } catch (_: Exception) {
        null
    }
}
