/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.validation

import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.FixtureFiles
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.WireMessage
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 校验器负路径全覆盖：对夹具正例逐字段变异，每个 schema 约束分支必须命中
 * （协议单测 ≥80% 分支 DoD 的主要贡献面）。
 */
class ValidatorNegativePathsTest {

    private val codec = EnvelopeCodec()

    /** JSON 文本级替换（对夹具做最小变异）。 */
    private fun mutate(text: String, from: String, to: String): String =
        text.replace(from, to, ignoreCase = false)

    private fun violationsOf(text: String): List<SchemaViolation> {
        val decoded = codec.decode(text)
        return when (decoded) {
            is WireMessage.V1 -> V1EnvelopeValidator.validate(decoded.envelope)
            is WireMessage.V2 -> V2FrameValidator.validate(decoded.frame)
            else -> emptyList()
        }
    }

    // ---- v1 信封（wire-v1-envelope.schema.json 逐约束）----

    @Test
    fun `v1 socketType 越界`() {
        val t = mutate(FixtureFiles.text("wire-v1-envelope.heartbeat.json"), "\"socketType\": 0", "\"socketType\": 9")
        assertTrue(violationsOf(t).any { it.field == "socketType" && it.rule == "enum:0,1,2,3" })
    }

    @Test
    fun `v1 messageId 模式违例`() {
        val t = mutate(FixtureFiles.text("wire-v1-envelope.heartbeat.json"), "2026-11-02#10:00:00#", "2026-11-02 10:00:00#")
        assertTrue(violationsOf(t).any { it.field == "messageId" && it.rule.startsWith("pattern") })
    }

    @Test
    fun `v1 apiSend 越界`() {
        val t = mutate(FixtureFiles.text("wire-v1-envelope.command-203.json"), "\"apiSend\": 1", "\"apiSend\": 7")
        assertTrue(violationsOf(t).any { it.field == "apiSend" })
    }

    @Test
    fun `v1 encryptType 越界（v1 只允许 0 或 1）`() {
        val t = mutate(FixtureFiles.text("wire-v1-envelope.command-203.json"), "\"encryptType\": 0", "\"encryptType\": 2")
        // 注意：encryptType=2 + list 在 v1 校验器必须报 enum 违例（v2 由 meta 形态分流）
        assertTrue(violationsOf(t).any { it.field == "encryptType" })
    }

    @Test
    fun `v1 encryptType=1 缺 encryptedList`() {
        val t = """
            {"socketType":2,"messageId":"2026-11-02#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
             "meta":"","apiSend":1,"encryptType":1}
        """.trimIndent()
        assertTrue(violationsOf(t).any { it.field == "encryptedList" })
    }

    @Test
    fun `v1 encryptType 缺省时 list 必须 present`() {
        val t = """{"socketType":2,"apiSend":0}"""
        assertTrue(violationsOf(t).any { it.field == "list" && it.rule.startsWith("required") })
    }

    @Test
    fun `v1 encryptedList 非 hex`() {
        val t = """
            {"socketType":2,"messageId":"2026-11-02#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
             "meta":"","apiSend":1,"encryptType":1,"encryptedList":"xyz"}
        """.trimIndent()
        assertTrue(violationsOf(t).any { it.field == "encryptedList" && it.rule == "pattern:hex" })
    }

    @Test
    fun `v1 list 元素范围约束（roomType-textType）`() {
        val t = mutate(FixtureFiles.text("wire-v1-envelope.report-101.json"), "\"roomType\": 2", "\"roomType\": 9")
        assertTrue(violationsOf(t).any { it.field.startsWith("list[0].roomType") })
        val t2 = mutate(FixtureFiles.text("wire-v1-envelope.report-101.json"), "\"textType\": 1,", "\"textType\": 99,")
        assertTrue(violationsOf(t2).any { it.field.startsWith("list[0].textType") || it.field.startsWith("list[0].subMessageList") })
    }

    // ---- v2 帧信封（command-envelope.schema.json 逐约束）----

    private val v2Text = FixtureFiles.text("command-envelope.plain.json")

