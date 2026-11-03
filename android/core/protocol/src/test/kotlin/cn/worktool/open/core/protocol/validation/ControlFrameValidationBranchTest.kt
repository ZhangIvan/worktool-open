/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.validation

import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * T3.4 三校验器（HelloAck/SessionHello/ControlPayload）逐约束分支补面
 * （CR T2.6-R3：模块 BRANCH ≥80% 硬门禁的缺口全部来自本三类的未命中分支；
 * 正例基准 = session-hello-ack.go-actual.json / session-hello.valid.json /
 * control-payload.* 夹具，此处逐字段变异打未覆盖分支，不改变冻结语义）。
 */
class ControlFrameValidationBranchTest {

    private fun s(v: String) = JsonPrimitive(v)
    private fun i(v: Int) = JsonPrimitive(v)
    private fun l(v: Long) = JsonPrimitive(v)

    private val nonce = "2e3b98e90a47796c8e86633fddac8d88"
    private val hmac = "c7626564dc10c1389c35c699ffd306d6b9bfbccd47d62e0ab0a2097457422f70"
    private val eventId = "1d97397d-67ef-4e01-a6ee-400e16e22f00"
    private val ts = 1_793_659_572_338L

    private fun assertHas(vs: List<SchemaViolation>, field: String, rulePrefix: String) =
        assertTrue(
            "期望违例 $field（$rulePrefix*），实际: $vs",
            vs.any { it.field == field && it.rule.startsWith(rulePrefix) },
        )

    // ---- HelloAckValidator（session-hello-ack.schema.json 逐约束）----

    private fun ackMeta(
        ver: JsonElement = i(2),
        tsEl: JsonElement? = l(ts),
        nonceEl: JsonElement? = s(nonce),
        hmacEl: JsonElement? = s(hmac),
        seq: JsonElement = l(1),
        eventIdEl: JsonElement = s(eventId),
        commandId: JsonElement = s(""),
        tokenVersion: JsonElement = i(1),
        extra: Pair<String, JsonElement>? = null,
    ): JsonObject = buildJsonObject {
        ver?.let { put("ver", it) }
        tsEl?.let { put("ts", it) }
        nonceEl?.let { put("nonce", it) }
        hmacEl?.let { put("hmac", it) }
        put("seq", seq)
        put("eventId", eventIdEl)
        put("commandId", commandId)
        put("tokenVersion", tokenVersion)
        extra?.let { put(it.first, it.second) }
    }

    private fun ackRoot(
        type: JsonElement = s("hello_ack"),
        serverTime: JsonElement? = l(ts),
        resume: JsonElement? = l(1),
        heartbeat: JsonElement? = i(15),
        timeout: JsonElement? = i(45),
        meta: JsonElement? = ackMeta(),
        extra: Pair<String, JsonElement>? = null,
    ): JsonObject = buildJsonObject {
        put("type", type)
        serverTime?.let { put("serverTime", it) }
        resume?.let { put("resumeFromSeq", it) }
        heartbeat?.let { put("heartbeatIntervalSeconds", it) }
        timeout?.let { put("sessionTimeoutSeconds", it) }
        meta?.let { put("meta", it) }
        extra?.let { put(it.first, it.second) }
    }

    @Test
    fun `hello_ack 根与必含逐字段缺失`() {
        // 非对象根
        assertTrue(HelloAckValidator.validate(JsonArray(emptyList())).any { it.field == "$" })
        assertTrue(HelloAckValidator.validate(JsonPrimitive(1)).any { it.field == "$" })
        // 必含逐字段缺失（ROOT_KEYS 全集）
        listOf("serverTime", "resumeFromSeq", "heartbeatIntervalSeconds", "sessionTimeoutSeconds", "meta").forEach { key ->
            val root = ackRoot().let { JsonObject(it.keys.filter { k -> k != key }.associateWith { k -> it[k]!! }) }
            assertHas(HelloAckValidator.validate(root), key, "required")
        }
        // 未知顶层字段（additionalProperties:false）
        assertHas(HelloAckValidator.validate(ackRoot(extra = "zz" to i(1))), "zz", "additionalProperties")
    }

