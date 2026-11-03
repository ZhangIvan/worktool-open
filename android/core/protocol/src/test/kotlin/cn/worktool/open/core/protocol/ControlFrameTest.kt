/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.session.FrameVerifier
import cn.worktool.open.core.protocol.session.SessionSequencer
import cn.worktool.open.core.protocol.validation.ControlPayloadValidator
import cn.worktool.open.core.protocol.validation.HelloAckValidator
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * T3.4 控制帧冻结的端侧消费测试（spec §5.7 / §5.4.2 v1.2.0）：
 *
 * - **DoD 用例：接受 Go 网关实际产出的 hello_ack 字节**（fixtures/session-hello-ack.go-actual.json，
 *   2026-11-03 本地实服务采集；token 为该测试实例签发的一次性值，TEST-ONLY）；
 * - W-M3R 拒收根因回归：hello_ack（独立控制帧，无 socketType）不得再按 4400 format 拒；
 * - 重连 seq 预置（服务端出向跨连接自持久确认点连续）：lastReceivedSeq=N → hello_ack seq=N+1；
 * - 901~904 载荷正反例夹具（fixtures/control-payload.* 与 negative/）过 [ControlPayloadValidator]。
 */
class ControlFrameTest {

    private val codec = EnvelopeCodec()

    /** 采集实例签发的一次性 deviceToken（与 fixtures/session-hello-ack.go-actual.json 配对，TEST-ONLY）。 */
    private val captureToken = Hex.decode("a8cd50744fba4c1051d12b8f2055d48ff82169d40acfafc51a4c543813d61113")

    private val captureClock = 1_793_659_572_338L // == 夹具 meta.ts（±30s 窗口内）

    // ---- DoD：接受 Go 实际 hello_ack 字节 ----

    @Test
    fun `接受 Go 实际 hello_ack 字节-DoD`() {
        val verifier = FrameVerifier(
            codec = codec,
            deviceToken = { captureToken },
            tokenVersion = { 1 },
            crypto = null,
            clock = { captureClock },
        )
        val goBytes = FixtureFiles.text("session-hello-ack.go-actual.json")
        val result = verifier.verify(goBytes)
        assertTrue(
            "Go 实际 hello_ack 必须整链接受（W-M3R 拒收根因回归）: $result",
            result is FrameVerifier.Verification.AcceptedControl,
        )
        val ack = (result as FrameVerifier.Verification.AcceptedControl).control
        assertEquals("hello_ack", ack.type)
        assertEquals(1L, ack.longField("resumeFromSeq"))
        assertEquals(15, ack.intField("heartbeatIntervalSeconds"))
        assertEquals(45, ack.intField("sessionTimeoutSeconds"))
        // 会话建立后：下一入向帧期望 seq=2（hello_ack.seq+1 连续，spec §5.4.2）
        val sequencer = SessionSequencer().apply { resetForReconnect(0) }
        assertEquals(SessionSequencer.Verdict.Expected, sequencer.checkAndAdvance(1))
        assertEquals(2L, sequencer.nextExpected)
    }

    @Test
    fun `Go 实际字节解码为独立控制帧-非信封`() {
        val decoded = codec.decode(FixtureFiles.text("session-hello-ack.go-actual.json"))
        assertTrue(decoded is WireMessage.Control)
        val control = (decoded as WireMessage.Control).control
        assertEquals("hello_ack", control.type)
        assertEquals(1_793_659_572_338L, control.longField("serverTime"))
        assertEquals(control.meta.ts, control.longField("serverTime"))
    }

    // ---- 重连 seq 预置（服务端出向跨连接连续，spec §5.4.2 v1.2.0）----

