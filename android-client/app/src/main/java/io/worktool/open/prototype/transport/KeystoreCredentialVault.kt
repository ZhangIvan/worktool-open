package io.worktool.open.prototype.transport

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.AtomicFile
import io.worktool.open.prototype.core.CredentialVault
import io.worktool.open.prototype.core.Identity
import org.json.JSONObject
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** No plaintext preferences, logging or backup-eligible credential file. Key loss requires pairing again. */
class KeystoreCredentialVault(context: Context) : CredentialVault {
    private val file = AtomicFile(File(context.noBackupFilesDir, "device-identity.aesgcm"))
    private val alias = "worktool.prototype.identity.v1"
    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true).setKeySize(256).build())
        }.generateKey()
    }
    @Synchronized override fun save(identity: Identity) {
        val plain = JSONObject().put("endpoint", identity.endpoint).put("deviceId", identity.deviceId)
            .put("keyId", identity.keyId).put("secret", identity.secret).toString().toByteArray(Charsets.UTF_8)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()); updateAAD(alias.toByteArray()) }
        val encrypted = cipher.doFinal(plain); plain.fill(0)
        val out = file.startWrite()
        try { out.write(byteArrayOf(1)); out.write(cipher.iv); out.write(encrypted); file.finishWrite(out) }
        catch (e: Exception) { file.failWrite(out); throw IllegalStateException("CREDENTIAL_WRITE_FAILED") }
    }
    @Synchronized override fun load(): Identity? {
        if (!file.baseFile.exists()) return null
        try {
            val data = file.readFully(); require(data.size in 30..8192 && data[0] == 1.toByte())
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply {
                init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, data.copyOfRange(1,13))); updateAAD(alias.toByteArray())
            }
            val plain = cipher.doFinal(data.copyOfRange(13,data.size))
            val j = JSONObject(String(plain, Charsets.UTF_8)); plain.fill(0)
            return Identity(j.getString("endpoint"),j.getString("deviceId"),j.getString("keyId"),j.getString("secret"))
        } catch (_: Exception) { throw IllegalStateException("CREDENTIAL_UNAVAILABLE_REPAIR_REQUIRED") }
    }
    @Synchronized override fun clear() { file.delete(); KeyStore.getInstance("AndroidKeyStore").apply { load(null); deleteEntry(alias) } }
}
