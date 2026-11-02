/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.FrameMeta
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.security.SecureRandom
import java.time.Instant
import java.time.LocalDateTime
import java.time.ZoneId
import java.util.UUID

/**
 * v2 出向帧构造器：分配 eventId/seq/nonce/ts/messageId，按 [FrameAuth] 计算 HMAC
 * （C-9 输入式），可选 AES-256-GCM 加密（encryptType=2）。
 */
class FrameBuilder(
    private val codec: EnvelopeCodec,
    private val deviceToken: () -> ByteArray,
    private val tokenVersion: () -> Int,
    /** null = 仅明文帧（encryptType=0）。 */
    private val crypto: SessionCrypto? = null,
    private val clock: () -> Long,
    /** 出向 seq 分配器（每会话每方向独立；重连 reset）。 */
    private val sequencer: SendSequencer,
    private val random: SecureRandom = SecureRandom(),
) {

    data class BuiltFrame(
        val frame: V2Frame,
        val text: String,
        val eventId: String,
    )

    /** 通用帧构造（心跳 socketType=0 / 上报 socketType=2 / 结果回执 socketType=3）。 */
    fun build(
        socketType: Int,
        commandId: String = "",
        apiSend: Int? = null,
        list: List<WeworkMessageBean> = emptyList(),
        encrypt: Boolean = false,
    ): BuiltFrame {
        val eventId = UUID.randomUUID().toString()
        val seq = sequencer.next()
        val ts = clock()
        val nonce = newNonce()
        val token = deviceToken()

        val listElement = codec.listElement(list)
        val encryptedList: String?
        val payloadDigest: String
        if (encrypt && crypto != null) {
            encryptedList = crypto.encryptList(listElement, seq, eventId, ts)
            payloadDigest = FrameAuth.payloadDigestOfEncrypted(encryptedList)
        } else {
            encryptedList = null
            payloadDigest = FrameAuth.payloadDigestOfList(listElement)
        }
        val hmac = FrameAuth.hmacHex(token, commandId, eventId, seq, ts, nonce, payloadDigest)
        val frame = V2Frame(
            socketType = socketType,
            messageId = codec.newMessageId(LocalDateTime.ofInstant(Instant.ofEpochMilli(ts), ZoneId.systemDefault())),
            meta = FrameMeta(
                ver = 2,
                ts = ts,
                nonce = nonce,
                hmac = hmac,
                seq = seq,
                eventId = eventId,
                commandId = commandId,
                tokenVersion = tokenVersion(),
            ),
            apiSend = apiSend,
            list = if (encryptedList == null) list else emptyList(),
            listPresent = encryptedList == null,
            encryptedList = encryptedList,
            encryptType = if (encryptedList == null) 0 else 2,
        )
        return BuiltFrame(frame, codec.encodeV2(frame), eventId)
    }

    /**
     * 会话 hello 首帧（protocol/spec.md §5.4.1：空载荷帧，seq 恒 1，
     * payloadDigest = SHA-256(空字节串)）。
     */
    fun buildHello(
        deviceId: String,
        lastEpoch: Long,
        lastReceivedSeq: Long,
        clientVersion: String,
        encryptTypeMax: Int,
        commands: List<Int>,
    ): String {
        val eventId = UUID.randomUUID().toString()
        val seq = sequencer.next()
        val ts = clock()
        val nonce = newNonce()
        val digest = FrameAuth.payloadDigestOfList(null)
        val hmac = FrameAuth.hmacHex(deviceToken(), "", eventId, seq, ts, nonce, digest)
        return buildJsonObject {
            put("type", "hello")
            put("deviceId", deviceId)
            put("tokenVersion", tokenVersion())
            put("lastEpoch", lastEpoch)
            put("lastReceivedSeq", lastReceivedSeq)
            put("capabilities", buildJsonObject {
                put("clientVersion", clientVersion)
                put("encryptTypeMax", encryptTypeMax)
                put(
                    "commands",
                    kotlinx.serialization.json.JsonArray(commands.map { JsonPrimitive(it) }),
                )
            })
            put("meta", buildJsonObject {
                put("ver", 2)
                put("ts", ts)
                put("nonce", nonce)
                put("hmac", hmac)
                put("seq", seq)
                put("eventId", eventId)
                put("commandId", "")
                put("tokenVersion", tokenVersion())
            })
        }.let { codec.json.encodeToString(kotlinx.serialization.json.JsonElement.serializer(), it) }
    }

    /** 心跳帧（socketType=0，空载荷）。 */
    fun buildHeartbeat(): BuiltFrame = build(socketType = MessageTypes.SOCKET_HEART_BEAT, apiSend = 0)

    /** 指令执行结果回执（socketType=3，回填 commandId）。 */
    fun buildExecResult(commandId: String, vararg payload: WeworkMessageBean): BuiltFrame =
        build(socketType = MessageTypes.SOCKET_RAW_CONFIRM, commandId = commandId, apiSend = 1, list = payload.toList())

    private fun newNonce(): String {
        val bytes = ByteArray(16)
        random.nextBytes(bytes)
        return Hex.encode(bytes)
    }
}