    @Test
    fun `v2 socketType 越界（socketType=1 在 v2 不用）`() {
        val t = mutate(v2Text, "\"socketType\": 2", "\"socketType\": 1")
        assertTrue(violationsOf(t).any { it.field == "socketType" && it.rule == "enum:0,2,3" })
    }

    @Test
    fun `v2 apiSend 越界`() {
        val t = mutate(v2Text, "\"apiSend\": 1", "\"apiSend\": 5")
        assertTrue(violationsOf(t).any { it.field == "apiSend" })
    }

    @Test
    fun `v2 encryptType 越界（仅 0 或 2）`() {
        val t = mutate(v2Text, "\"encryptType\": 0", "\"encryptType\": 1")
        assertTrue(violationsOf(t).any { it.field == "encryptType" && it.rule == "enum:0,2" })
    }

    @Test
    fun `v2 encryptType=0 缺 list`() {
        val t = mutate(v2Text, ",\n  \"encryptType\": 0,\n  \"list\": [", ",\n  \"encryptType\": 0,\n  \"listX\": [")
        assertTrue(violationsOf(t).any { it.field == "list" && it.rule.startsWith("required") })
    }

    @Test
    fun `v2 meta 逐字段违例`() {
        // ver != 2
        assertTrue(
            violationsOf(mutate(v2Text, "\"ver\": 2", "\"ver\": 3")).any { it.field == "meta.ver" && it.rule == "const:2" },
        )
        // ts < 0
        assertTrue(
            violationsOf(mutate(v2Text, "\"ts\": 1793613605000", "\"ts\": -1")).any { it.field == "meta.ts" },
        )
        // seq < 1
        assertTrue(
            violationsOf(mutate(v2Text, "\"seq\": 413", "\"seq\": 0")).any { it.field == "meta.seq" },
        )
        // tokenVersion < 1
        assertTrue(
            violationsOf(mutate(v2Text, "\"tokenVersion\": 1", "\"tokenVersion\": 0")).any { it.field == "meta.tokenVersion" },
        )
        // nonce 非 32 hex
        assertTrue(
            violationsOf(mutate(v2Text, "5c1f9a7b3e2d4c6f8a0b2d4e6f8a0c2e", "NOTHEX")).any { it.field == "meta.nonce" },
        )
        // hmac 非 64 hex
        assertTrue(
            violationsOf(mutate(v2Text, "c7626564dc10c1389c35c699ffd306d6b9bfbccd47d62e0ab0a2097457422f70", "abc"))
                .any { it.field == "meta.hmac" },
        )
        // eventId 非 UUID
        assertTrue(
            violationsOf(mutate(v2Text, "7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a", "not-a-uuid")).any { it.field == "meta.eventId" },
        )
        // commandId 非空非 UUID
        assertTrue(
            violationsOf(mutate(v2Text, "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", "XX")).any { it.field == "meta.commandId" },
        )
    }

    @Test
    fun `v2 messageId 模式违例`() {
        val t = mutate(v2Text, "2026-11-02#10:00:05#", "bogus#")
        assertTrue(violationsOf(t).any { it.field == "messageId" })
    }

    // ---- 载荷单元约束 ----

    @Test
    fun `bean 校验负路径`() {
        fun v(bean: WeworkMessageBean) = BeanValidator.violations(bean, "list[0]")

        assertTrue(v(WeworkMessageBean(type = 1, roomType = 5)).any { it.field.endsWith("roomType") })
        assertTrue(v(WeworkMessageBean(type = 1, textType = 19)).any { it.field.endsWith("textType") })
        assertTrue(v(WeworkMessageBean(type = 1, source = "elsewhere")).any { it.field.endsWith("source") })
        assertTrue(v(WeworkMessageBean(type = 1, errorCode = -1)).any { it.field.endsWith("errorCode") })
        assertTrue(v(WeworkMessageBean(type = 1, timeCost = -5)).any { it.field.endsWith("timeCost") })

        val sub = cn.worktool.open.core.protocol.SubMessageBean(
            sender = 3, textType = 20, imageSize = -1,
            itemMessageList = listOf(cn.worktool.open.core.protocol.ItemMessageBean(feature = 1)),
        )
        val vs = v(WeworkMessageBean(type = 1, subMessageList = listOf(sub)))
        assertTrue(vs.any { it.field.contains("sender") })
        assertTrue(vs.any { it.field.contains("textType") })
        assertTrue(vs.any { it.field.contains("imageSize") })
        assertTrue(vs.any { it.field.contains("feature") })
    }

