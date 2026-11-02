/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.android

import android.content.Context
import android.content.SharedPreferences
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import cn.worktool.open.core.protocol.DeviceCredentialStore
import cn.worktool.open.core.protocol.DeviceCredentials
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * deviceToken 端侧加密存储（protocol/spec.md §5.1.3，SR-05/G5）：
 * - AES-256-GCM 密钥由 AndroidKeystore 持有（不可导出）；
 * - 凭据 JSON 以密文存于应用私有 SharedPreferences（getEncoded 密钥永不落盘）；
 * - 明文仅在内存中短暂存在（load 后即返回 [DeviceCredentials]）。
 *
 * 设备侧行为验证 = T2.5 真机联调（deferred：JVM 单测无法覆盖 Keystore 硬件路径，
 * InMemoryDeviceCredentialStore 承担语义测试）。
 */
class AndroidKeystoreCredentialStore(
    context: Context,
    prefsName: String = "worktool_protocol_creds",
) : DeviceCredentialStore {

    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(prefsName, Context.MODE_PRIVATE)

    override fun save(credentials: DeviceCredentials) {
        val plain = credentials.deviceTokenHex.toByteArray(Charsets.UTF_8)
        val cipher = Cipher.getInstance(TRANSFORM).apply {
            init(Cipher.ENCRYPT_MODE, key())
        }
        val blob = cipher.iv + cipher.doFinal(plain)
        prefs.edit()
            .putString(K_DEVICE_TOKEN, toHex(blob))
            .putString(K_DEVICE_ID, credentials.deviceId)
            .putInt(K_TOKEN_VERSION, credentials.tokenVersion)
            .putString(K_SESSION_ID, credentials.sessionId)
            .putString(K_SERVER_X25519, credentials.serverX25519PublicKeyHex)
            .putString(K_WS_PATH, credentials.wsPath)
            .apply()
    }

    override fun load(): DeviceCredentials? {
        val tokenHexBlob = prefs.getString(K_DEVICE_TOKEN, null) ?: return null
        val deviceId = prefs.getString(K_DEVICE_ID, null) ?: return null
        val tokenVersion = prefs.getInt(K_TOKEN_VERSION, -1)
        if (tokenVersion < 1) return null
        return try {
            val blob = fromHex(tokenHexBlob)
            val iv = blob.copyOfRange(0, IV_LEN)
            val ct = blob.copyOfRange(IV_LEN, blob.size)
            val cipher = Cipher.getInstance(TRANSFORM).apply {
                init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, iv))
            }
            val tokenHex = String(cipher.doFinal(ct), Charsets.UTF_8)
            DeviceCredentials(
                deviceId = deviceId,
                deviceTokenHex = tokenHex,
                tokenVersion = tokenVersion,
                sessionId = prefs.getString(K_SESSION_ID, null),
                serverX25519PublicKeyHex = prefs.getString(K_SERVER_X25519, null),
                rotationIntervalDays = null,
                heartbeatIntervalSeconds = 15,
                sessionTimeoutSeconds = 45,
                wsPath = prefs.getString(K_WS_PATH, null),
            )
        } catch (_: Exception) {
            // 解密失败（密钥轮换/数据损坏）= 无有效凭据 → 上层走重新配对/重协商
            null
        }
    }

    override fun clear() {
        prefs.edit().clear().apply()
    }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (ks.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return generator.generateKey()
    }

    private fun toHex(bytes: ByteArray): String =
        bytes.joinToString("") { "%02x".format(it) }

    private fun fromHex(hex: String): ByteArray {
        val out = ByteArray(hex.length / 2)
        for (i in out.indices) {
            out[i] = hex.substring(2 * i, 2 * i + 2).toInt(16).toByte()
        }
        return out
    }

    companion object {
        private const val ANDROID_KEYSTORE = "AndroidKeyStore"
        private const val KEY_ALIAS = "wt_protocol_credential_key"
        private const val TRANSFORM = "AES/GCM/NoPadding"
        private const val IV_LEN = 12
        private const val TAG_BITS = 128

        private const val K_DEVICE_TOKEN = "device_token_blob"
        private const val K_DEVICE_ID = "device_id"
        private const val K_TOKEN_VERSION = "token_version"
        private const val K_SESSION_ID = "session_id"
        private const val K_SERVER_X25519 = "server_x25519"
        private const val K_WS_PATH = "ws_path"
    }
}
