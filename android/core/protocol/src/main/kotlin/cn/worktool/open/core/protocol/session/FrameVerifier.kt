/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.EnvelopeDecodeException
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WireMessage
import cn.worktool.open.core.protocol.crypto.FrameAuth
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

        data class Rejected(
            /** WS 关闭码（spec §5.2.5）；null = 仅丢帧不关闭（解密失败）。 */
            val closeCode: Int?,
            val rule: String,
            val detail: String,
        ) : Verification {
            val shouldClose: Boolean get() = closeCode != null
        }
    }

    fun resetForReconnect() {
        receiveNonces.prune(clock())
        receiveSequencer.resetForReconnect()
    }

    fun verify(text: String): Verification {
        // 1. 格式校验（schema 等价）→ 4400
        val decoded = try {
            codec.decode(text)
        } catch (e: EnvelopeDecodeException) {
            return Verification.Rejected(4400, "format", e.message ?: "解码失败")
        }
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
}
