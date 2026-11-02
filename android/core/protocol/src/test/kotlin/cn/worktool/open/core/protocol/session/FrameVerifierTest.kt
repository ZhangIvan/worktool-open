/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.FixtureFiles
import cn.worktool.open.core.protocol.FrameMeta
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * v2 入向帧校验管线测试（protocol/spec.md §5.2.4 冻结顺序 + §5.4 投递语义）：
 * 篡改 seq/ts/nonce 必败（任务卡 T2.2 必测项）、校验顺序、eventId 去重、加密帧全链路。
 */
class FrameVerifierTest {

    private val codec = EnvelopeCodec()
    private val token = Hex.decode("5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4")
    private var now = 1_793_613_600_000L
    private val clock = { now }
    private var nonceCounter = 0

    /** 每帧独立 nonce（默认值；重放/篡改用例显式传参）。 */
    private fun nextNonce(): String {
        nonceCounter += 1
        return "5c1f9a7b3e2d4c6f8a0b2d4e6f8a%04x".format(nonceCounter)
    }

    private fun newVerifier(crypto: SessionCrypto? = null): FrameVerifier = FrameVerifier(
        codec = codec,
        deviceToken = { token },
        tokenVersion = { 1 },
        crypto = crypto,
        clock = clock,
    )

    /** 手工构造帧（完全控制字段，可任意重算/伪造 hmac）。 */
    private fun frameText(
        seq: Long,
        ts: Long = now,
        nonce: String = nextNonce(),
        eventId: String = java.util.UUID.randomUUID().toString(),
        commandId: String = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f",
        tokenVersion: Int = 1,
        list: List<WeworkMessageBean> = listOf(
            WeworkMessageBean(type = 203, roomType = 1, extras = mapOf("targetRoom" to Json.parseToJsonElement("\"外部群测试群\""))),
        ),
        hmacOf: (String) -> String = { digest -> hmac(seq, ts, nonce, eventId, commandId, digest) },
    ): String {
        check(nonce.length == 32) { "nonce 必须 32 字符" }
        val digest = FrameAuth.payloadDigestOfList(codec.listElement(list))
        val meta = FrameMeta(
            ver = 2, ts = ts, nonce = nonce, hmac = hmacOf(digest), seq = seq,
            eventId = eventId, commandId = commandId, tokenVersion = tokenVersion,
        )
        val frame = V2Frame(
            socketType = 2,
            messageId = "2026-11-02#10:00:05#aabbccdd-eeff-4a55-8b8b-1c2d3e4f5064",
            meta = meta, apiSend = 1, list = list, listPresent = true, encryptType = 0,
        )
        return codec.encodeV2(frame)
    }

    private fun hmac(seq: Long, ts: Long, nonce: String, eventId: String, commandId: String, digest: String): String =
        FrameAuth.hmacHex(token, commandId, eventId, seq, ts, nonce, digest)

    // ---- 基线：合法帧通过 ----

    @Test
    fun `合法明文帧通过全链路`() {
        val v = newVerifier()
        val result = v.verify(frameText(seq = 1))
        assertTrue("基线帧必须通过: $result", result is FrameVerifier.Verification.Accepted)
        val accepted = result as FrameVerifier.Verification.Accepted
        assertFalse(accepted.duplicateReAck)
        assertEquals(203, accepted.frame.list[0].type)
    }

    // ---- 任务卡必测：篡改 seq / ts / nonce 必败 ----

    @Test
    fun `篡改 seq 必败-4401`() {
        val v = newVerifier()
        // 声明 hmac 按 seq=1 计算，帧内 seq 篡改为 2（未重算 hmac）
        // 帧内 seq=2，但 hmac 按 seq=1 计算（nonce/eventId/commandId 均一致 → 差异只来自 seq）
        val nonceForTamper = nextNonce()
        val tampered = frameText(
            seq = 2,
            nonce = nonceForTamper,
            hmacOf = { digest -> hmac(1, now, nonceForTamper, "7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a", "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", digest) },
        )
        val result = v.verify(tampered)
        assertTrue(result is FrameVerifier.Verification.Rejected)
        val rejected = result as FrameVerifier.Verification.Rejected
        assertEquals(4401, rejected.closeCode)
        assertTrue(rejected.shouldClose)
    }

