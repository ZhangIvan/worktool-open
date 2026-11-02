/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import kotlinx.serialization.KSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.descriptors.SerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull

/**
 * v1 兼容信封数据模型（WeworkMessageListBean 全字段，protocol/spec.md §4）。
 *
 * 字段保真策略：
 * - 已还原字段（spec §4.4「已还原」行）全部类型化；
 * - 待冻结字段（候选 `targetRoom`/`receivedContent` 等，spec §4.4）与上游任意增量字段
 *   保留在 [WeworkMessageBean.extras]（Map<String, JsonElement>），解码→编码往返不丢失；
 * - 子结构（Friend/SubMessageBean/ItemMessageBean）字段集已全部还原，未知键忽略
 *   （schema additionalProperties:true 的容错面）。
 */
internal val subBeanJson = Json {
    ignoreUnknownKeys = true
    encodeDefaults = false
    explicitNulls = false
}

/** Friend 子结构（spec §4.3）。 */
@Serializable
data class Friend(
    @SerialName("phone") val phone: String? = null,
    @SerialName("name") val name: String? = null,
    @SerialName("markName") val markName: String? = null,
    @SerialName("markCorp") val markCorp: String? = null,
    @SerialName("markExtra") val markExtra: String? = null,
    @SerialName("tagList") val tagList: List<String>? = null,
    @SerialName("newFriend") val newFriend: Boolean? = null,
    @SerialName("leavingMsg") val leavingMsg: String? = null,
)

/** ItemMessageBean 子结构（spec §4.3）。 */
@Serializable
data class ItemMessageBean(
    @SerialName("feature") val feature: Int? = null,
    @SerialName("text") val text: String? = null,
)

/** SubMessageBean 子结构（spec §4.3）。 */
@Serializable
data class SubMessageBean(
    @SerialName("sender") val sender: Int? = null,
    @SerialName("textType") val textType: Int? = null,
    @SerialName("itemMessageList") val itemMessageList: List<ItemMessageBean>? = null,
    @SerialName("nameList") val nameList: List<String>? = null,
    @SerialName("imageRepeat") val imageRepeat: Boolean? = null,
    @SerialName("imageSize") val imageSize: Long? = null,
    @SerialName("image") val image: String? = null,
)

/**
 * WeworkMessageBean：v1/v2 共用载荷单元（v2 lite 定义并入 ExecCallbackBean 字段族，
 * spec §4.3/§5.4）。`type` 必含；其余字段可选；未识别键保留在 [extras]。
 */
data class WeworkMessageBean(
    val type: Int,
    val roomType: Int? = null,
    val textType: Int? = null,
    val at: List<String>? = null,
    val atList: List<String>? = null,
    val extraText: String? = null,
    val fileUrl: String? = null,
    val fileBase64: String? = null,
    val fileType: String? = null,
    val friend: Friend? = null,
    val subMessageList: List<SubMessageBean>? = null,
    val source: String? = null,
    // socketType=3 结果回执字段族（ExecCallbackBean，spec §4.3）
    val errorCode: Int? = null,
    val errorReason: String? = null,
    val runTime: String? = null,
    val timeCost: Long? = null,
    val successList: List<String>? = null,
    val failList: List<String>? = null,
    val metaJson: String? = null,
    val rawMsg: String? = null,
    // 待冻结/上游增量字段（spec §4.4）：候选 targetRoom/receivedContent 等
    val extras: Map<String, JsonElement> = emptyMap(),
) {
    /** 取待冻结候选字段（存在则透传，互通依据以 T3.2 冻结为准）。 */
    fun extraString(name: String): String? =
        (extras[name] as? JsonPrimitive)?.takeIf { it.isString }?.content
}

/** WeworkMessageBean 手写序列化器：已知字段类型化 + 未知字段全量保真。 */
object WeworkMessageBeanSerializer : KSerializer<WeworkMessageBean> {

    private val delegate = JsonElement.serializer()

    override val descriptor: SerialDescriptor = delegate.descriptor

    override fun deserialize(decoder: Decoder): WeworkMessageBean {
        val obj = decoder.decodeSerializableValue(delegate).jsonObject
        val type = obj.intOrNull("type")
            ?: throw kotlinx.serialization.SerializationException("WeworkMessageBean.type 必含且为整数")
        val extras = obj.entries.filter { it.key !in KNOWN_KEYS }.associate { it.key to it.value }
        return WeworkMessageBean(
            type = type,
            roomType = obj.intOrNull("roomType"),
            textType = obj.intOrNull("textType"),
            at = obj.stringListOrNull("at"),
            atList = obj.stringListOrNull("atList"),
            extraText = obj.stringOrNull("extraText"),
            fileUrl = obj.stringOrNull("fileUrl"),
            fileBase64 = obj.stringOrNull("fileBase64"),
            fileType = obj.stringOrNull("fileType"),
            friend = obj["friend"]?.takeIf { it !is JsonNull }?.let { subBeanJson.decodeFromJsonElement<Friend>(it) },
            subMessageList = obj["subMessageList"]?.takeIf { it !is JsonNull }?.let {
                subBeanJson.decodeFromJsonElement<List<SubMessageBean>>(it)
            },
            source = obj.stringOrNull("source"),
            errorCode = obj.intOrNull("errorCode"),
            errorReason = obj.stringOrNull("errorReason"),
            runTime = obj.stringOrNull("runTime"),
            timeCost = obj.longOrNull("timeCost"),
            successList = obj.stringListOrNull("successList"),
            failList = obj.stringListOrNull("failList"),
            metaJson = obj.stringOrNull("metaJson"),
            rawMsg = obj.stringOrNull("rawMsg"),
            extras = extras,
        )
    }

