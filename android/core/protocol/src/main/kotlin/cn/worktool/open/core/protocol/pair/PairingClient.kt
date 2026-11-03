/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.pair

import cn.worktool.open.core.protocol.crypto.Ed25519
import cn.worktool.open.core.protocol.crypto.Ed25519Keys
import cn.worktool.open.core.protocol.transport.EndpointPolicy
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.security.PrivateKey
import java.security.SecureRandom
import java.util.concurrent.TimeUnit

/**
 * 配对/重协商 HTTP 客户端（OkHttp；spec §5.1/§5.5.1）。
 *
 * - `POST {base}/api/v2/pair`：一次性配对码 + Ed25519 签名公钥 + X25519 临时公钥；
 * - `POST {base}/api/v2/device-sessions`：凭据重协商（复用配对注册的签名公钥，
 *   `Ed25519 签名(deviceId|ts|nonce)`，无需新配对码）。请求体形状为临时冻结
 *   （spec §9 Q3：hello_ack/901~904 控制帧 schema 随 T3.4 冻结）。
 * - TLS 强制（SR-01/09）：默认 OkHttp 系统证书校验，无 trust-all；
 *   明文/裸 IP 由 [EndpointPolicy] 拒绝（loopback 原型例外）。
 */
class PairingClient(
    private val baseUrl: String,
    private val client: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .readTimeout(15, TimeUnit.SECONDS)
        .build(),
    private val json: Json = pairJson,
    private val random: SecureRandom = SecureRandom(),
    private val clock: () -> Long = System::currentTimeMillis,
) {

    init {
        val check = EndpointPolicy.check(baseUrl)
        if (check is EndpointPolicy.Check.Violated) {
            throw IllegalStateException("配对端点被安全策略拒绝: ${check.rule}（${check.detail}）")
        }
    }

    /** 发起配对。403 = 配对码无效/过期/已用（PAIRING_CODE_INVALID）；400 = 公钥格式非法。 */
    fun pair(request: PairRequest): PairResponse {
        PairSchemas.validateRequest(request).takeIf { it.isNotEmpty() }?.let {
            throw PairingException("本地配对请求校验失败: ${it.joinToString("; ")}")
        }
        val body = json.encodeToString(JsonElement.serializer(), request.toJsonElement())
        val element = execute("$baseUrl/api/v2/pair", body)
        val violations = PairSchemas.validateResponse(element)
        if (violations.isNotEmpty()) {
            throw PairingException("配对响应校验失败: ${violations.joinToString("; ")}")
        }
        return PairResponse.fromJsonElement(element)
    }

    /**
     * 凭据重协商（离线错过轮换后 hello 4404 的恢复路径，spec §5.5.1）。
     * 签名串 = `deviceId|ts|nonce`（Ed25519，64 字节 hex）。
     *
     * JCA 路径重载（API 33+ / 宿主 JVM）；API 24~32 设备用
     * [renegotiate(deviceId, signingKey: Ed25519Keys.SigningKey)][renegotiate]
     * 双路径重载（T2.6）。
     */
    fun renegotiate(deviceId: String, signingKey: PrivateKey): PairResponse {
        val ts = clock()
        val nonce = newNonce()
        val signature = Ed25519.sign(signingKey, "$deviceId|$ts|$nonce".toByteArray(Charsets.UTF_8))
        return postRenegotiate(deviceId, ts, nonce, signature)
    }

    /**
     * 凭据重协商·双路径重载（T2.6）：签名实现随 [Ed25519Keys.SigningKey.origin]
     * 路由（设备 JCA / 纯 JVM 回退），两路径行为等价（RFC 8032 官方向量 + 交叉
     * 验签互认，`Ed25519T26DualPathTest`）。EXTERNAL_PUBLIC_ONLY（lab 裁决 B
     * 注入公钥）不可签名 → [require] 响亮失败（该场景重协商 NOT_RUN）。
     */
    fun renegotiate(deviceId: String, signingKey: Ed25519Keys.SigningKey): PairResponse {
        require(signingKey.signCapable) { "签名密钥不可签名（origin=${signingKey.origin}）" }
        val ts = clock()
        val nonce = newNonce()
        val signature = Ed25519Keys.sign(signingKey, "$deviceId|$ts|$nonce".toByteArray(Charsets.UTF_8))
        return postRenegotiate(deviceId, ts, nonce, signature)
    }

    private fun newNonce(): String {
        val nonceBytes = ByteArray(16).also { random.nextBytes(it) }
        return cn.worktool.open.core.protocol.crypto.Hex.encode(nonceBytes)
    }

    private fun postRenegotiate(deviceId: String, ts: Long, nonce: String, signature: ByteArray): PairResponse {
        val body = buildJsonObject {
            put("deviceId", deviceId)
            put("ts", ts)
            put("nonce", nonce)
            put("ed25519Signature", cn.worktool.open.core.protocol.crypto.Hex.encode(signature))
        }
        val element = execute(
            "$baseUrl/api/v2/device-sessions",
            json.encodeToString(JsonElement.serializer(), body),
        )
        val violations = PairSchemas.validateResponse(element)
        if (violations.isNotEmpty()) {
            throw PairingException("重协商响应校验失败: ${violations.joinToString("; ")}")
        }
        return PairResponse.fromJsonElement(element)
    }

    private fun execute(url: String, bodyJson: String): JsonObject {
        val request = Request.Builder()
            .url(url)
            .post(bodyJson.toRequestBody("application/json".toMediaType()))
            .build()
        client.newCall(request).execute().use { response ->
            when {
                response.isSuccessful -> {
                    val text = response.body?.string().orEmpty()
                    if (text.isBlank()) throw PairingException("空响应体", httpCode = response.code)
                    return try {
                        Json.parseToJsonElement(text) as? JsonObject
                            ?: throw PairingException("响应必须是 JSON 对象", httpCode = response.code)
                    } catch (e: PairingException) {
                        throw e
                    } catch (e: Exception) {
                        throw PairingException("响应不是合法 JSON: ${e.message}", httpCode = response.code)
                    }
                }
                response.code == 403 -> throw PairingException(
                    "PAIRING_CODE_INVALID（配对码无效/过期/已用）",
                    httpCode = 403, errorCode = "PAIRING_CODE_INVALID",
                )
                response.code == 400 -> throw PairingException(
                    "BAD_REQUEST（公钥格式非法等）", httpCode = 400, errorCode = "BAD_REQUEST",
                )
                else -> throw PairingException("HTTP ${response.code}", httpCode = response.code)
            }
        }
    }
}
