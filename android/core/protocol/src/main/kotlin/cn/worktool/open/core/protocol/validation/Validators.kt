/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.validation

import cn.worktool.open.core.protocol.FrameMeta
import cn.worktool.open.core.protocol.V1Envelope
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WeworkMessageBean
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.longOrNull

/**
 * 机器件等价校验（protocol/schema 目录全部 *.schema.json 的 Kotlin 落地；字段级规则映射见各函数 kdoc）。
 *
 * 校验结果 = 违例列表（空 = 通过）。v2 帧校验失败 = §5.2.4 校验顺序第 1 步「格式校验」
 * 失败（关闭码 4400）；正反例夹具（protocol/fixtures 目录与 fixtures/negative 目录）
 * 必须分别通过/被拒（docs/03 T2.2 DoD）。
 */
data class SchemaViolation(
    /** 违例字段路径（如 `meta.seq`）。 */
    val field: String,
    /** 规则标识（如 `pattern:uuid`、`enum:0,2,3`）。 */
    val rule: String,
    /** 实际值摘要。 */
    val actual: String,
) {
    override fun toString(): String = "$field 违反 $rule（实际: $actual）"
}

/** 共用 pattern（逐条对应 protocol/schema 目录 *.json 的 pattern/enum/约束）。 */
object Patterns {
    val MESSAGE_ID = Regex("""^[0-9]{4}-[0-9]{2}-[0-9]{2}#[0-9]{2}:[0-9]{2}:[0-9]{2}#[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$""")
    val UUID_LOWER = Regex("""^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$""")
    val COMMAND_ID = Regex("""^(|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$""")
    val NONCE = Regex("""^[0-9a-f]{32}$""")
    val HMAC = Regex("""^[0-9a-f]{64}$""")
    val HEX64_LOWER = Regex("""^[0-9a-f]{64}$""")
    val HEX_ANY = Regex("""^[0-9a-fA-F]+$""")
    val PAIRING_CODE = Regex("""^[0-9]{6}$""")
    val DEVICE_ID = Regex("""^[A-Za-z0-9._-]{8,64}$""")
    val WS_PATH = Regex("""^/api/v2/device-sessions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$""")
}

/** 载荷单元约束（wire-v1-envelope weworkMessageBean + command-envelope weworkMessageBeanLite）。 */
object BeanValidator {

    /** v1/v2 通用 list 元素约束（schema definitions）。 */
    fun violations(bean: WeworkMessageBean, prefix: String = "list"): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        if (bean.roomType != null && bean.roomType !in 0..4) {
            v += SchemaViolation("$prefix.roomType", "enum:0..4", "${bean.roomType}")
        }
        if (bean.textType != null && bean.textType !in 0..18) {
            v += SchemaViolation("$prefix.textType", "enum:0..18", "${bean.textType}")
        }
        if (bean.source != null && bean.source !in setOf("local", "server")) {
            v += SchemaViolation("$prefix.source", "enum:local,server", "${bean.source}")
        }
        if (bean.errorCode != null && bean.errorCode < 0) {
            v += SchemaViolation("$prefix.errorCode", "minimum:0", "${bean.errorCode}")
        }
        if (bean.timeCost != null && bean.timeCost < 0) {
            v += SchemaViolation("$prefix.timeCost", "minimum:0", "${bean.timeCost}")
        }
        bean.subMessageList?.forEachIndexed { i, sub ->
            val p = "$prefix.subMessageList[$i]"
            if (sub.sender != null && sub.sender !in 0..2) {
                v += SchemaViolation("$p.sender", "enum:0..2", "${sub.sender}")
            }
            if (sub.textType != null && sub.textType !in 0..18) {
                v += SchemaViolation("$p.textType", "enum:0..18", "${sub.textType}")
            }
            if (sub.imageSize != null && sub.imageSize < 0) {
                v += SchemaViolation("$p.imageSize", "minimum:0", "${sub.imageSize}")
            }
            sub.itemMessageList?.forEachIndexed { j, item ->
                if (item.feature != null && item.feature !in setOf(0, 2)) {
                    v += SchemaViolation("$p.itemMessageList[$j].feature", "enum:0,2", "${item.feature}")
                }
            }
        }
        return v
    }
}

/** v1 兼容信封校验（wire-v1-envelope.schema.json 全约束）。 */
object V1EnvelopeValidator {