    @Test
    fun `hello_ack 顶层标量约束逐分支`() {
        // type 非法（错值 / 非字符串）
        assertHas(HelloAckValidator.validate(ackRoot(type = s("bye"))), "type", "const:hello_ack")
        assertHas(HelloAckValidator.validate(ackRoot(type = i(903))), "type", "const:hello_ack")
        assertHas(HelloAckValidator.validate(ackRoot(type = s("hello"))), "type", "const:hello_ack")
        // serverTime 负值
        assertHas(HelloAckValidator.validate(ackRoot(serverTime = l(-1))), "serverTime", "minimum:0")
        // resumeFromSeq <1 / 非整数
        assertHas(HelloAckValidator.validate(ackRoot(resume = l(0))), "resumeFromSeq", "minimum:1")
        assertHas(HelloAckValidator.validate(ackRoot(resume = s("x"))), "resumeFromSeq", "minimum:1")
        // 冻结 const 15/45
        assertHas(HelloAckValidator.validate(ackRoot(heartbeat = i(30))), "heartbeatIntervalSeconds", "const:15")
        assertHas(HelloAckValidator.validate(ackRoot(timeout = i(30))), "sessionTimeoutSeconds", "const:45")
        // 合法基准零违例（分支另一侧）
        assertTrue(HelloAckValidator.validate(ackRoot()).isEmpty())
    }