    // ---- hello 校验（session-hello.schema.json 逐约束）----

    private val helloText = FixtureFiles.text("session-hello.valid.json")

    private fun helloViolations(mutator: (String) -> String): List<SchemaViolation> =
        SessionHelloValidator.validate(Json.parseToJsonElement(mutator(helloText)))

    @Test
    fun `hello 负路径逐字段`() {
        assertTrue(helloViolations { it.replace("\"hello\"", "\"bye\"") }.any { it.field == "type" })
        assertTrue(helloViolations { it.replace("device-9f2c7b41", "short") }.any { it.field == "deviceId" })
        assertTrue(helloViolations { it.replace("\"tokenVersion\": 1", "\"tokenVersion\": 0", ) }.any { it.field == "tokenVersion" || it.field == "meta.tokenVersion" })
        assertTrue(helloViolations { it.replace("\"lastEpoch\": 128", "\"lastEpoch\": -1") }.any { it.field == "lastEpoch" })
        assertTrue(helloViolations { it.replace("\"lastReceivedSeq\": 412", "\"lastReceivedSeq\": -2") }.any { it.field == "lastReceivedSeq" })
        assertTrue(helloViolations { it.replace("\"encryptTypeMax\": 2", "\"encryptTypeMax\": 1") }.any { it.field.startsWith("capabilities") })
        assertTrue(helloViolations { it.replace("\"clientVersion\": \"0.1.0\"", "\"clientVersion\": \"\"") }.any { it.field == "capabilities.clientVersion" })
        assertTrue(helloViolations { it.replace("\"commands\"", "\"commandsX\"") }.any { it.field.startsWith("meta") || it.field.startsWith("capabilities") })
        // seq 恒 1 / commandId 恒空
        assertTrue(helloViolations { it.replace("\"seq\": 1", "\"seq\": 2") }.any { it.field == "meta.seq" && it.rule == "const:1" })
        assertTrue(helloViolations { it.replace("\"commandId\": \"\"", "\"commandId\": \"x\"") }.any { it.field == "meta.commandId" })
        // 未知字段（additionalProperties:false，三层）
        assertTrue(helloViolations { it.replace("{\n  \"type\": \"hello\"", "{\n  \"type\": \"hello\", \"zz\": 1") }.any { it.field == "zz" })
        assertTrue(helloViolations { it.replace("\"capabilities\": {", "\"capabilities\": {\"zz\":1,") }.any { it.field == "capabilities.zz" })
        assertTrue(helloViolations { it.replace("\"meta\": {\n    \"ver\": 2", "\"meta\": {\n    \"ver\": 2, \"zz\": 9") }.any { it.field == "meta.zz" })
        // 必含字段缺失
        assertTrue(helloViolations { it.replace("\"meta\": {", "\"metaX\": {") }.any { it.field == "meta" && it.rule == "type:object" })
        // 非对象根
        assertTrue(SessionHelloValidator.validate(Json.parseToJsonElement("[1]")).any { it.field == "$" })
    }

    // ---- pair request/response（逐约束）----