    fun validate(e: V1Envelope): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        if (e.socketType !in 0..3) {
            v += SchemaViolation("socketType", "enum:0,1,2,3", "${e.socketType}")
        }
        if (e.messageId != null && !Patterns.MESSAGE_ID.matches(e.messageId)) {
            v += SchemaViolation("messageId", "pattern:yyyy-MM-dd#HH:mm:ss#uuid", e.messageId)
        }
        if (e.apiSend != null && e.apiSend !in setOf(0, 1)) {
            v += SchemaViolation("apiSend", "enum:0,1", "${e.apiSend}")
        }
        when (e.encryptType) {
            null, 0 -> if (!e.listPresent) {
                v += SchemaViolation("list", "required(encryptType=0)", "缺失")
            }
            1 -> if (e.encryptedList == null) {
                v += SchemaViolation("encryptedList", "required(encryptType=1)", "缺失")
            }
            else -> v += SchemaViolation("encryptType", "enum:0,1", "${e.encryptType}")
        }
        if (e.encryptedList != null && !Patterns.HEX_ANY.matches(e.encryptedList)) {
            v += SchemaViolation("encryptedList", "pattern:hex", e.encryptedList.take(16) + "…")
        }
        e.list.forEachIndexed { i, bean ->
            v += BeanValidator.violations(bean, "list[$i]")
        }
        return v
    }
}

/** v2 帧信封校验（command-envelope.schema.json 全约束）。 */
object V2FrameValidator {

    fun validate(f: V2Frame): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        if (f.socketType !in setOf(0, 2, 3)) {
            v += SchemaViolation("socketType", "enum:0,2,3", "${f.socketType}")
        }
        if (!Patterns.MESSAGE_ID.matches(f.messageId)) {
            v += SchemaViolation("messageId", "pattern:yyyy-MM-dd#HH:mm:ss#uuid", f.messageId)
        }
        v += metaViolations(f.meta)
        if (f.apiSend != null && f.apiSend !in setOf(0, 1)) {
            v += SchemaViolation("apiSend", "enum:0,1", "${f.apiSend}")
        }
        when (f.encryptType) {
            0 -> if (!f.listPresent) {
                v += SchemaViolation("list", "required(encryptType=0)", "缺失")
            }
            2 -> {
                if (f.encryptedList == null) {
                    v += SchemaViolation("encryptedList", "required(encryptType=2)", "缺失")
                } else if (!Patterns.HEX_ANY.matches(f.encryptedList)) {
                    v += SchemaViolation("encryptedList", "pattern:hex", f.encryptedList.take(16) + "…")
                }
                // 车道加严：encryptType=2 且并发明文 list → 拒绝
                //（fixtures/negative/command-envelope.encrypt-mismatch.json；spec §5.3 密文独占承载）
                if (f.listPresent && f.list.isNotEmpty()) {
                    v += SchemaViolation("list", "must-be-absent(encryptType=2)", "与 encryptedList 并存")
                }
            }
            else -> v += SchemaViolation("encryptType", "enum:0,2", "${f.encryptType}")
        }
        f.list.forEachIndexed { i, bean ->
            v += BeanValidator.violations(bean, "list[$i]")
        }
        return v
    }

    /** frameMeta 约束（additionalProperties:false 在解码器执行）。 */
    fun metaViolations(m: FrameMeta): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        if (m.ver != 2) v += SchemaViolation("meta.ver", "const:2", "${m.ver}")
        if (m.ts < 0) v += SchemaViolation("meta.ts", "minimum:0", "${m.ts}")
        if (m.seq < 1) v += SchemaViolation("meta.seq", "minimum:1", "${m.seq}")
        if (m.tokenVersion < 1) v += SchemaViolation("meta.tokenVersion", "minimum:1", "${m.tokenVersion}")
        if (!Patterns.NONCE.matches(m.nonce)) v += SchemaViolation("meta.nonce", "pattern:hex32", m.nonce)
        if (!Patterns.HMAC.matches(m.hmac)) v += SchemaViolation("meta.hmac", "pattern:hex64", m.hmac)
        if (!Patterns.UUID_LOWER.matches(m.eventId)) v += SchemaViolation("meta.eventId", "pattern:uuid", m.eventId)
        if (!Patterns.COMMAND_ID.matches(m.commandId)) {
            v += SchemaViolation("meta.commandId", "pattern:uuid|empty", m.commandId)
        }
        return v
    }
}

/** 会话 hello 校验（session-hello.schema.json；树形操作以覆盖 additionalProperties:false）。 */
object SessionHelloValidator {

    private val ROOT_KEYS = setOf(
        "type", "deviceId", "tokenVersion", "lastEpoch", "lastReceivedSeq", "capabilities", "meta",
    )
    private val CAP_KEYS = setOf("clientVersion", "encryptTypeMax", "commands")
    private val META_KEYS = setOf(
        "ver", "ts", "nonce", "hmac", "seq", "eventId", "commandId", "tokenVersion",
    )