    @Test
    fun `hello_ack meta 逐字段违例与不变式`() {
        // meta 非对象（string）→ type:object
        assertHas(HelloAckValidator.validate(ackRoot(meta = s("x"))), "meta", "type:object")
        // 未知 meta 字段
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(extra = "zz" to i(1)))), "meta.zz", "additionalProperties")
        // 必含逐字段缺失
        listOf("ts", "nonce", "hmac").forEach { key ->
            val m = ackMeta().let { JsonObject(it.keys.filter { k -> k != key }.associateWith { k -> it[k]!! }) }
            assertHas(HelloAckValidator.validate(ackRoot(meta = m)), "meta.$key", "required")
        }
        // 标量约束
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(ver = i(3)))), "meta.ver", "const:2")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(tsEl = l(-1)))), "meta.ts", "minimum:0")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(seq = l(0)))), "meta.seq", "minimum:1")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(commandId = s("cmd")))), "meta.commandId", "const:empty")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(tokenVersion = i(0)))), "meta.tokenVersion", "minimum:1")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(nonceEl = s("nothex")))), "meta.nonce", "pattern:hex32")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(hmacEl = s("short")))), "meta.hmac", "pattern:hex64")
        assertHas(HelloAckValidator.validate(ackRoot(meta = ackMeta(eventIdEl = s("not-uuid")))), "meta.eventId", "pattern:uuid")
        // 语义不变式：serverTime==meta.ts / resumeFromSeq==meta.seq（含缺失侧短路分支）
        assertHas(HelloAckValidator.validate(ackRoot(serverTime = l(ts + 1))), "serverTime", "invariant:==meta.ts")
        assertHas(HelloAckValidator.validate(ackRoot(resume = l(2))), "resumeFromSeq", "invariant:==meta.seq")
        assertHas(HelloAckValidator.validate(ackRoot(resume = l(1), meta = ackMeta(seq = l(3)))), "resumeFromSeq", "invariant:==meta.seq")
        // resume/ts 缺失 → 不变式跳过（不误报 NPE）
        assertTrue(HelloAckValidator.validate(ackRoot(resume = null)).any { it.field == "resumeFromSeq" })
        assertTrue(HelloAckValidator.validate(ackRoot(meta = ackMeta(tsEl = null))).any { it.field == "meta.ts" })
    }

    // ---- SessionHelloValidator（session-hello.schema.json 未命中分支）----

    private fun helloMeta(
        ver: JsonElement = i(2),
        tsEl: JsonElement = l(ts),
        nonceEl: JsonElement = s(nonce),
        hmacEl: JsonElement = s(hmac),
        seq: JsonElement = l(1),
        eventIdEl: JsonElement = s(eventId),
        commandId: JsonElement = s(""),
        tokenVersion: JsonElement = i(1),
    ): JsonObject = buildJsonObject {
        put("ver", ver); put("ts", tsEl); put("nonce", nonceEl); put("hmac", hmacEl)
        put("seq", seq); put("eventId", eventIdEl); put("commandId", commandId); put("tokenVersion", tokenVersion)
    }

    private fun helloRoot(
        type: JsonElement = s("hello"),
        deviceId: JsonElement? = s("device-9f2c7b41"),
        tokenVersion: JsonElement? = i(1),
        lastEpoch: JsonElement? = l(0),
        lastReceivedSeq: JsonElement? = l(0),
        capabilities: JsonElement = buildJsonObject {
            put("clientVersion", "0.1.0"); put("encryptTypeMax", 2); put("commands", JsonArray(listOf(i(203))))
        },
        meta: JsonElement? = helloMeta(),
    ): JsonObject = buildJsonObject {
        put("type", type)
        deviceId?.let { put("deviceId", it) }
        tokenVersion?.let { put("tokenVersion", it) }
        lastEpoch?.let { put("lastEpoch", it) }
        lastReceivedSeq?.let { put("lastReceivedSeq", it) }
        put("capabilities", capabilities)
        meta?.let { put("meta", it) }
    }

    @Test
    fun `session hello 必含缺失与类型分支`() {
        assertTrue(SessionHelloValidator.validate(JsonPrimitive("x")).any { it.field == "$" })
        // 必含字段缺失（null 侧分支）
        assertHas(SessionHelloValidator.validate(helloRoot(deviceId = null)), "deviceId", "pattern:8..64")
        assertHas(SessionHelloValidator.validate(helloRoot(tokenVersion = null)), "tokenVersion", "minimum:1")
        // lastEpoch/lastReceivedSeq 为可选字段（schema 非 required）→ 缺失零违例（负值才拒）
        assertTrue(SessionHelloValidator.validate(helloRoot(lastEpoch = null, lastReceivedSeq = null)).isEmpty())
        // 非法值侧
        assertHas(SessionHelloValidator.validate(helloRoot(deviceId = s("short"))), "deviceId", "pattern:8..64")
        assertHas(SessionHelloValidator.validate(helloRoot(tokenVersion = i(0))), "tokenVersion", "minimum:1")
        assertHas(SessionHelloValidator.validate(helloRoot(lastEpoch = l(-1))), "lastEpoch", "minimum:0")
        assertHas(SessionHelloValidator.validate(helloRoot(lastReceivedSeq = l(-1))), "lastReceivedSeq", "minimum:0")
        assertHas(
            SessionHelloValidator.validate(helloRoot(type = s("hola"), deviceId = null, capabilities = i(1), meta = null)),
            "type", "const:hello",
        )
        // capabilities 非对象（schema type:object 镜像；修复前 as? 静默跳过）
        assertHas(SessionHelloValidator.validate(helloRoot(capabilities = s("x"))), "capabilities", "type:object")
        assertTrue(SessionHelloValidator.validate(helloRoot()).isEmpty())
    }

    @Test
    fun `session hello capabilities 逐分支`() {
        fun caps(vararg pairs: Pair<String, JsonElement>) = buildJsonObject {
            pairs.forEach { (k, v) -> put(k, v) }
        }
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps())),
            "capabilities.clientVersion", "length:1..32",
        )
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s(""), "encryptTypeMax" to i(2), "commands" to JsonArray(listOf(i(203)))))),
            "capabilities.clientVersion", "length:1..32",
        )
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s("x".repeat(33)), "encryptTypeMax" to i(2), "commands" to JsonArray(listOf(i(203)))))),
            "capabilities.clientVersion", "length:1..32",
        )
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s("0.1.0"), "encryptTypeMax" to i(1), "commands" to JsonArray(listOf(i(203)))))),
            "capabilities.encryptTypeMax", "enum:0,2",
        )
        // commands 元素非整数 / 越界（11..999）
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s("0.1.0"), "encryptTypeMax" to i(2), "commands" to JsonArray(listOf(s("x"), i(10), i(1000)))))),
            "capabilities.commands[0]", "range:11..999",
        )
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s("0.1.0"), "encryptTypeMax" to i(2), "commands" to JsonArray(listOf(i(5)))))),
            "capabilities.commands[0]", "range:11..999",
        )
        // capabilities 未知字段
        assertHas(
            SessionHelloValidator.validate(helloRoot(capabilities = caps("clientVersion" to s("0.1.0"), "encryptTypeMax" to i(2), "commands" to JsonArray(listOf(i(203))), "zz" to i(1)))),
            "capabilities.zz", "additionalProperties",
        )
    }

    @Test
    fun `session hello meta 未命中分支`() {
        assertHas(SessionHelloValidator.validate(helloRoot(meta = null)), "meta", "type:object")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(ver = i(3)))), "meta.ver", "const:2")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(tsEl = l(-1)))), "meta.ts", "minimum:0")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(seq = l(2)))), "meta.seq", "const:1")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(commandId = s("cmd")))), "meta.commandId", "const:empty")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(tokenVersion = i(0)))), "meta.tokenVersion", "minimum:1")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(nonceEl = s("nothex")))), "meta.nonce", "pattern:hex32")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(hmacEl = s("short")))), "meta.hmac", "pattern:hex64")
        assertHas(SessionHelloValidator.validate(helloRoot(meta = helloMeta(eventIdEl = s("not-uuid")))), "meta.eventId", "pattern:uuid")
    }

    // ---- ControlPayloadValidator（control-payload.schema.json 未命中分支）----

    @Test
    fun `901 逐字段缺失与形状违例`() {
        fun p901(
            deviceToken: JsonElement? = s(hmac),
            tokenVersion: JsonElement? = i(2),
            serverPub: JsonElement? = s(hmac),
        ) = buildJsonObject {
            put("type", 901)
            deviceToken?.let { put("deviceToken", it) }
            tokenVersion?.let { put("tokenVersion", it) }
            serverPub?.let { put("serverX25519PublicKey", it) }
        }
        // 合法基准
        assertTrue(ControlPayloadValidator.validate(p901()).isEmpty())
        // 缺失分支
        assertHas(ControlPayloadValidator.validate(p901(deviceToken = null)), "deviceToken", "required")
        assertHas(ControlPayloadValidator.validate(p901(tokenVersion = null)), "tokenVersion", "required")
        assertHas(ControlPayloadValidator.validate(p901(serverPub = null)), "serverX25519PublicKey", "required")
        // 形状/取值分支
        assertHas(ControlPayloadValidator.validate(p901(deviceToken = s("zz"))), "deviceToken", "pattern:hex64")
        assertHas(ControlPayloadValidator.validate(p901(tokenVersion = i(1))), "tokenVersion", "minimum:2")
        assertHas(ControlPayloadValidator.validate(p901(serverPub = s("zz"))), "serverX25519PublicKey", "pattern:hex64")
    }

    @Test
    fun `904 entries-request 变体与条目逐分支`() {
        fun entry(
            commandId: JsonElement? = s(eventId),
            epoch: JsonElement? = l(1),
            started: JsonElement = JsonPrimitive(true),
            outcome: JsonElement? = s("success"),
            source: JsonElement? = s("local"),
        ) = buildJsonObject {
            commandId?.let { put("commandId", it) }
            epoch?.let { put("epoch", it) }
            put("started", started)
            outcome?.let { put("outcome", it) }
            source?.let { put("source", it) }
        }
        // 合法补报（entries 非空 + 条目全过 = 各分支通过侧）
        assertTrue(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry()))) },
            ).isEmpty(),
        )
        // oneOf 两侧
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", 904) }),
            "entries|request", "oneOf",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject {
                    put("type", 904)
                    put("entries", JsonArray(listOf(entry())))
                    put("request", "journal_replay")
                },
            ),
            "entries|request", "oneOf",
        )
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", 904); put("request", "other") }),
            "request", "const:journal_replay",
        )
        // 空数组
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", 904); put("entries", JsonArray(emptyList())) }),
            "entries", "minItems:1",
        )
        // 条目非对象
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(i(1)))) },
            ),
            "entries[0]", "type:object",
        )
        // 条目未知字段 / 必含缺失
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject {
                    put("type", 904)
                    put("entries", JsonArray(listOf(buildJsonObject {
                        put("commandId", eventId); put("epoch", 1L); put("started", true)
                        put("outcome", "success"); put("source", "local"); put("zz", 1)
                    })))
                },
            ),
            "entries[0].zz", "additionalProperties",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(source = null)))) },
            ),
            "entries[0].source", "required",
        )
        // 条目字段取值
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(commandId = s("x"))))) },
            ),
            "entries[0].commandId", "pattern:uuid",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(epoch = l(-1))))) },
            ),
            "entries[0].epoch", "minimum:0",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(started = s("yes"))))) },
            ),
            "entries[0].started", "type:boolean",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(outcome = s("weird"))))) },
            ),
            "entries[0].outcome", "enum",
        )
        assertHas(
            ControlPayloadValidator.validate(
                buildJsonObject { put("type", 904); put("entries", JsonArray(listOf(entry(source = s("elsewhere"))))) },
            ),
            "entries[0].source", "enum:local,server",
        )
    }

    @Test
    fun `载荷 type 分支-缺失-非整数-越界-902-903 未知字段`() {
        // type 缺失 / 非整数
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("deviceToken", hmac) }),
            "type", "required",
        )
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", s("903")) }),
            "type", "required",
        )
        // 越界
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", 905) }),
            "type", "const:901..904",
        )
        // 902/903 合法（载荷仅 type）与未知字段
        assertTrue(ControlPayloadValidator.validate(buildJsonObject { put("type", 902) }).isEmpty())
        assertTrue(ControlPayloadValidator.validate(buildJsonObject { put("type", 903) }).isEmpty())
        assertHas(
            ControlPayloadValidator.validate(buildJsonObject { put("type", 903); put("zz", 1) }),
            "zz", "additionalProperties",
        )
    }
}
