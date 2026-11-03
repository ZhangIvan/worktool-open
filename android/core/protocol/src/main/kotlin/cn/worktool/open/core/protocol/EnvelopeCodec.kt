/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.longOrNull
import java.time.LocalDateTime
import java.time.format.DateTimeFormatter
import java.util.UUID

/** 编解码失败（= v2 校验顺序第 1 步「格式校验」失败，关闭码 4400）。 */
class EnvelopeDecodeException(message: String) : Exception(message)

/**
 * 信封编解码器（protocol/spec.md §4/§5.2.1）。
 *
 * - v1 兼容信封（WeworkMessageListBean，meta 为字符串）与 v2 帧信封（meta 为对象）
 *   由 `meta` 字段形态判别（spec §2：meta 为对象 = v2 帧判别条件）；
 * - v1 裸心跳 `{"type":11}` 与建连首帧 `{"td":<ms>}` 为独立形态（spec §3/§4.2）；
 * - messageId 生成格式 `yyyy-MM-dd#HH:mm:ss#<UUID>`（上游还原，§4.1）；
 * - 本类只做「结构」编解码；格式约束（pattern/enum/必含）见 validation/Validators.kt。
 */
class EnvelopeCodec(
    internal val json: Json = Json { encodeDefaults = false; explicitNulls = false; ignoreUnknownKeys = false },
) {

    private val beanListSerializer = kotlinx.serialization.builtins.ListSerializer(WeworkMessageBeanSerializer)

    // ---- 解码 ----

    fun decode(text: String): WireMessage {
        val root = try {
            json.parseToJsonElement(text)
        } catch (e: Exception) {
            throw EnvelopeDecodeException("非法 JSON: ${e.message}")
        }
        val obj = root as? JsonObject
            ?: throw EnvelopeDecodeException("信封必须是 JSON 对象")
        return when {
            obj.size == 1 && obj.containsKey("type") -> {
                val t = (obj["type"] as? JsonPrimitive)?.takeIf { !it.isString }?.intOrNull
                    ?: throw EnvelopeDecodeException("裸心跳 type 必须是整数")
                WireMessage.BareHeartbeat(t)
            }
            obj.size == 1 && obj.containsKey("td") -> {
                val td = (obj["td"] as? JsonPrimitive)?.takeIf { !it.isString }?.longOrNull
                    ?: throw EnvelopeDecodeException("td 建连帧必须是 Unix 毫秒整数")
                WireMessage.ConnectionHello(td)
            }
            // 独立控制帧（spec §5.7.1 T3.4 冻结）：type 为字符串 + meta 对象
            //（hello/hello_ack；不承载于信封，无 socketType/messageId）。
            isControlShape(obj) -> decodeControl(obj)
            else -> decodeEnvelope(obj)
        }
    }

    /** 独立控制帧判别：顶层 type 为字符串且 meta 为对象（spec §5.7.1）。 */
    private fun isControlShape(obj: JsonObject): Boolean {
        val t = obj["type"] as? JsonPrimitive ?: return false
        return t.isString && obj["meta"] is JsonObject
    }

    private fun decodeControl(obj: JsonObject): WireMessage {
        val type = (obj["type"] as JsonPrimitive).content
        val metaEl = obj["meta"] as JsonObject
        val fields = obj.entries.filter { it.key != "type" && it.key != "meta" }
            .associate { it.key to it.value }
        return WireMessage.Control(
            ControlFrame(
                type = type,
                meta = decodeMeta(metaEl),
                fields = fields,
            ),
        )
    }

    private fun decodeEnvelope(obj: JsonObject): WireMessage {
        val socketType = obj.intOrNull("socketType")
            ?: throw EnvelopeDecodeException("信封缺少 socketType")
        val listPresent = obj.containsKey("list") && obj["list"] !is JsonNull
        val extrasOf = { keys: Set<String> ->
            obj.entries.filter { it.key !in keys }.associate { it.key to it.value }
        }
        val metaEl = obj["meta"]
        return when (metaEl) {
            null, is JsonNull -> WireMessage.V1(
                V1Envelope(
                    socketType = socketType,
                    messageId = obj.stringOrNull("messageId"),
                    meta = null,
                    apiSend = obj.intOrNull("apiSend"),
                    list = decodeList(obj["list"]),
                    listPresent = listPresent,
                    encryptedList = obj.stringOrNull("encryptedList"),
                    encryptType = obj.intOrNull("encryptType"),
                    extras = extrasOf(V1_KEYS),
                ),
            )
            is JsonObject -> WireMessage.V2(
                V2Frame(
                    socketType = socketType,
                    messageId = obj.stringOrNull("messageId")
                        ?: throw EnvelopeDecodeException("v2 帧缺少 messageId"),
                    meta = decodeMeta(metaEl),
                    apiSend = obj.intOrNull("apiSend"),
                    list = decodeList(obj["list"]),
                    listPresent = listPresent,
                    encryptedList = obj.stringOrNull("encryptedList"),
                    encryptType = obj.intOrNull("encryptType") ?: 0,
                    extras = extrasOf(V2_KEYS),
                ),
            )
            is JsonPrimitive ->
                if (metaEl.isString) {
                    WireMessage.V1(
                        V1Envelope(
                            socketType = socketType,
                            messageId = obj.stringOrNull("messageId"),
                            meta = metaEl.content,
                            apiSend = obj.intOrNull("apiSend"),
                            list = decodeList(obj["list"]),
                            listPresent = listPresent,
                            encryptedList = obj.stringOrNull("encryptedList"),
                            encryptType = obj.intOrNull("encryptType"),
                            extras = extrasOf(V1_KEYS),
                        ),
                    )
                } else {
                    throw EnvelopeDecodeException("v1 meta 必须是字符串（v2 meta 为对象）")
                }
            else -> throw EnvelopeDecodeException("meta 形态非法")
        }
    }

    private fun decodeMeta(metaObj: JsonObject): FrameMeta {
        val unknown = metaObj.keys.filter { it !in META_KEYS }
        if (unknown.isNotEmpty()) {
            throw EnvelopeDecodeException("v2 meta 含未知字段（additionalProperties:false）: ${unknown.joinToString(",")}")
        }
        val missing = META_KEYS.filter { metaObj[it] == null }
        if (missing.isNotEmpty()) {
            throw EnvelopeDecodeException("v2 meta 缺少必含字段: ${missing.joinToString(",")}")
        }
        val ver = metaObj.intOrNull("ver") ?: throw EnvelopeDecodeException("meta.ver 必须是整数")
        val ts = metaObj.longOrNull("ts") ?: throw EnvelopeDecodeException("meta.ts 必须是整数")
        val seq = metaObj.longOrNull("seq") ?: throw EnvelopeDecodeException("meta.seq 必须是整数")
        val tokenVersion = metaObj.intOrNull("tokenVersion")
            ?: throw EnvelopeDecodeException("meta.tokenVersion 必须是整数")
        return FrameMeta(
            ver = ver,
            ts = ts,
            nonce = metaObj.stringOrNull("nonce") ?: throw EnvelopeDecodeException("meta.nonce 必须是字符串"),
            hmac = metaObj.stringOrNull("hmac") ?: throw EnvelopeDecodeException("meta.hmac 必须是字符串"),
            seq = seq,
            eventId = metaObj.stringOrNull("eventId") ?: throw EnvelopeDecodeException("meta.eventId 必须是字符串"),
            commandId = metaObj.stringOrNull("commandId") ?: throw EnvelopeDecodeException("meta.commandId 必须是字符串"),
            tokenVersion = tokenVersion,
        )
    }

    private fun decodeList(listEl: JsonElement?): List<WeworkMessageBean> {
        if (listEl == null || listEl is JsonNull) return emptyList()
        val arr = listEl as? JsonArray ?: throw EnvelopeDecodeException("list 必须是数组")
        return arr.map { el ->
            try {
                json.decodeFromJsonElement(WeworkMessageBeanSerializer, el.jsonObject)
            } catch (e: EnvelopeDecodeException) {
                throw e
            } catch (e: Exception) {
                throw EnvelopeDecodeException("list 元素解码失败: ${e.message}")
            }
        }
    }

    // ---- 编码 ----

    fun encodeV1(envelope: V1Envelope): String = json.encodeToString(
        JsonElement.serializer(),
        v1Element(envelope),
    )

    fun encodeV2(frame: V2Frame): String = json.encodeToString(
        JsonElement.serializer(),
        v2Element(frame),
    )

    /** 供摘要计算/测试取树形结构（JCS 与键序无关）。 */
    fun v1Element(envelope: V1Envelope): JsonObject {
        val out = LinkedHashMap<String, JsonElement>()
        out["socketType"] = JsonPrimitive(envelope.socketType)
        envelope.messageId?.let { out["messageId"] = JsonPrimitive(it) }
        out["meta"] = JsonPrimitive(envelope.meta ?: "")
        envelope.apiSend?.let { out["apiSend"] = JsonPrimitive(it) }
        if (envelope.listPresent || envelope.list.isNotEmpty() || envelope.encryptedList == null) {
            out["list"] = listElement(envelope.list)
        }
        envelope.encryptedList?.let { out["encryptedList"] = JsonPrimitive(it) }
        envelope.encryptType?.let { out["encryptType"] = JsonPrimitive(it) }
        envelope.extras.forEach { (k, v) -> if (k !in V1_KEYS) out[k] = v }
        return JsonObject(out)
    }

    fun v2Element(frame: V2Frame): JsonObject {
        val out = LinkedHashMap<String, JsonElement>()
        out["socketType"] = JsonPrimitive(frame.socketType)
        out["messageId"] = JsonPrimitive(frame.messageId)
        out["meta"] = metaElement(frame.meta)
        frame.apiSend?.let { out["apiSend"] = JsonPrimitive(it) }
        if (frame.listPresent || frame.list.isNotEmpty() || frame.encryptedList == null) {
            out["list"] = listElement(frame.list)
        }
        frame.encryptedList?.let { out["encryptedList"] = JsonPrimitive(it) }
        out["encryptType"] = JsonPrimitive(frame.encryptType)
        frame.extras.forEach { (k, v) -> if (k !in V2_KEYS) out[k] = v }
        return JsonObject(out)
    }

    /** frameMeta 树形（frameMeta 字段序冻结；独立控制帧校验复用）。 */
    fun metaElement(meta: FrameMeta): JsonObject = JsonObject(
        linkedMapOf(
            "ver" to JsonPrimitive(meta.ver),
            "ts" to JsonPrimitive(meta.ts),
            "nonce" to JsonPrimitive(meta.nonce),
            "hmac" to JsonPrimitive(meta.hmac),
            "seq" to JsonPrimitive(meta.seq),
            "eventId" to JsonPrimitive(meta.eventId),
            "commandId" to JsonPrimitive(meta.commandId),
            "tokenVersion" to JsonPrimitive(meta.tokenVersion),
        ),
    )

    fun listElement(list: List<WeworkMessageBean>): JsonArray =
        json.encodeToJsonElement(beanListSerializer, list) as JsonArray

    /**
     * 解析已验帧载荷树（list JsonArray）为载荷单元列表（T3.4 执行回路接线消费入口：
     * onCommand 的 payload JsonElement → 强类型 bean；含 901~904 控制载荷——
     * 控制字段在 extras 保留，约束校验见 validation/ControlPayloadValidator）。
     */
    fun beans(payload: JsonElement): List<WeworkMessageBean> = decodeList(payload)

    // ---- 构造器 ----

    /** messageId：`yyyy-MM-dd#HH:mm:ss#<UUID>`（上游还原：日期时间之间的空格替换为 `#`）。 */
    fun newMessageId(now: LocalDateTime = LocalDateTime.now()): String =
        MESSAGE_ID_FORMAT.format(now) + "#" + UUID.randomUUID().toString()

    /** v1 建连首帧（onOpen 立即发送，spec §3）。 */
    fun connectionHelloJson(tdMillis: Long): String = """{"td":$tdMillis}"""

    /** v1 裸心跳 `{"type":11}`（spec §4.2）。 */
    fun bareHeartbeatJson(): String = """{"type":${MessageTypes.HEART_BEAT}}"""

    companion object {
        // '#' 在 DateTimeFormatter pattern 中是保留字，须以字面量引号包裹
        private val MESSAGE_ID_FORMAT: DateTimeFormatter =
            DateTimeFormatter.ofPattern("yyyy-MM-dd'#'HH:mm:ss")

        private val META_KEYS = setOf(
            "ver", "ts", "nonce", "hmac", "seq", "eventId", "commandId", "tokenVersion",
        )

        private val V1_KEYS = setOf(
            "socketType", "messageId", "meta", "apiSend", "list", "encryptedList", "encryptType",
        )

        private val V2_KEYS = V1_KEYS
    }
}
