/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.validation.ExecCallbackValidator
import cn.worktool.open.core.protocol.validation.PairRequestValidator
import cn.worktool.open.core.protocol.validation.PairResponseValidator
import cn.worktool.open.core.protocol.validation.SessionHelloValidator
import cn.worktool.open.core.protocol.validation.V1EnvelopeValidator
import cn.worktool.open.core.protocol.validation.V2FrameValidator
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/**
 * 夹具正反例全量校验（docs/03 T2.2 DoD：夹具正反例全量校验，CI 必跑）。
 *
 * 正例（protocol/fixtures/）逐文件解码 + schema 等价校验通过；
 * 反例（protocol/fixtures/negative/）必须被拒（解码失败或校验违例）。
 */
class FixtureSuiteTest {

    private val codec = EnvelopeCodec()

    // ---- v1 兼容信封（wire-v1-envelope.*）----

    @Test
    fun `v1 wire 夹具全部通过解码与校验`() {
        val v1Fixtures = listOf(
            "wire-v1-envelope.heartbeat.json",
            "wire-v1-envelope.report-101.json",
            "wire-v1-envelope.command-203.json",
        )
        v1Fixtures.forEach { name ->
            val decoded = codec.decode(FixtureFiles.text(name))
            val env = (decoded as WireMessage.V1).envelope
            assertTrue("$name v1 夹具 meta 必须是字符串形态", env.meta is String || env.meta == null)
            assertTrue(
                "$name 校验违例: ${V1EnvelopeValidator.validate(env)}",
                V1EnvelopeValidator.validate(env).isEmpty(),
            )
            // 编码往返：再解码等值
            val reEncoded = codec.encodeV1(env)
            val reDecoded = codec.decode(reEncoded) as WireMessage.V1
            assertEquals(env, reDecoded.envelope)
        }
    }

    // ---- v2 帧信封（command-envelope.*）----

    @Test
    fun `v2 帧夹具全部通过解码与校验`() {
        val v2Fixtures = listOf(
            "command-envelope.plain.json",
            "command-envelope.encrypted.json",
            "command-envelope.heartbeat.json",
            "command-envelope.exec-result.json",
        )
        v2Fixtures.forEach { name ->
            val decoded = codec.decode(FixtureFiles.text(name))
            val frame = (decoded as WireMessage.V2).frame
            assertTrue(
                "$name 校验违例: ${V2FrameValidator.validate(frame)}",
                V2FrameValidator.validate(frame).isEmpty(),
            )
            val reEncoded = codec.encodeV2(frame)
            assertEquals(frame, (codec.decode(reEncoded) as WireMessage.V2).frame)
        }
    }

    // ---- 会话 hello ----

    @Test
    fun `session-hello 夹具通过校验`() {
        val violations = SessionHelloValidator.validate(FixtureFiles.element("session-hello.valid.json"))
        assertTrue("hello 校验违例: $violations", violations.isEmpty())
    }

    // ---- 配对 ----

    @Test
    fun `pair-request 与 pair-response 夹具通过校验`() {
        assertTrue(
            PairRequestValidator.validate(FixtureFiles.element("pair-request.valid.json")).isEmpty(),
        )
        assertTrue(
            PairResponseValidator.validate(FixtureFiles.element("pair-response.valid.json")).isEmpty(),
        )
    }

    // ---- 结果回执载荷 ----

    @Test
    fun `exec-callback 夹具通过解码与校验`() {
        val decoded = codec.decode(FixtureFiles.text("command-envelope.exec-result.json"))
        val frame = (decoded as WireMessage.V2).frame
        assertEquals(MessageTypes.SOCKET_RAW_CONFIRM, frame.socketType)
        assertTrue(ExecCallbackValidator.validate(frame.list[0]).isEmpty())
    }

    // ---- 反例必须被拒 ----

