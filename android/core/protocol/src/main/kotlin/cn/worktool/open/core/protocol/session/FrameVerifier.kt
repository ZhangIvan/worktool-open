/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.ControlFrame
import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.EnvelopeDecodeException
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WireMessage
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.validation.HelloAckValidator
import cn.worktool.open.core.protocol.validation.V2FrameValidator
import kotlinx.serialization.json.JsonElement

/**
 * v2 入向帧校验器——按 protocol/spec.md §5.2.4 冻结顺序执行：
 *
 * `格式校验 → tokenVersion → ts(±30s) → nonce(5min) → hmac → seq/eventId →（若 encryptType=2）解密`
 *
 * 任一步失败即拒帧；鉴权类失败（4401/4402/4403/4404/4405）同时要求关闭会话；
 * 解密失败 = 帧整体拒绝（spec §5.3，不关闭会话）。
 * 篡改 seq/ts/nonce → HMAC 失配（C-9）或对应窗口校验失败（任务卡 T2.2 必测项）。
 *
 * T3.4（spec §5.7）：**独立控制帧**（hello_ack；type 为字符串 + meta 对象，不承载于信封）
 * 按同顺序校验——W-M3R 拒收根因（按信封 schema 4400 拒 hello_ack → seq_gap 连锁）在此修复。
 */
