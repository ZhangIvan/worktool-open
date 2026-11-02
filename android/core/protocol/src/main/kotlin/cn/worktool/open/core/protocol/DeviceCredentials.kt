/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.crypto.Hex

/**
 * 设备凭据（配对响应持久化形态，spec §5.1.3）。
 * deviceToken 端侧加密存储（SR-05/G5：AndroidKeystore 实现在 core:protocol-android）；
 * 服务端仅存 SHA-256 hash。逐帧 HMAC 密钥 = deviceToken 原始字节。
 */
data class DeviceCredentials(
    val deviceId: String,
    val deviceTokenHex: String,
    val tokenVersion: Int,
    val sessionId: String? = null,
    val serverX25519PublicKeyHex: String? = null,
    val rotationIntervalDays: Int? = null,
    val heartbeatIntervalSeconds: Int = 15,
    val sessionTimeoutSeconds: Int = 45,
    val wsPath: String? = null,
) {
    val deviceToken: ByteArray get() = Hex.decode(deviceTokenHex)
}

/** 凭据存取接口（生产：Keystore 加密 + 私有存储；测试：内存）。 */
interface DeviceCredentialStore {
    fun save(credentials: DeviceCredentials)
    fun load(): DeviceCredentials?
    fun clear()
}

class InMemoryDeviceCredentialStore : DeviceCredentialStore {
    private var cached: DeviceCredentials? = null
    override fun save(credentials: DeviceCredentials) {
        cached = credentials
    }
    override fun load(): DeviceCredentials? = cached
    override fun clear() {
        cached = null
    }
}