    @Test
    fun `pair-request 负路径`() {
        val valid = FixtureFiles.element("pair-request.valid.json").toString()
        fun pr(mutator: (String) -> String) =
            PairRequestValidator.validate(Json.parseToJsonElement(mutator(valid)))
        assertTrue(pr { it.replace("482913", "4821") }.any { it.field == "pairingCode" })       // 5 位
        assertTrue(pr { it.replace("482913", "482a13") }.any { it.field == "pairingCode" })     // 非数字
        assertTrue(pr { it.replace("device-9f2c7b41", "dev") }.any { it.field == "deviceId" })
        assertTrue(pr { it.replace("lab-main", "") }.any { it.field == "deviceName" })           // 长度 0
        assertTrue(pr { it.replace("67f1dc5eb7b5c4ea807c3c4bf86b704e646200ebb5505cac939bc94883c02f3c", "67f1dc5e") }.any { it.field == "ed25519PublicKey" })
        assertTrue(pr { it.replace("7a6f28f12b1ebc96c379eb3e634e359c380c7a5ff139f11ee47140bb2774189c", "zz".repeat(32)) }.any { it.field == "x25519EphemeralPublicKey" })
        assertTrue(pr { it.replace("\"deviceName\":\"lab-main\",", "\"deviceName\":\"lab-main\",\"extra\":1,") }.any { it.field == "extra" })
        // 必含字段缺失
        assertTrue(pr { it.replace("\"pairingCode\":\"482913\",", "") }.any { it.field == "pairingCode" })
        // 非对象根
        assertTrue(PairRequestValidator.validate(Json.parseToJsonElement("[]")).any { it.field == "$" })
    }

    @Test
    fun `pair-response 负路径`() {
        val valid = FixtureFiles.element("pair-response.valid.json").toString()
        fun pr(mutator: (String) -> String) =
            PairResponseValidator.validate(Json.parseToJsonElement(mutator(valid)))
        // const 违例（15/45 冻结值）
        assertTrue(pr { it.replace("\"heartbeatIntervalSeconds\":15", "\"heartbeatIntervalSeconds\":5") }.any { it.field == "heartbeatIntervalSeconds" && it.rule == "const:15" })
        assertTrue(pr { it.replace("\"sessionTimeoutSeconds\":45", "\"sessionTimeoutSeconds\":30") }.any { it.field == "sessionTimeoutSeconds" && it.rule == "const:45" })
        assertTrue(pr { it.replace("\"rotationIntervalDays\":30", "\"rotationIntervalDays\":0") }.any { it.field == "rotationIntervalDays" })
        assertTrue(pr { it.replace("\"issuedAt\":1791000000000", "\"issuedAt\":-1") }.any { it.field == "issuedAt" })
        assertTrue(pr { it.replace("device-9f2c7b41", "x!") }.any { it.field == "deviceId" })
        assertTrue(pr { it.replace("5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80", "zz") }.any { it.field == "sessionId" })
        assertTrue(pr { it.replace("e0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1", "abc") }.any { it.field == "deviceToken" })
        assertTrue(pr { it.replace("8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d", "zz") }.any { it.field == "serverX25519PublicKey" })
        assertTrue(pr { it.replace("\"tokenVersion\":1", "\"tokenVersion\":0") }.any { it.field == "tokenVersion" })
        assertTrue(pr { it.replace("\"wsPath\":\"/api/v2/device-sessions/5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80\"", "\"wsPath\":\"/other\"") }.any { it.field == "wsPath" })
        // 必含缺失 + 未知字段
        assertTrue(pr { it.replace("\"deviceId\":\"device-9f2c7b41\",", "") }.any { it.field == "deviceId" })
        assertTrue(pr { it.replace("\"tokenVersion\":1,", "\"tokenVersion\":1,\"zz\":1,") }.any { it.field == "zz" })
        assertTrue(PairResponseValidator.validate(Json.parseToJsonElement("3")).any { it.field == "$" })
    }

    // ---- exec callback（exec-callback.schema.json）----

    @Test
    fun `exec-callback 负路径`() {
        assertTrue(ExecCallbackValidator.validate(WeworkMessageBean(type = 11)).any { it.field == "type" && it.rule.startsWith("range") })
        assertTrue(ExecCallbackValidator.validate(WeworkMessageBean(type = 600)).any { it.field == "type" })
        assertTrue(ExecCallbackValidator.validate(WeworkMessageBean(type = 203)).any { it.field == "errorCode" && it.rule.startsWith("required") })
        assertTrue(ExecCallbackValidator.validate(WeworkMessageBean(type = 203, errorCode = -1)).any { it.field == "errorCode" })
    }
}
