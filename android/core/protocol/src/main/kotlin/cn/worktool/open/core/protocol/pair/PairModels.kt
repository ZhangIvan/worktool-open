/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.pair

import cn.worktool.open.core.protocol.EnvelopeDecodeException
import cn.worktool.open.core.protocol.validation.PairRequestValidator
import cn.worktool.open.core.protocol.validation.PairResponseValidator
import cn.worktool.open.core.protocol.validation.SchemaViolation
import cn.worktool.open.core.protocol.validation.intOf
import cn.worktool.open.core.protocol.validation.longOf
import cn.worktool.open.core.protocol.validation.stringOf
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** 配对请求（pair-request.schema.json，spec §5.1.2）。 */
data class PairRequest(
    val pairingCode: String,
    val deviceId: String,
    val deviceName: String? = null,
    /** Ed25519 签名公钥（32 字节小写 hex；与临时公钥分列两字段，docs/02 §5 冻结）。 */
    val ed25519PublicKeyHex: String,
    /** X25519 临时公钥（32 字节小写 hex；每次配对/重协商新生成）。 */
    val x25519EphemeralPublicKeyHex: String,
) {
    fun toJsonElement(): JsonObject = buildJsonObject {
        put("pairingCode", pairingCode)
        put("deviceId", deviceId)
        deviceName?.let { put("deviceName", it) }
        put("ed25519PublicKey", ed25519PublicKeyHex)
        put("x25519EphemeralPublicKey", x25519EphemeralPublicKeyHex)
    }

    companion object {
        fun fromJsonElement(element: JsonElement): PairRequest {
            val obj = element as? JsonObject
                ?: throw EnvelopeDecodeException("配对请求必须是 JSON 对象")
            return PairRequest(
                pairingCode = obj.stringOf("pairingCode")
                    ?: throw EnvelopeDecodeException("pairingCode 必含"),
                deviceId = obj.stringOf("deviceId") ?: throw EnvelopeDecodeException("deviceId 必含"),
                deviceName = obj.stringOf("deviceName"),
                ed25519PublicKeyHex = obj.stringOf("ed25519PublicKey")
                    ?: throw EnvelopeDecodeException("ed25519PublicKey 必含"),
                x25519EphemeralPublicKeyHex = obj.stringOf("x25519EphemeralPublicKey")
                    ?: throw EnvelopeDecodeException("x25519EphemeralPublicKey 必含"),
            )
        }
    }
}

/** 配对响应（pair-response.schema.json，spec §5.1.3）。 */
data class PairResponse(
    val deviceId: String,
    val sessionId: String,
    val deviceTokenHex: String,
    val tokenVersion: Int,
    val serverX25519PublicKeyHex: String,
    val heartbeatIntervalSeconds: Int = 15,
    val sessionTimeoutSeconds: Int = 45,
    val rotationIntervalDays: Int = 30,
    val issuedAt: Long,
    val wsPath: String,
) {
    companion object {
        fun fromJsonElement(element: JsonElement): PairResponse {
            val obj = element as? JsonObject
                ?: throw EnvelopeDecodeException("配对响应必须是 JSON 对象")
            fun req(name: String): String =
                obj.stringOf(name) ?: throw EnvelopeDecodeException("配对响应缺少 $name")
            return PairResponse(
                deviceId = req("deviceId"),
                sessionId = req("sessionId"),
                deviceTokenHex = req("deviceToken"),
                tokenVersion = obj.intOf("tokenVersion")
                    ?: throw EnvelopeDecodeException("tokenVersion 必含"),
                serverX25519PublicKeyHex = req("serverX25519PublicKey"),
                heartbeatIntervalSeconds = obj.intOf("heartbeatIntervalSeconds") ?: 15,
                sessionTimeoutSeconds = obj.intOf("sessionTimeoutSeconds") ?: 45,
                rotationIntervalDays = obj.intOf("rotationIntervalDays") ?: 30,
                issuedAt = obj.longOf("issuedAt") ?: throw EnvelopeDecodeException("issuedAt 必含"),
                wsPath = req("wsPath"),
            )
        }
    }
}

/** 校验入口（夹具正反例复用）。 */
object PairSchemas {
    fun validateRequest(request: PairRequest): List<SchemaViolation> =
        PairRequestValidator.validate(request.toJsonElement())

    fun validateResponse(responseJson: JsonElement): List<SchemaViolation> =
        PairResponseValidator.validate(responseJson)
}

/** 配对/会话 HTTP 异常（含服务端错误语义，spec §5.1.2 拒绝条件）。 */
class PairingException(
    message: String,
    val httpCode: Int? = null,
    val errorCode: String? = null,
) : Exception(message)

internal val pairJson = Json { encodeDefaults = false; ignoreUnknownKeys = false }