    private fun helloAckText(
        seq: Long,
        ts: Long,
        resumeFromSeq: Long = seq,
        token: ByteArray = captureToken,
        tokenVersion: Int = 1,
        eventId: String = "1d97397d-67ef-4e01-a6ee-400e16e22f00",
        nonce: String = "2e3b98e90a47796c8e86633fddac8d88",
        heartbeat: Int = 15,
        timeout: Int = 45,
        hmacOf: (String) -> String = { digest -> FrameAuth.hmacHex(token, "", eventId, seq, ts, nonce, digest) },
    ): String {
        val digest = FrameAuth.payloadDigestOfList(null) // hello_ack 空载荷帧
        return Json.encodeToString(
            kotlinx.serialization.json.JsonElement.serializer(),
            kotlinx.serialization.json.buildJsonObject {
                put("type", kotlinx.serialization.json.JsonPrimitive("hello_ack"))
                put("serverTime", kotlinx.serialization.json.JsonPrimitive(ts))
                put("resumeFromSeq", kotlinx.serialization.json.JsonPrimitive(resumeFromSeq))
                put("heartbeatIntervalSeconds", kotlinx.serialization.json.JsonPrimitive(heartbeat))
                put("sessionTimeoutSeconds", kotlinx.serialization.json.JsonPrimitive(timeout))
                put(
                    "meta",
                    kotlinx.serialization.json.buildJsonObject {
                        put("ver", kotlinx.serialization.json.JsonPrimitive(2))
                        put("ts", kotlinx.serialization.json.JsonPrimitive(ts))
                        put("nonce", kotlinx.serialization.json.JsonPrimitive(nonce))
                        put("hmac", kotlinx.serialization.json.JsonPrimitive(hmacOf(digest)))
                        put("seq", kotlinx.serialization.json.JsonPrimitive(seq))
                        put("eventId", kotlinx.serialization.json.JsonPrimitive(eventId))
                        put("commandId", kotlinx.serialization.json.JsonPrimitive(""))
                        put("tokenVersion", kotlinx.serialization.json.JsonPrimitive(tokenVersion))
                    },
                )
            },
        )
    }

    @Test
    fun `重连预置 lastReceivedSeq=5 时 hello_ack seq=6 通过-seq=1 拒 4405`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        verifier.resetForReconnect(5)
        // 服务端按持久确认点续发：hello_ack seq=6
        val ok = verifier.verify(helloAckText(seq = 6, ts = captureClock, eventId = "aaaaaaaa-0000-4000-8000-000000000001"))
        assertTrue("seq=6（=5+1）必须通过: $ok", ok is FrameVerifier.Verification.AcceptedControl)