    fun validate(root: JsonElement): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        val obj = root as? JsonObject ?: return listOf(SchemaViolation("$", "type:object", "非对象"))
        obj.keys.filter { it !in ROOT_KEYS }.forEach {
            v += SchemaViolation(it, "additionalProperties:false", "未知字段")
        }
        if ((obj.stringOf("type")) != "hello") v += SchemaViolation("type", "const:hello", obj.stringOf("type") ?: "?")
        val deviceId = obj.stringOf("deviceId")
        if (deviceId == null || !Patterns.DEVICE_ID.matches(deviceId)) {
            v += SchemaViolation("deviceId", "pattern:8..64", deviceId ?: "缺失")
        }
        val tokenVersion = obj.intOf("tokenVersion")
        if (tokenVersion == null || tokenVersion < 1) {
            v += SchemaViolation("tokenVersion", "minimum:1", tokenVersion?.toString() ?: "缺失")
        }
        obj.longOf("lastEpoch")?.let { if (it < 0) v += SchemaViolation("lastEpoch", "minimum:0", "$it") }
        obj.longOf("lastReceivedSeq")?.let { if (it < 0) v += SchemaViolation("lastReceivedSeq", "minimum:0", "$it") }
        (obj["capabilities"] as? JsonObject)?.let { caps ->
            caps.keys.filter { it !in CAP_KEYS }.forEach {
                v += SchemaViolation("capabilities.$it", "additionalProperties:false", "未知字段")
            }
            val cv = caps.stringOf("clientVersion")
            if (cv == null || cv.isEmpty() || cv.length > 32) {
                v += SchemaViolation("capabilities.clientVersion", "length:1..32", cv ?: "缺失")
            }
            caps.intOf("encryptTypeMax")?.let {
                if (it !in setOf(0, 2)) v += SchemaViolation("capabilities.encryptTypeMax", "enum:0,2", "$it")
            }
            (caps["commands"] as? kotlinx.serialization.json.JsonArray)?.forEachIndexed { i, c ->
                val t = (c as? JsonPrimitive)?.intOrNull
                if (t == null || t < 11 || t > 999) {
                    v += SchemaViolation("capabilities.commands[$i]", "range:11..999", t?.toString() ?: "?")
                }
            }
        }
        val meta = obj["meta"] as? JsonObject
        if (meta == null) {
            v += SchemaViolation("meta", "type:object", "缺失")
            return v
        }
        meta.keys.filter { it !in META_KEYS }.forEach {
            v += SchemaViolation("meta.$it", "additionalProperties:false", "未知字段")
        }
        META_KEYS.forEach { key ->
            if (meta[key] == null) v += SchemaViolation("meta.$key", "required", "缺失")
        }
        // frameMeta 通用约束 + hello 特化（seq 恒 1、commandId 空）
        meta.intOf("ver")?.let { if (it != 2) v += SchemaViolation("meta.ver", "const:2", "$it") }
        meta.longOf("ts")?.let { if (it < 0) v += SchemaViolation("meta.ts", "minimum:0", "$it") }
        meta.longOf("seq")?.let { if (it != 1L) v += SchemaViolation("meta.seq", "const:1", "$it") }
        meta.stringOf("commandId")?.let { if (it.isNotEmpty()) v += SchemaViolation("meta.commandId", "const:empty", it) }
        meta.intOf("tokenVersion")?.let { if (it < 1) v += SchemaViolation("meta.tokenVersion", "minimum:1", "$it") }
        meta.stringOf("nonce")?.let { if (!Patterns.NONCE.matches(it)) v += SchemaViolation("meta.nonce", "pattern:hex32", it) }
        meta.stringOf("hmac")?.let { if (!Patterns.HMAC.matches(it)) v += SchemaViolation("meta.hmac", "pattern:hex64", it) }
        meta.stringOf("eventId")?.let { if (!Patterns.UUID_LOWER.matches(it)) v += SchemaViolation("meta.eventId", "pattern:uuid", it) }
        return v
    }
}

/** 配对请求校验（pair-request.schema.json）。 */
object PairRequestValidator {

    private val KEYS = setOf(
        "pairingCode", "deviceId", "deviceName", "ed25519PublicKey", "x25519EphemeralPublicKey",
    )