class FrameVerifier(
    private val codec: EnvelopeCodec,
    private val deviceToken: () -> ByteArray,
    private val tokenVersion: () -> Int,
    /** encryptType=2 解密所需会话密钥；null 时收到加密帧按格式拒帧。 */
    private val crypto: SessionCrypto?,
    private val clock: () -> Long,
    nonceWindow: NonceWindow = NonceWindow(clock = clock),
    sequencer: SessionSequencer = SessionSequencer(),
    dedup: EventDeduplicator = EventDeduplicator(clock = clock),
    private val clockSkewMillis: Long = 30_000,
) {

    private val receiveNonces = nonceWindow
    private val receiveSequencer = sequencer
    private val eventDedup = dedup

    sealed interface Verification {
        /**
         * @param duplicateReAck true = 重复 eventId 且同 payloadDigest：幂等回执原确认，
         *   不得重复副作用（spec §5.4.3）。
         */
        data class Accepted(
            val frame: V2Frame,
            val payload: JsonElement,
            val duplicateReAck: Boolean,
        ) : Verification

        /**
         * 独立控制帧通过全链校验（T3.4，spec §5.7）：会话内即 hello_ack
         * （服务端出向首帧；seq == resumeFromSeq == 期望首帧）。
         */
        data class AcceptedControl(val control: ControlFrame) : Verification

        data class Rejected(
            /** WS 关闭码（spec §5.2.5）；null = 仅丢帧不关闭（解密失败）。 */
            val closeCode: Int?,
            val rule: String,
            val detail: String,
        ) : Verification {
            val shouldClose: Boolean get() = closeCode != null
        }
    }

    /**
     * 重连复位（spec §5.4.2 v1.2.0 按方向澄清）：端侧接收计数器以持久确认点
     * [lastReceivedSeq] 预置（期望服务端首帧 = lastReceivedSeq+1，即 hello_ack.seq）。
     */
    fun resetForReconnect(lastReceivedSeq: Long = 0L) {
        receiveNonces.prune(clock())
        receiveSequencer.resetForReconnect(lastReceivedSeq)
    }

    fun verify(text: String): Verification {
        // 1. 格式校验（schema 等价）→ 4400
        val decoded = try {
            codec.decode(text)
        } catch (e: EnvelopeDecodeException) {
            return Verification.Rejected(4400, "format", e.message ?: "解码失败")
        }
        // 独立控制帧（hello_ack；spec §5.7.1）：同 §5.2.4 顺序的控制帧链
        (decoded as? WireMessage.Control)?.let { return verifyHelloAck(it.control) }
        val frame = (decoded as? WireMessage.V2)?.frame
            ?: return Verification.Rejected(4400, "format", "v2 会话内收到非 v2 帧（meta 须为对象）")
        val violations = V2FrameValidator.validate(frame)
        if (violations.isNotEmpty()) {
            return Verification.Rejected(4400, "format", violations.joinToString("; "))
        }

        // 2. tokenVersion → 4404（→ §5.5 重协商或强制重配对）
        val expectedVersion = tokenVersion()
        if (frame.meta.tokenVersion != expectedVersion) {
            return Verification.Rejected(
                4404, "token_version",
                "期望 $expectedVersion，收到 ${frame.meta.tokenVersion}",
            )
        }

        // 3. ts ±30s → 4403
        val now = clock()
        val skew = kotlin.math.abs(frame.meta.ts - now)
        if (skew > clockSkewMillis) {
            return Verification.Rejected(4403, "clock_skew", "|ts-now|=${skew}ms > ${clockSkewMillis}ms")
        }

        // 4. nonce 5min 窗口 → 4402
        if (!receiveNonces.checkAndRecord(frame.meta.nonce, now)) {
            return Verification.Rejected(4402, "nonce_replay", frame.meta.nonce)
        }

        // 5. HMAC（C-9 输入式）→ 4401
        val payloadDigest = if (frame.encryptType == 2) {
            FrameAuth.payloadDigestOfEncrypted(
                frame.encryptedList
                    ?: return Verification.Rejected(4400, "format", "encryptType=2 缺 encryptedList"),
            )
        } else {
            FrameAuth.payloadDigestOfList(codec.listElement(frame.list))
        }
        if (!FrameAuth.verifyHmac(
                deviceToken(), frame.meta.hmac,
                frame.meta.commandId, frame.meta.eventId, frame.meta.seq,
                frame.meta.ts, frame.meta.nonce, payloadDigest,
            )
        ) {
            return Verification.Rejected(4401, "hmac_mismatch", "HMAC 失配（含 seq 防篡改，C-9）")
        }

        // 6. eventId 去重 + seq 严格递增 → 4405（触发 903 重新同步）
        when (val verdict = eventDedup.observe(frame.meta.eventId, payloadDigest, now)) {
            is EventDeduplicator.Verdict.Conflict ->
                return Verification.Rejected(
                    4405, "event_id_conflict",
                    "重复 eventId 异内容: ${verdict.observed} != ${verdict.declared}",
                )
            is EventDeduplicator.Verdict.DuplicateSameDigest ->
                return payloadOrRejected(frame)?.let {
                    Verification.Accepted(frame, it, duplicateReAck = true)
                } ?: Verification.Rejected(null, "decrypt_failed", "解密失败（帧整体拒绝）")
            EventDeduplicator.Verdict.First -> {
                val seqVerdict = receiveSequencer.checkAndAdvance(frame.meta.seq)
                when (seqVerdict) {
                    SessionSequencer.Verdict.Expected -> Unit
                    is SessionSequencer.Verdict.Stale ->
                        return Verification.Rejected(
                            4405, "seq_stale",
                            "seq=${seqVerdict.seq} ≤ lastReceived=${seqVerdict.lastReceived}",
                        )
                    is SessionSequencer.Verdict.Gap ->
                        return Verification.Rejected(
                            4405, "seq_gap",
                            "seq=${seqVerdict.seq}，期望 ${seqVerdict.expected}",
                        )
                }
            }
        }

        // 7. 解密（encryptType=2）：失败 = 帧整体拒绝（不关闭会话）
        return payloadOrRejected(frame)?.let {
            Verification.Accepted(frame, it, duplicateReAck = false)
        } ?: Verification.Rejected(null, "decrypt_failed", "解密失败（帧整体拒绝）")
    }

    /** 取载荷：encryptType=0 → list 树；=2 → 解密（失败返回 null = 帧整体拒绝）。 */
    private fun payloadOrRejected(frame: V2Frame): JsonElement? {
        if (frame.encryptType != 2) return codec.listElement(frame.list)
        val crypto = crypto ?: return null
        return try {
            crypto.decryptList(frame.encryptedList ?: return null, frame.meta.seq, frame.meta.eventId, frame.meta.ts)
        } catch (_: Exception) {
            null
        }
    }

    /**
     * hello_ack 校验链（spec §5.2.4 同顺序 + §5.7.2 不变式；T3.4 冻结形态）：
     * 格式（session-hello-ack schema 等价）→ tokenVersion → ts → nonce → hmac
     * （空载荷、commandId 空串）→ seq（== resumeFromSeq == 期望首帧 lastReceivedSeq+1）→ eventId 去重。
     * 会话内收到其他独立控制帧（如服务端发 hello）= 4400。
     */
    private fun verifyHelloAck(control: ControlFrame): Verification {
        if (control.type != "hello_ack") {
            return Verification.Rejected(4400, "format", "会话内收到非 hello_ack 独立控制帧: ${control.type}")
        }
        // 1. 格式（schema 等价）
        val root = kotlinx.serialization.json.buildJsonObject {
            put("type", kotlinx.serialization.json.JsonPrimitive(control.type))
            control.fields.forEach { (k, v) -> put(k, v) }
            put("meta", codec.metaElement(control.meta))
        }
        val violations = HelloAckValidator.validate(root)
        if (violations.isNotEmpty()) {
            return Verification.Rejected(4400, "format", violations.joinToString("; "))
        }

        // 2. tokenVersion → 4404
        val expectedVersion = tokenVersion()
        if (control.meta.tokenVersion != expectedVersion) {
            return Verification.Rejected(
                4404, "token_version",
                "期望 $expectedVersion，收到 ${control.meta.tokenVersion}",
            )
        }

        // 3. ts ±30s → 4403
        val now = clock()
        val skew = kotlin.math.abs(control.meta.ts - now)
        if (skew > clockSkewMillis) {
            return Verification.Rejected(4403, "clock_skew", "|ts-now|=${skew}ms > ${clockSkewMillis}ms")
        }

        // 4. nonce 5min 窗口 → 4402
        if (!receiveNonces.checkAndRecord(control.meta.nonce, now)) {
            return Verification.Rejected(4402, "nonce_replay", control.meta.nonce)
        }

        // 5. HMAC（空载荷帧：payloadDigest = SHA-256(空字节串)；commandId 空串输入）→ 4401
        val digest = FrameAuth.payloadDigestOfList(null)
        if (!FrameAuth.verifyHmac(
                deviceToken(), control.meta.hmac,
                "", control.meta.eventId, control.meta.seq,
                control.meta.ts, control.meta.nonce, digest,
            )
        ) {
            return Verification.Rejected(4401, "hmac_mismatch", "hello_ack HMAC 验签失败")
        }

        // 6. seq：hello_ack.seq == resumeFromSeq（schema 不变式已过）== 期望首帧 → 4405
        when (val verdict = receiveSequencer.checkAndAdvance(control.meta.seq)) {
            SessionSequencer.Verdict.Expected -> Unit
            is SessionSequencer.Verdict.Stale ->
                return Verification.Rejected(
                    4405, "seq_stale",
                    "hello_ack seq=${verdict.seq} ≤ lastReceived=${verdict.lastReceived}（期望 ${verdict.lastReceived + 1}）",
                )
            is SessionSequencer.Verdict.Gap ->
                return Verification.Rejected(
                    4405, "seq_gap",
                    "hello_ack seq=${verdict.seq}，期望 ${verdict.expected}（== resumeFromSeq，lastReceivedSeq 预置失步）",
                )
        }
        when (eventDedup.observe(control.meta.eventId, digest, now)) {
            is EventDeduplicator.Verdict.Conflict ->
                return Verification.Rejected(
                    4405, "event_id_conflict",
                    "hello_ack eventId 冲突: ${control.meta.eventId}",
                )
            EventDeduplicator.Verdict.First, is EventDeduplicator.Verdict.DuplicateSameDigest -> Unit
        }
        // hello_ack 重复（同 eventId 同摘要，如重连窗口内重放）：幂等接受，不重复副作用语义由上层
        return Verification.AcceptedControl(control)
    }
}
