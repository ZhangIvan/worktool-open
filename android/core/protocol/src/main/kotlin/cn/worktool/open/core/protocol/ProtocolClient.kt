/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.delivery.AtLeastOnceQueue
import cn.worktool.open.core.protocol.delivery.JournalStore
import cn.worktool.open.core.protocol.session.FrameBuilder
import cn.worktool.open.core.protocol.session.FrameVerifier
import cn.worktool.open.core.protocol.session.SendSequencer
import cn.worktool.open.core.protocol.session.SessionCrypto
import cn.worktool.open.core.protocol.transport.BackoffPolicy
import cn.worktool.open.core.protocol.transport.OkHttpWsTransport
import cn.worktool.open.core.protocol.transport.ReconnectingWsClient
import cn.worktool.open.core.protocol.transport.Scheduler
import kotlinx.serialization.json.JsonElement
import java.security.PrivateKey

/**
 * core:protocol 对外门面（T2.2）。
 *
 * 两个入口：
 * - [v1Compat]：上游兼容通道 `{host}/webserver/wework/{robotId}`（心跳 5s 裸 `{"type":11}`，
 *   onOpen 发 `{"td":<ms>}`，重连 5s→600s；鉴权薄弱是已知事实，仅迁移用，spec §2）；
 * - [v2Session]：v2 安全会话（配对凭据 + 逐帧 HMAC + AES-256-GCM；心跳 15s / 失联 45s）。
 *
 * 真机联调（D1 RIG 通道）= T2.5 deferred；本层交付 JVM 可测的协议语义。
 */
object ProtocolClient {
    /** v2 客户端能力声明（hello.capabilities）。 */
    data class Capabilities(
        val clientVersion: String,
        val encryptTypeMax: Int = 2,
        val commands: List<Int>,
    )

    /** v1 兼容客户端。 */
    class V1Compat(
        wsUrl: String,
        scheduler: Scheduler,
        clock: () -> Long,
        onMessage: (WireMessage) -> Unit,
        httpClient: okhttp3.OkHttpClient = OkHttpWsTransport.defaultClient(),
        transportFactory: cn.worktool.open.core.protocol.transport.WsTransport.Factory? = null,
    ) {
        private val codec = EnvelopeCodec()
        private val ws = ReconnectingWsClient(
            transportFactory = transportFactory ?: OkHttpWsTransport.Factory(httpClient),
            urlProvider = { wsUrl },
            greeting = { codec.connectionHelloJson(clock()) },
            onServerMessage = { text -> onMessage(codec.decode(text)) },
            heartbeatFrame = { codec.bareHeartbeatJson() },
            heartbeatIntervalMillis = 5_000,
            inactivityTimeoutMillis = null,
            backoff = BackoffPolicy.v1(),
            scheduler = scheduler,
            clock = clock,
        )

        fun start() = ws.start()
        fun stop() = ws.stop()
        fun send(envelope: V1Envelope): Boolean = ws.send(codec.encodeV1(envelope))
    }