    fun validate(root: JsonElement): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        val obj = root as? JsonObject ?: return listOf(SchemaViolation("$", "type:object", "非对象"))
        obj.keys.filter { it !in KEYS }.forEach {
            v += SchemaViolation(it, "additionalProperties:false", "未知字段")
        }
        val code = obj.stringOf("pairingCode")
        if (code == null || !Patterns.PAIRING_CODE.matches(code)) {
            v += SchemaViolation("pairingCode", "pattern:6digits", code ?: "缺失")
        }
        val deviceId = obj.stringOf("deviceId")
        if (deviceId == null || !Patterns.DEVICE_ID.matches(deviceId)) {
            v += SchemaViolation("deviceId", "pattern:8..64", deviceId ?: "缺失")
        }
        obj.stringOf("deviceName")?.let {
            if (it.isEmpty() || it.length > 64) v += SchemaViolation("deviceName", "length:1..64", "$it")
        }
        val ed = obj.stringOf("ed25519PublicKey")
        if (ed == null || !Patterns.HEX64_LOWER.matches(ed)) {
            v += SchemaViolation("ed25519PublicKey", "pattern:hex64", ed ?: "缺失")
        }
        val x = obj.stringOf("x25519EphemeralPublicKey")
        if (x == null || !Patterns.HEX64_LOWER.matches(x)) {
            v += SchemaViolation("x25519EphemeralPublicKey", "pattern:hex64", x ?: "缺失")
        }
        return v
    }
}

/** 配对响应校验（pair-response.schema.json 全约束，含 const 15/45 冻结值）。 */
object PairResponseValidator {

    private val KEYS = setOf(
        "deviceId", "sessionId", "deviceToken", "tokenVersion", "serverX25519PublicKey",
        "heartbeatIntervalSeconds", "sessionTimeoutSeconds", "rotationIntervalDays", "issuedAt", "wsPath",
    )

    fun validate(root: JsonElement): List<SchemaViolation> {
        val v = mutableListOf<SchemaViolation>()
        val obj = root as? JsonObject ?: return listOf(SchemaViolation("$", "type:object", "非对象"))
        obj.keys.filter { it !in KEYS }.forEach {
            v += SchemaViolation(it, "additionalProperties:false", "未知字段")
        }
        KEYS.forEach { key ->
            if (obj[key] == null) v += SchemaViolation(key, "required", "缺失")
        }
        obj.stringOf("deviceId")?.let {
            if (!Patterns.DEVICE_ID.matches(it)) v += SchemaViolation("deviceId", "pattern:8..64", it)
        }
        obj.stringOf("sessionId")?.let {
            if (!Patterns.UUID_LOWER.matches(it)) v += SchemaViolation("sessionId", "pattern:uuid", it)
        }
        obj.stringOf("deviceToken")?.let {
            if (!Patterns.HEX64_LOWER.matches(it)) v += SchemaViolation("deviceToken", "pattern:hex64", it)
        }
        obj.intOf("tokenVersion")?.let { if (it < 1) v += SchemaViolation("tokenVersion", "minimum:1", "$it") }
        obj.stringOf("serverX25519PublicKey")?.let {
            if (!Patterns.HEX64_LOWER.matches(it)) v += SchemaViolation("serverX25519PublicKey", "pattern:hex64", it)
        }
        obj.intOf("heartbeatIntervalSeconds")?.let {
            if (it != 15) v += SchemaViolation("heartbeatIntervalSeconds", "const:15", "$it")
        }
        obj.intOf("sessionTimeoutSeconds")?.let {
            if (it != 45) v += SchemaViolation("sessionTimeoutSeconds", "const:45", "$it")
        }
        obj.intOf("rotationIntervalDays")?.let {
            if (it < 1) v += SchemaViolation("rotationIntervalDays", "minimum:1", "$it")
        }
        obj.longOf("issuedAt")?.let { if (it < 0) v += SchemaViolation("issuedAt", "minimum:0", "$it") }
        obj.stringOf("wsPath")?.let {
            if (!Patterns.WS_PATH.matches(it)) v += SchemaViolation("wsPath", "pattern:/api/v2/device-sessions/{uuid}", it)
        }
        return v
    }
}

/** 结果回执载荷校验（exec-callback.schema.json）。 */
object ExecCallbackValidator {

    fun validate(bean: WeworkMessageBean): List<SchemaViolation> {
        val v = BeanValidator.violations(bean, "execCallback").toMutableList()
        if (bean.type < 201 || bean.type > 509) {
            v += SchemaViolation("type", "range:201..509", "${bean.type}")
        }
        if (bean.errorCode == null || bean.errorCode < 0) {
            v += SchemaViolation("errorCode", "required,minimum:0", bean.errorCode?.toString() ?: "缺失")
        }
        return v
    }
}

// ---- 树形取值工具 ----

internal fun JsonObject.stringOf(name: String): String? =
    (this[name] as? JsonPrimitive)?.takeIf { it.isString }?.content

internal fun JsonObject.intOf(name: String): Int? =
    (this[name] as? JsonPrimitive)?.takeIf { !it.isString }?.intOrNull

internal fun JsonObject.longOf(name: String): Long? =
    (this[name] as? JsonPrimitive)?.takeIf { !it.isString }?.longOrNull