    @Test
    fun `反例 encrypt-mismatch 必须被拒`() {
        val negative = FixtureFiles.text("negative/command-envelope.encrypt-mismatch.json")
        // encryptType=2 却携带明文 list、缺 encryptedList
        val decoded = codec.decode(negative)
        val frame = (decoded as WireMessage.V2).frame
        val violations = V2FrameValidator.validate(frame)
        assertTrue("encrypt-mismatch 必须有违例", violations.isNotEmpty())
        assertTrue(
            "必须命中 encryptedList required 与 list 并存规则",
            violations.any { it.field == "encryptedList" || it.field == "list" },
        )
        // 全零 hmac 形式上符合 hex pattern（格式步不拦）；鉴权步（HMAC 复算）必拒——见 FrameVerifierTest
    }

    @Test
    fun `反例 missing-hmac 必须被拒`() {
        try {
            val negative = FixtureFiles.text("negative/command-envelope.missing-hmac.json")
            codec.decode(negative)
            fail("缺 hmac 的帧必须解码失败")
        } catch (e: EnvelopeDecodeException) {
            assertTrue(e.message!!.contains("hmac"))
        }
    }

    @Test
    fun `反例 pair-request invalid-code 必须被拒`() {
        val negative = FixtureFiles.element("negative/pair-request.invalid-code.json")
        val violations = PairRequestValidator.validate(negative)
        assertTrue("非法配对码必须有违例", violations.isNotEmpty())
        assertTrue(violations.any { it.field == "pairingCode" && it.rule.startsWith("pattern") })
    }

    // ---- 夹具清单完整性（文件名冻结契约，spec §7）----

    @Test
    fun `夹具清单与冻结清单一致`() {
        val names = FixtureFiles.allFixtureNames()
        // T3.4（spec v1.2.0 变更记录）新增控制帧夹具：session-hello-ack.go-actual +
        // control-payload.901~904 五件（正例）/ 五件反例
        // 2026-11-05 契约冻结车道（docs/16 §指令字段契约冻结）登记数据文件：
        // admin-command-examples.json（管理台目录 52 条 example 跨语言交换夹具，
        // 单一事实源=server catalog.go；Go TestCatalogExamplesFixtureInSync 锁同步、
        // Kotlin CatalogExampleContractTest 消费——非 wire 信封夹具，不入 schema 校验）。
        val expected = setOf(
            "admin-command-examples.json",
            "command-envelope.encrypted.json",
            "command-envelope.exec-result.json",
            "command-envelope.heartbeat.json",
            "command-envelope.plain.json",
            "control-payload.901-rotate.json",
            "control-payload.902-revoked.json",
            "control-payload.903-resync.json",
            "control-payload.904-replay.json",
            "control-payload.904-request.json",
            "exec-callback.success.json",
            "hmac-vector.json",
            "pair-request.valid.json",
            "pair-response.valid.json",
            "session-hello-ack.go-actual.json",
            "session-hello.valid.json",
            "wire-v1-envelope.command-203.json",
            "wire-v1-envelope.heartbeat.json",
            "wire-v1-envelope.report-101.json",
        )
        assertEquals("fixtures 目录 json 清单变化（冻结接口，变更须提 AUD）", expected, names.toSet())
        val negativeNames = FixtureFiles.fixture("negative").listFiles()!!
            .filter { it.extension == "json" }.map { it.name }.toSet()
        assertEquals(
            setOf(
                "command-envelope.encrypt-mismatch.json",
                "command-envelope.missing-hmac.json",
                "control-payload.901-bad-token.json",
                "control-payload.903-extra-field.json",
                "control-payload.904-empty.json",
                "pair-request.invalid-code.json",
                "session-hello-ack.bad-frozen.json",
                "session-hello-ack.envelope-wrapped.json",
            ),
            negativeNames,
        )
    }

    // ---- exec-callback.success 独立夹具（v1 载荷形态）----

    @Test
    fun `exec-callback success 载荷校验`() {
        val element = FixtureFiles.element("exec-callback.success.json").jsonObject
        assertEquals(203, (element["type"] as kotlinx.serialization.json.JsonPrimitive).content.toInt())
        assertEquals(0, (element["errorCode"] as kotlinx.serialization.json.JsonPrimitive).content.toInt())
        assertTrue((element["successList"] as JsonArray).size == 1)
    }
}
