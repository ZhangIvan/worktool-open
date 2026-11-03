/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import cn.worktool.open.core.protocol.delivery.AtLeastOnceQueue
import cn.worktool.open.core.protocol.delivery.JournalStore
import cn.worktool.open.core.protocol.ControlFrame
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
        /** hello_ack 通过全链校验后的回调（T3.4，spec §5.7：会话建立信号；resumeFromSeq/serverTime 可读）。 */
        onHelloAck: ((ControlFrame) -> Unit)? = null,
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
            AtLeastOnceQueue(it, OutboundSinkWs(), refreshForResend = ::rebuildForResend)
        }

        /**
         * 一次性 wsPath 消耗标记（2026-11-03 重连拨死 URL 修复）：
         * 配对签发的 wsPath 绑定单次 sessionId——首连后即失效（服务端会话结束/超时后
         * 旧路径 404）。true = 已用 wsPath 完成过至少一次连接 → urlProvider 改拨
         * 会话重建入口 /api/v2/device-sessions（服务端按持久凭据重开 deviceId 会话）。
         */
        @Volatile
        private var wsPathUsedAndExpired = false

        private val ws = ReconnectingWsClient(
            transportFactory = transportFactory ?: OkHttpWsTransport.Factory(httpClient),
            urlProvider = {
                // 2026-11-03 重连拨死 URL 修复（会话 2 根因，docs/12 事故记录）：
                // 配对时签发的 wsPath 绑定**一次性 sessionId**——服务端会话过期/45s 失联
                // 关流后旧路径 404，重连永远拨死 URL → 设备永久离线 → 旧绕行=每迭代重配对
                //（应用↔企微高频切换，风控暴露）。修复：重连改拨**会话重建入口**
                // `/api/v2/device-sessions`（服务端按持久凭据复活/重开同一 deviceId 会话），
                // 首连仍优先用配对 wsPath（零额外 RTT）。
                val wsPath = credentials.wsPath
                when {
                    wsPath != null && !wsPathUsedAndExpired -> baseUrl.trimEnd('/') + wsPath
                    else -> baseUrl.trimEnd('/') + "/api/v2/device-sessions"
                }
            },
            greeting = {
                // 出向计数器在构造 hello **前**复位（spec §5.4.1/§5.4.2：hello seq 恒 1，
                // 后续帧自 2 连续 +1；服务端每连接从 1 起算消费 seq）。此前复位点在
                // onConnected（hello 之后）——首连 hello 之后的帧会重发 seq=1、重连时
                // hello 会带着旧计数（>1），两种形态都会被网关 4405 拒收（CR T34-01）。
                sendSequencer.reset()
                builder.buildHello(
                    deviceId = credentials.deviceId,
                    lastEpoch = lastEpoch(),
                    lastReceivedSeq = lastReceivedSeq(),
                    clientVersion = capabilities.clientVersion,
                    encryptTypeMax = capabilities.encryptTypeMax,
                    commands = capabilities.commands,
                )
            },
            onServerMessage = { text -> handleInbound(text, onCommand, onRejected, onHelloAck) },
            heartbeatFrame = { builder.buildHeartbeat().text },
            heartbeatIntervalMillis = (credentials.heartbeatIntervalSeconds * 1000).toLong(),
            inactivityTimeoutMillis = (credentials.sessionTimeoutSeconds * 1000).toLong(),
            backoff = BackoffPolicy.v2(),
            scheduler = scheduler,
            clock = clock,
            onConnected = {
                // 出向计数复位已移至 greeting 构造处（hello=1 后帧连续 +1，见上）。
                // spec §5.4.2 v1.2.0：端侧接收计数器以持久确认点预置（期望服务端
                // 首帧 = lastReceivedSeq+1，即 hello_ack.seq）
                if (!wsPathUsedAndExpired) wsPathUsedAndExpired = true // 首连消耗一次性 wsPath
                verifier.resetForReconnect(lastReceivedSeq())
                queue?.flushPending()
            },
        )

        private inner class OutboundSinkWs : cn.worktool.open.core.protocol.delivery.OutboundSink {
            override fun send(frameText: String): Boolean = ws.send(frameText)
        }

        fun start() = ws.start()
        fun stop() = ws.stop()

        /**
         * 当前重连序号透出（docs/16 采纳项「离线态重连文案」：状态卡显示
         * 「重连中（第 N 次）」；0 = 已连上/从未重连）。经 DeviceSessionLoop →
         * SessionRuntime → 主界面状态卡。
         */
        fun reconnectAttempts(): Int = ws.reconnectAttempts()

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
            rememberResult(commandId, built.eventId, beans.toList())
            val q = queue
            return if (q != null) {
                q.enqueue(built.eventId, built.text, clock())
                true
            } else {
                ws.send(built.text)
            }
        }

        /**
         * 幂等重发结果回执（spec §5.4.3 + 冻结入站规则 §5.2.4，CR T34-01）：
         * 重复下发（重复 eventId 同内容 / journal commandId 去重命中）→ 以
         * **同 eventId + 载荷逐值一致（同 payloadDigest）** 重建回执帧——新 nonce、
         * 当前 ts、下一 seq、重算 HMAC。原样重发旧帧文本必被网关拒收：
         * nonce 5min 窗口（4402）、ts ±30s（4403）、seq 严格 == InSeq+1（4405+903，
         * 幂等重发帧同样消费帧序）。对齐 python 规范客户端重放形态
         * （run_cross_e2e.py send_exec_result 同 eventId 新 seq）。
         *
         * @return false = 无原回执可重放（首次执行前崩溃等；静默，journal 重放兜底）
         */
        fun resendExecResult(commandId: String): Boolean {
            val record = synchronized(lastResults) { lastResults[commandId] } ?: return false
            val built = builder.build(
                socketType = MessageTypes.SOCKET_RAW_CONFIRM,
                commandId = commandId,
                apiSend = 1,
                list = record.beans,
                replayEventId = record.eventId,
            )
            return ws.send(built.text)
        }

        /** 最近一次结果回执（commandId → eventId + 载荷；幂等重建源）。 */
        private data class ResultRecord(val eventId: String, val beans: List<WeworkMessageBean>)

        private val lastResults = LinkedHashMap<String, ResultRecord>()

        private fun rememberResult(commandId: String, eventId: String, beans: List<WeworkMessageBean>) {
            synchronized(lastResults) {
                lastResults[commandId] = ResultRecord(eventId, beans)
                if (lastResults.size > 256) { // 有界（崩溃恢复兜底 = journal 重放）
                    val it = lastResults.entries.iterator()
                    it.next(); it.remove()
                }
            }
        }

        /** 幂等回执源（诊断/测试面）：commandId → 原回执 eventId。 */
        fun lastResultEventId(commandId: String): String? =
            synchronized(lastResults) { lastResults[commandId]?.eventId }

        /**
         * 出向重放帧重建（AtLeastOnce flushPending 路径的 [AtLeastOnceQueue] 刷新钩子）：
         * 解析 journal 固化的帧文本 → 同 eventId + 同载荷（bean 往返保真 → JCS 摘要不变）
         * + 新 nonce/ts/seq + 重算 HMAC。原样重发会被 ts±30s/nonce 窗口/seq 严格递增拒收
         * （>30s 重连窗口必 4403，见 CR T34-01 复核项）。
         *
         * 边界（如实登记）：仅明文帧可重建——加密帧 payloadDigest 覆盖密文字节
         * （spec §5.2.2），而密文经 AAD(seq,ts) 绑定帧序，重加密必变异 digest
         * （同 eventId 异内容 → 4405 冲突）。加密帧在冻结规则下无「同 eventId 刷新信封」
         * 的安全重发形态，保持原样投递（投递失败由重连/对账收敛）——**规格层待议项**，
         * 需 spec 明确加密帧重放口径（如：重放帧换新 eventId + 端侧去重）后方可实装。
         */
        private fun rebuildForResend(eventId: String, frameText: String): String {
            val frame = (codec.decode(frameText) as? WireMessage.V2)?.frame ?: return frameText
            if (frame.meta.eventId != eventId || frame.encryptType == 2) return frameText
            return builder.build(
                socketType = frame.socketType,
                commandId = frame.meta.commandId,
                apiSend = frame.apiSend,
                list = frame.list,
                replayEventId = eventId,
            ).text
        }

        private fun handleInbound(
            text: String,
            onCommand: (V2Frame, JsonElement, Boolean) -> Unit,
            onRejected: (FrameVerifier.Verification.Rejected) -> Unit,
            onHelloAck: ((ControlFrame) -> Unit)?,
        ) {
            when (val v = verifier.verify(text)) {
                is FrameVerifier.Verification.Accepted -> onCommand(v.frame, v.payload, v.duplicateReAck)
                is FrameVerifier.Verification.AcceptedControl -> onHelloAck?.invoke(v.control)
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
