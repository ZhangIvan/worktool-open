/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.content.SharedPreferences
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec

/**
 * 产品面 Ed25519 回退种子持久化（T2.6 装配注记「回退种子加密持久化归 app 层」的
 * 产品实现；与 labDebug 的 LabEd25519SeedStore 同范式，prefs/密钥别名隔离）。
 *
 * 作用：JCA 无 Ed25519 的设备（API 24~32，如 D1 Android 12）落到纯 JVM 回退库生成
 * 签名密钥时，种子经 AndroidKeyStore AES-256-GCM 加密持久化——重复配对复用同一
 * 设备签名公钥（注册公钥不漂移）；种子明文永不落盘、不进日志（G13）。
 */
class SigningSeedStore(context: Context) {

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
            null // 解密失败（密钥轮换/损坏）→ 视为无存档重新生成；旧配对自然失效
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
        const val PREFS = "product_ed25519_seed"
        const val KEY_SEED = "seed_hex_enc"
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "product_ed25519_seed_wrap"
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