    /**
     * v2 安全会话客户端。
     *
     * @param credentials 配对/重协商签发的凭据（tokenVersion 变化由外部重协商后替换实例）
     * @param sessionCrypto encryptType=2 会话密钥（null = 仅明文帧）
     * @param journal AtLeastOnce 出向 journal（SQLite 实现 = core:protocol-android）
     * @param onCommand 已验帧回调（duplicateReAck=true 时幂等回执原确认，不重复副作用）
     * @param onRejected 拒帧回调（shouldClose=true 的鉴权类失败已自动断连；
     *   4404 → 外部按 §5.5 走重协商/强制重配对）
     */
    class V2Session(
        private val credentials: DeviceCredentials,
        private val baseUrl: String,
        sessionCrypto: SessionCrypto?,
        journal: JournalStore?,
        private val scheduler: Scheduler,
        private val clock: () -> Long,
        private val capabilities: Capabilities,
        /** hello.lastEpoch：journal 已应用的最小命令 epoch（端侧拒旧水位）。 */
        private val lastEpoch: () -> Long = { 0L },
        /** hello.lastReceivedSeq：端侧持久确认的最高入向 seq（服务端从此值 +1 补发）。 */
        private val lastReceivedSeq: () -> Long = { 0L },
        onCommand: (V2Frame, JsonElement, duplicateReAck: Boolean) -> Unit,
        onRejected: (FrameVerifier.Verification.Rejected) -> Unit,
        httpClient: okhttp3.OkHttpClient = OkHttpWsTransport.defaultClient(),
        transportFactory: cn.worktool.open.core.protocol.transport.WsTransport.Factory? = null,
    ) {
        private val codec = EnvelopeCodec()
        private val sendSequencer = SendSequencer()
        private val verifier = FrameVerifier(
            codec = codec,
            deviceToken = { credentials.deviceToken },
            tokenVersion = { credentials.tokenVersion },
            crypto = sessionCrypto,
            clock = clock,
        )
        private val builder = FrameBuilder(
            codec = codec,
            deviceToken = { credentials.deviceToken },
            tokenVersion = { credentials.tokenVersion },
            crypto = sessionCrypto,
            clock = clock,
            sequencer = sendSequencer,
        )

        /** 出向 AtLeastOnce 队列（journal 为 null 时退化为直接发送）。 */
        private val queue: AtLeastOnceQueue? = journal?.let {
            AtLeastOnceQueue(it, OutboundSinkWs())
        }

        private val ws = ReconnectingWsClient(
            transportFactory = transportFactory ?: OkHttpWsTransport.Factory(httpClient),
            urlProvider = {
                credentials.wsPath?.let { path -> baseUrl.trimEnd('/') + path }
                    ?: baseUrl.trimEnd('/') + "/api/v2/device-sessions/" + credentials.sessionId
            },
            greeting = {
                builder.buildHello(
                    deviceId = credentials.deviceId,
                    lastEpoch = lastEpoch(),
                    lastReceivedSeq = lastReceivedSeq(),
                    clientVersion = capabilities.clientVersion,
                    encryptTypeMax = capabilities.encryptTypeMax,
                    commands = capabilities.commands,
                )
            },
            onServerMessage = { text -> handleInbound(text, onCommand, onRejected) },
            heartbeatFrame = { builder.buildHeartbeat().text },
            heartbeatIntervalMillis = (credentials.heartbeatIntervalSeconds * 1000).toLong(),
            inactivityTimeoutMillis = (credentials.sessionTimeoutSeconds * 1000).toLong(),
            backoff = BackoffPolicy.v2(),
            scheduler = scheduler,
            clock = clock,
            onConnected = {
                sendSequencer.reset()
                verifier.resetForReconnect()
                queue?.flushPending()
            },
        )

        private inner class OutboundSinkWs : cn.worktool.open.core.protocol.delivery.OutboundSink {
            override fun send(frameText: String): Boolean = ws.send(frameText)
        }

        fun start() = ws.start()
        fun stop() = ws.stop()

        /** 发送上报（socketType=2）——经 AtLeastOnce 队列（先落盘后发送）；eventId 由帧构造器分配。 */
        fun sendReport(vararg beans: WeworkMessageBean, encrypt: Boolean = false): Boolean {
            val built = builder.build(
                socketType = MessageTypes.SOCKET_MESSAGE_LIST,
                apiSend = 0,
                list = beans.toList(),
                encrypt = encrypt,
            )
            val q = queue
            return if (q != null) {
                q.enqueue(built.eventId, built.text, clock())
                true
            } else {
                ws.send(built.text)
            }
        }

        /** 发送指令执行结果回执（socketType=3，回填 commandId）。 */
        fun sendExecResult(commandId: String, vararg beans: WeworkMessageBean): Boolean {
            val built = builder.buildExecResult(commandId, *beans)
            val q = queue
            return if (q != null) {
                q.enqueue(built.eventId, built.text, clock())
                true
            } else {
                ws.send(built.text)
            }
        }

        private fun handleInbound(
            text: String,
            onCommand: (V2Frame, JsonElement, Boolean) -> Unit,
            onRejected: (FrameVerifier.Verification.Rejected) -> Unit,
        ) {
            when (val v = verifier.verify(text)) {
                is FrameVerifier.Verification.Accepted -> onCommand(v.frame, v.payload, v.duplicateReAck)
                is FrameVerifier.Verification.Rejected -> {
                    onRejected(v)
                    if (v.shouldClose) {
                        // 鉴权类失败关闭会话（spec §5.2.4/§5.2.5）；
                        // 4404 的恢复路径（重协商/重配对）由外部凭据管理者执行（§5.5）
                        ws.stop()
                    }
                }
            }
        }
    }

    /**
     * 凭据重协商入口（离线错过轮换 → hello 4404 → 复用签名公钥重协商，spec §5.5.1）。
     * 签名密钥应由调用方从 AndroidKeystore 取出。
     */
    fun renegotiate(
        baseUrl: String,
        deviceId: String,
        signingKey: PrivateKey,
    ): DeviceCredentials {
        val response = cn.worktool.open.core.protocol.pair.PairingClient(baseUrl)
            .renegotiate(deviceId, signingKey)
        return DeviceCredentials(
            deviceId = response.deviceId,
            deviceTokenHex = response.deviceTokenHex,
            tokenVersion = response.tokenVersion,
            sessionId = response.sessionId,
            serverX25519PublicKeyHex = response.serverX25519PublicKeyHex,
            rotationIntervalDays = response.rotationIntervalDays,
            heartbeatIntervalSeconds = response.heartbeatIntervalSeconds,
            sessionTimeoutSeconds = response.sessionTimeoutSeconds,
            wsPath = response.wsPath,
        )
    }
}