    @Test
    fun `篡改 ts 必败-4403（ts 窗口先于 hmac 判定）`() {
        val v = newVerifier()
        // ts 超窗（即使 hmac 按新 ts 正确重算）→ 4403
        val stale = frameText(seq = 1, ts = now - 31_000)
        val result = v.verify(stale)
        assertTrue(result is FrameVerifier.Verification.Rejected)
        assertEquals(4403, (result as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `时钟容差边界 30s`() {
        // 29s 通过 / 30s 通过（=30 未超） / 31s 拒（>30）
        assertTrue(newVerifier().verify(frameText(seq = 1, ts = now - 29_000)) is FrameVerifier.Verification.Accepted)
        assertTrue(newVerifier().verify(frameText(seq = 1, ts = now - 30_000)) is FrameVerifier.Verification.Accepted)
        val rejected = newVerifier().verify(frameText(seq = 1, ts = now - 31_000))
        assertEquals(4403, (rejected as FrameVerifier.Verification.Rejected).closeCode)
        val rejected2 = newVerifier().verify(frameText(seq = 1, ts = now + 31_000))
        assertEquals(4403, (rejected2 as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `篡改 nonce 必败-nonce 重放 4402 或 hmac 4401`() {
        // ① hmac 按 nonce A 计算，帧内 nonce 篡改为 B → 4401
        val v = newVerifier()
        val nonceA = "aaaaaaaabbbbbbbbccccccccdddd0001"
        val nonceB = "aaaaaaaabbbbbbbbccccccccdddd0002"
        val forgedNonceHmac = frameText(seq = 1, nonce = nonceB, hmacOf = { d ->
            hmac(1, now, nonceA, "7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a", "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f", d)
        })
        assertEquals(4401, (v.verify(forgedNonceHmac) as FrameVerifier.Verification.Rejected).closeCode)

        // ② 相同 nonce 合法重发（hmac 均正确）→ 第二次 4402 nonce 重放
        val verifier2 = newVerifier()
        val frameA = frameText(seq = 1, nonce = "22222222222222222222222222222222", eventId = "11111111-1111-4111-8111-111111111111")
        assertTrue(verifier2.verify(frameA) is FrameVerifier.Verification.Accepted)
        // 用同一 nonce 但不同 eventId（新 seq=2）→ nonce 窗口命中 → 4402
        val frameB = frameText(seq = 2, nonce = "22222222222222222222222222222222", eventId = "22222222-2222-4222-8222-222222222222")
        assertEquals(4402, (verifier2.verify(frameB) as FrameVerifier.Verification.Rejected).closeCode)
    }

    // ---- 校验顺序（§5.2.4）----

    @Test
    fun `格式校验先于一切-4400`() {
        val v = newVerifier()
        // 非 JSON / 非对象
        assertEquals(4400, (v.verify("not-json") as FrameVerifier.Verification.Rejected).closeCode)
        // v1 帧进入 v2 会话 → 4400
        val v1Text = FixtureFiles.text("wire-v1-envelope.command-203.json")
        assertEquals(4400, (v.verify(v1Text) as FrameVerifier.Verification.Rejected).closeCode)
        // pattern 违例（nonce 非 hex）且 ts 超窗 → 仍报 4400（格式在前）
        val bad = frameText(seq = 1, nonce = "NOTHEXNOTHEXNOTHEXNOTHEXNOTHEX00", ts = now - 999_999)
        assertEquals(4400, (v.verify(bad) as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `tokenVersion 先于 ts-4404`() {
        val v = newVerifier()
        // 版本错 + ts 超窗 → 4404（顺序：tokenVersion 在 ts 前）
        val wrongVersion = frameText(seq = 1, tokenVersion = 2, ts = now - 999_999)
        assertEquals(4404, (v.verify(wrongVersion) as FrameVerifier.Verification.Rejected).closeCode)
    }

    @Test
    fun `ts 先于 nonce 与 hmac`() {
        val v = newVerifier()
        // ts 超窗 + hmac 全错 → 4403（ts 先于 hmac）
        val staleBadHmac = frameText(seq = 1, ts = now - 999_999, hmacOf = { "0000000000000000000000000000000000000000000000000000000000000000" })
        assertEquals(4403, (v.verify(staleBadHmac) as FrameVerifier.Verification.Rejected).closeCode)
    }

    // ---- seq 严格递增（§5.4.2）----

    @Test
    fun `seq 跳号与回退 4405`() {
        val v = newVerifier()
        assertTrue(v.verify(frameText(seq = 1)) is FrameVerifier.Verification.Accepted)
        // 跳号 3（缺 2）→ 4405
        assertEquals("seq_gap", (v.verify(frameText(seq = 3)) as FrameVerifier.Verification.Rejected).rule)
        // 回退到 1 → 4405
        assertEquals("seq_stale", (v.verify(frameText(seq = 1)) as FrameVerifier.Verification.Rejected).rule)
        // 正常 2 → 通过
        assertTrue(v.verify(frameText(seq = 2)) is FrameVerifier.Verification.Accepted)
    }

    // ---- eventId 去重（§5.4.3）----

    @Test
    fun `重复 eventId 同内容幂等回执-异内容冲突 4405`() {
        val v = newVerifier()
        val e1 = "33333333-3333-4333-8333-333333333333"
        assertTrue(v.verify(frameText(seq = 1, eventId = e1)) is FrameVerifier.Verification.Accepted)

        // 同 eventId 同内容、seq 不增（重传形态）→ duplicateReAck（不重复副作用）
        val dup = v.verify(frameText(seq = 1, eventId = e1)) as FrameVerifier.Verification.Accepted
        assertTrue(dup.duplicateReAck)

        // 同 eventId 异内容（改 payload）→ 4405
        val otherPayload = listOf(WeworkMessageBean(type = 204))
        val conflict = v.verify(frameText(seq = 1, eventId = e1, list = otherPayload))
        assertEquals("event_id_conflict", (conflict as FrameVerifier.Verification.Rejected).rule)
    }

    // ---- 加密帧全链路（§5.3）----

    @Test
    fun `加密帧验签后解密-篡改密文拒绝`() {
        val deviceSeed = Hex.decode("302e020100300506032b656e04220420") +
            Hex.decode("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
        val crypto = SessionCrypto.derive(
            deviceEphemeralPrivate = cn.worktool.open.core.protocol.crypto.X25519.decodePrivatePkcs8(deviceSeed),
            serverX25519PublicKeyRaw = Hex.decode("de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f"),
            sessionId = "5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80",
        )

        // 构造加密帧：加密 list → encryptedList → digest → hmac
        val eventId = "8e7f6a5b-4c3d-4e5f-9a0b-2c3d4e5f6a7b"
        val plain = codec.listElement(listOf(WeworkMessageBean(type = 203, roomType = 1)))
        val encrypted = crypto.encryptList(plain, seq = 1, eventId = eventId, ts = now)
        val digest = FrameAuth.payloadDigestOfEncrypted(encrypted)
        val meta = FrameMeta(
            ver = 2, ts = now, nonce = "6d2e0b8c4f3a5d7e9b1c3e5f7a9d0e2f",
            hmac = hmac(1, now, "6d2e0b8c4f3a5d7e9b1c3e5f7a9d0e2f", eventId, "d2c3b4a5-f6e7-4d8c-9a0b-1d2e3f4a5b6c", digest),
            seq = 1, eventId = eventId, commandId = "d2c3b4a5-f6e7-4d8c-9a0b-1d2e3f4a5b6c", tokenVersion = 1,
        )
        val frame = V2Frame(
            socketType = 2, messageId = "2026-11-02#10:00:06#bccddeef-ff00-4a56-8b8b-1c2d3e4f5065",
            meta = meta, apiSend = 1, list = emptyList(), listPresent = false,
            encryptedList = encrypted, encryptType = 2,
        )
        val text = codec.encodeV2(frame)

        val verifier = newVerifier(crypto)
        val accepted = verifier.verify(text) as FrameVerifier.Verification.Accepted
        assertEquals(plain, accepted.payload) // 解密载荷 == 原文

        // 篡改密文 + 新 eventId/nonce/seq=2 + 重算 hmac（模拟已知密文中间人绕开 eventId 冲突/nonce 重放判定）
        // → 验签通过但解密失败 → 帧整体拒绝（不关闭）。同 eventId 异内容会先命中 4405（spec §5.4.3），另测。
        val tamperedChars = encrypted.toCharArray().also { it[0] = if (it[0] == '0') '1' else '0' }
        val tampered = tamperedChars.concatToString()
        val tNonce = "9e8d0c1b2a3f4e5d6c7b8a9f0e1d2c3b"
        val tEventId = "7a6b5c4d-3e2f-4a5b-9c8d-1e2f3a4b5c6d"
        val tDigest = FrameAuth.payloadDigestOfEncrypted(tampered)
        val tMeta = meta.copy(
            nonce = tNonce,
            seq = 2,
            eventId = tEventId,
            hmac = hmac(2, now, tNonce, tEventId, "d2c3b4a5-f6e7-4d8c-9a0b-1d2e3f4a5b6c", tDigest),
        )
        val tFrame = frame.copy(meta = tMeta, encryptedList = tampered)
        val tResult = verifier.verify(codec.encodeV2(tFrame)) as FrameVerifier.Verification.Rejected
        assertEquals("decrypt_failed", tResult.rule)
        assertFalse(tResult.shouldClose)
    }

    // ---- 夹具帧经预推进 sequencer 全通过（与金标准向量闭环）----

    @Test
    fun `夹具帧在预推进 seq 后可整链验签`() {
        val sequencer = SessionSequencer()
        repeat(412) { sequencer.checkAndAdvance(it + 1L) }
        val verifier = FrameVerifier(
            codec = codec, deviceToken = { token }, tokenVersion = { 1 }, crypto = null, clock = clock,
            sequencer = sequencer,
        )
        now = 1_793_613_605_000
        val plain = FixtureFiles.text("command-envelope.plain.json")
        val result = verifier.verify(plain)
        assertTrue("夹具帧必须整链验签通过: $result", result is FrameVerifier.Verification.Accepted)
    }

    @Test
    fun `重连后 seq 计数复位（spec 5-4-2）`() {
        val sequencer = SessionSequencer()
        sequencer.checkAndAdvance(1)
        sequencer.checkAndAdvance(2)
        sequencer.resetForReconnect()
        assertEquals(SessionSequencer.Verdict.Expected, sequencer.checkAndAdvance(1))
    }
}