        // 新连接却发 seq=1（计数失步/重放）→ 4405 seq_stale
        val verifier2 = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        verifier2.resetForReconnect(5)
        val stale = verifier2.verify(helloAckText(seq = 1, ts = captureClock, eventId = "aaaaaaaa-0000-4000-8000-000000000002"))
        assertEquals(4405, (stale as FrameVerifier.Verification.Rejected).closeCode)
        assertEquals("seq_stale", stale.rule)
    }

    // ---- 校验链各失败位 ----

    @Test
    fun `hello_ack 冻结值漂移拒 4400-反例夹具`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        val bad = verifier.verify(FixtureFiles.text("negative/session-hello-ack.bad-frozen.json"))
        assertEquals(4400, (bad as FrameVerifier.Verification.Rejected).closeCode)
        assertEquals("format", bad.rule)
    }

    @Test
    fun `hello_ack 信封化残缺形态拒 4400`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        val bad = verifier.verify(FixtureFiles.text("negative/session-hello-ack.envelope-wrapped.json"))
        assertEquals(4400, (bad as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `hello_ack HMAC 失配拒 4401`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        val bad = verifier.verify(
            helloAckText(
                seq = 1, ts = captureClock,
                hmacOf = { _ -> "0000000000000000000000000000000000000000000000000000000000000000" },
            ),
        )
        assertEquals(4401, (bad as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `hello_ack ts 超窗拒 4403-tokenVersion 拒 4404`() {
        val skewVerifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        assertEquals(
            4403,
            (skewVerifier.verify(helloAckText(seq = 1, ts = captureClock - 31_000, eventId = "bbbbbbbb-0000-4000-8000-000000000001")) as FrameVerifier.Verification.Rejected).closeCode,
        )
        val verVerifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 2 },
            crypto = null, clock = { captureClock },
        )
        assertEquals(
            4404,
            (verVerifier.verify(helloAckText(seq = 1, ts = captureClock, tokenVersion = 1, eventId = "bbbbbbbb-0000-4000-8000-000000000002")) as FrameVerifier.Verification.Rejected).closeCode,
        )
    }

    @Test
    fun `会话内收到 hello 独立控制帧拒 4400`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        val hello = FixtureFiles.text("session-hello.valid.json")
        val result = verifier.verify(hello)
        val rejected = result as FrameVerifier.Verification.Rejected
        assertEquals(4400, rejected.closeCode)
        assertTrue(rejected.detail.contains("hello_ack"))
    }

    // ---- hello_ack 之后信封帧 seq 连续 ----

    @Test
    fun `hello_ack 后信封帧 seq 连续`() {
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { captureToken }, tokenVersion = { 1 },
            crypto = null, clock = { captureClock },
        )
        assertTrue(
            verifier.verify(FixtureFiles.text("session-hello-ack.go-actual.json"))
                is FrameVerifier.Verification.AcceptedControl,
        )
        // hello_ack seq=1 已推进 → 下一信封帧期望 seq=2；seq=3 → seq_gap 4405
        val frame = FrameVerifierTestAccess.buildEnvelopeFrame(
            codec = codec, token = captureToken, seq = 3, ts = captureClock,
        )
        val gap = verifier.verify(frame) as FrameVerifier.Verification.Rejected
        assertEquals("seq_gap", gap.rule)
    }

    // ---- 901~904 载荷夹具（schema 镜像校验）----

    @Test
    fun `901~904 正例夹具全部通过`() {
        listOf(
            "control-payload.901-rotate.json",
            "control-payload.902-revoked.json",
            "control-payload.903-resync.json",
            "control-payload.904-replay.json",
            "control-payload.904-request.json",
        ).forEach { name ->
            val tree = Json.parseToJsonElement(FixtureFiles.text(name)).jsonObject
            assertTrue("$name 必须通过: ${ControlPayloadValidator.validate(tree)}", ControlPayloadValidator.validate(tree).isEmpty())
        }
    }

    @Test
    fun `901~904 反例夹具全部被拒`() {
        listOf(
            "control-payload.901-bad-token.json",
            "control-payload.903-extra-field.json",
            "control-payload.904-empty.json",
        ).forEach { name ->
            val tree = Json.parseToJsonElement(FixtureFiles.text("negative/$name")).jsonObject
            assertTrue("$name 必须被拒", ControlPayloadValidator.validate(tree).isNotEmpty())
        }
    }

    @Test
    fun `HelloAckValidator 对 go-actual 夹具零违例-对反例夹具非零`() {
        val good = Json.parseToJsonElement(FixtureFiles.text("session-hello-ack.go-actual.json"))
        assertTrue(HelloAckValidator.validate(good).isEmpty())
        val bad = Json.parseToJsonElement(FixtureFiles.text("negative/session-hello-ack.bad-frozen.json"))
        assertTrue(HelloAckValidator.validate(bad).isNotEmpty())
    }
}

/** 测试辅助：构造一帧合法签名的 socketType=2 明文信封帧（seq/ts 可控）。 */
internal object FrameVerifierTestAccess {
    fun buildEnvelopeFrame(codec: EnvelopeCodec, token: ByteArray, seq: Long, ts: Long): String {
        val list = listOf(WeworkMessageBean(type = 203, roomType = 1, extras = mapOf("targetRoom" to Json.parseToJsonElement("\"外部群测试群\""))))
        val digest = FrameAuth.payloadDigestOfList(codec.listElement(list))
        val nonce = "5c1f9a7b3e2d4c6f8a0b2d4e6f8a0c%02x".format(seq.toInt())
        val eventId = java.util.UUID.randomUUID().toString()
        val hmac = FrameAuth.hmacHex(token, "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", eventId, seq, ts, nonce, digest)
        val frame = V2Frame(
            socketType = 2,
            messageId = "2026-11-03#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
            meta = FrameMeta(ver = 2, ts = ts, nonce = nonce, hmac = hmac, seq = seq, eventId = eventId, commandId = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", tokenVersion = 1),
            apiSend = 1, list = list, listPresent = true, encryptType = 0,
        )
        return codec.encodeV2(frame)
    }
}