    override fun serialize(encoder: Encoder, value: WeworkMessageBean) {
        val known = buildJsonObject {
            put("type", JsonPrimitive(value.type))
            value.roomType?.let { put("roomType", JsonPrimitive(it)) }
            value.textType?.let { put("textType", JsonPrimitive(it)) }
            value.at?.let { put("at", JsonArray(it.map { n -> JsonPrimitive(n) })) }
            value.atList?.let { put("atList", JsonArray(it.map { n -> JsonPrimitive(n) })) }
            value.extraText?.let { put("extraText", JsonPrimitive(it)) }
            value.fileUrl?.let { put("fileUrl", JsonPrimitive(it)) }
            value.fileBase64?.let { put("fileBase64", JsonPrimitive(it)) }
            value.fileType?.let { put("fileType", JsonPrimitive(it)) }
            value.friend?.let { put("friend", subBeanJson.encodeToJsonElement(it)) }
            value.subMessageList?.let { put("subMessageList", subBeanJson.encodeToJsonElement(it)) }
            value.source?.let { put("source", JsonPrimitive(it)) }
            value.errorCode?.let { put("errorCode", JsonPrimitive(it)) }
            value.errorReason?.let { put("errorReason", JsonPrimitive(it)) }
            value.runTime?.let { put("runTime", JsonPrimitive(it)) }
            value.timeCost?.let { put("timeCost", JsonPrimitive(it)) }
            value.successList?.let { put("successList", JsonArray(it.map { n -> JsonPrimitive(n) })) }
            value.failList?.let { put("failList", JsonArray(it.map { n -> JsonPrimitive(n) })) }
            value.metaJson?.let { put("metaJson", JsonPrimitive(it)) }
            value.rawMsg?.let { put("rawMsg", JsonPrimitive(it)) }
        }
        val merged = LinkedHashMap<String, JsonElement>(known)
        value.extras.forEach { (k, v) -> if (!KNOWN_KEYS.contains(k)) merged[k] = v }
        encoder.encodeSerializableValue(delegate, JsonObject(merged))
    }

    private val KNOWN_KEYS = setOf(
        "type", "roomType", "textType", "at", "atList", "extraText", "fileUrl", "fileBase64",
        "fileType", "friend", "subMessageList", "source", "errorCode", "errorReason", "runTime",
        "timeCost", "successList", "failList", "metaJson", "rawMsg",
    )
}

/** v2 帧鉴权与投递语义 meta（protocol/spec.md §5.2.1；additionalProperties:false）。 */
data class FrameMeta(
    val ver: Int,
    val ts: Long,
    val nonce: String,
    val hmac: String,
    val seq: Long,
    val eventId: String,
    val commandId: String,
    val tokenVersion: Int,
)

/** v1 兼容信封（WeworkMessageListBean，spec §4.1）：meta 为字符串。 */
data class V1Envelope(
    val socketType: Int,
    val messageId: String? = null,
    val meta: String? = null,
    val apiSend: Int? = null,
    val list: List<WeworkMessageBean> = emptyList(),
    /** 原始报文是否显式携带 `list`（schema：encryptType=0 时 required）。 */
    val listPresent: Boolean = false,
    val encryptedList: String? = null,
    val encryptType: Int? = null,
    val extras: Map<String, JsonElement> = emptyMap(),
)

/** v2 帧信封（command-envelope.schema.json）：meta 为对象（v2 帧判别条件）。 */
data class V2Frame(
    val socketType: Int,
    val messageId: String,
    val meta: FrameMeta,
    val apiSend: Int? = null,
    val list: List<WeworkMessageBean> = emptyList(),
    /** 原始报文是否显式携带 `list`（schema：encryptType=0 时 required）。 */
    val listPresent: Boolean = false,
    val encryptedList: String? = null,
    val encryptType: Int = 0,
    val extras: Map<String, JsonElement> = emptyMap(),
)

/** 解码后的线上形态（信封外的裸心跳与建连 td 帧按独立形态建模，spec §3/§4.2）。 */
sealed interface WireMessage {
    data class V1(val envelope: V1Envelope) : WireMessage
    data class V2(val frame: V2Frame) : WireMessage

    /** v1 裸心跳 `{"type":11}`（spec §4.2：与信封形态均须接受）。 */
    data class BareHeartbeat(val type: Int = MessageTypes.HEART_BEAT) : WireMessage

    /** v1 建连首帧 `{"td":<Unix毫秒>}`（上游还原，spec §3）。 */
    data class ConnectionHello(val td: Long) : WireMessage
}

// ---- JsonElement 取值工具（类型不符返回 null，由校验器统一报格式错误）----

internal fun JsonObject.stringOrNull(name: String): String? =
    (this[name] as? JsonPrimitive)?.takeIf { it.isString }?.content

internal fun JsonObject.intOrNull(name: String): Int? =
    (this[name] as? JsonPrimitive)?.takeIf { !it.isString }?.intOrNull

internal fun JsonObject.longOrNull(name: String): Long? =
    (this[name] as? JsonPrimitive)?.takeIf { !it.isString }?.longOrNull

internal fun JsonObject.stringListOrNull(name: String): List<String>? =
    (this[name] as? JsonArray)?.map { (it as? JsonPrimitive)?.takeIf { p -> p.isString }?.content ?: return null }
