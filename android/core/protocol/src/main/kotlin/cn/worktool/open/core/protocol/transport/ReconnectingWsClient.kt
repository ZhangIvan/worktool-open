/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

/**
 * 断线重连 WS 客户端状态机（protocol/spec.md §3）：
 * - 指数退避重连（v1 5s→600s / v2 5s→120s 可配，[BackoffPolicy]）；
 * - onOpen 立即发建连首帧（v1 `{"td":<ms>}` / v2 hello）；
 * - 周期心跳（v1 5s 裸 `{"type":11}` / v2 15s 鉴权帧，由上层注入帧文本）；
 * - v2 45s 不活跃关闭重连；v1 心跳发送失败即重连（上游还原）。
 *
 * 时钟/调度全部可注入，状态机在 JVM 上确定性可测。
 */
class ReconnectingWsClient(
    private val transportFactory: WsTransport.Factory,
    private val urlProvider: () -> String,
    /** onOpen 后立即发送的建连首帧（v1 td / v2 hello）；null = 不发。 */
    private val greeting: (() -> String)? = null,
    private val onServerMessage: (String) -> Unit,
    /** 周期心跳帧文本；null = 不心跳。 */
    private val heartbeatFrame: (() -> String)? = null,
    private val heartbeatIntervalMillis: Long,
    private val inactivityTimeoutMillis: Long? = null,
    private val backoff: BackoffPolicy,
    private val scheduler: Scheduler,
    private val clock: () -> Long,
    private val onConnected: () -> Unit = {},
    private val onEvent: ((Event) -> Unit)? = null,
) {

    sealed interface Event {
        object Connected : Event
        data class Disconnected(val cause: String) : Event
        data class ReconnectScheduled(val delayMillis: Long) : Event
        data class HeartbeatSent(val at: Long) : Event
        data class HeartbeatSendFailed(val at: Long) : Event
        data class InactivityTimeout(val idleMillis: Long) : Event
    }

    private enum class State { IDLE, CONNECTING, OPEN, WAITING_RETRY }

    private val lock = Any()
    private var state = State.IDLE
    private var stopped = true
    private var transport: WsTransport? = null
    private var lastServerActivity = 0L

    /**
     * 最近一次「发送成功」时刻（H2/性能审计 2026-11-04）：TCP 写成功=内核级链路
     * 活性证据（半开连接最终会写失败/读失败暴露）。不活跃看门狗以此与
     * [lastServerActivity] 取 max——服务端对心跳无回包（spec：心跳无应答语义）
     * 不再误杀健康会话，消灭每分钟重连循环；死链仍由「写失败→handleDown」与
     * 「服务端读 deadline 关闭→读失败」双路径检测。
     */
    private var lastSendOk = 0L
    private var heartbeatHandle: Cancellable? = null
    private var timeoutHandle: Cancellable? = null

    /**
     * 当前重连序号（docs/16 采纳项「离线态重连文案」）：断连后进入第 N 次重连，
     * onOpen 成功即清零。0 = 从未重连/当前已连上。UI 据此显示「重连中（第 N 次）」。
     */
    private var reconnectAttemptCount = 0

    /** 重连计数透出（线程安全；状态卡观测面）。 */
    fun reconnectAttempts(): Int = synchronized(lock) { reconnectAttemptCount }

    fun start() {
        synchronized(lock) {
            if (!stopped) return
            stopped = false
        }
        connect()
    }

    fun stop() {
        synchronized(lock) {
            stopped = true
            state = State.IDLE
            cancelTimers()
            transport?.close(1000, "client-stop")
            transport = null
        }
    }

    /** 发送文本帧（未连接返回 false，由 AtLeastOnce 队列保持待发）。 */
    fun send(text: String): Boolean = transport?.send(text) ?: false

    private fun connect() {
        synchronized(lock) {
            if (stopped) return
            state = State.CONNECTING
        }
        val t = transportFactory.create()
        transport = t
        t.connect(urlProvider(), object : TransportListener {
            override fun onOpen() = handleOpen()
            override fun onMessage(text: String) = handleMessage(text)
            override fun onFailure(t: Throwable?) = handleDown("failure: ${t?.message ?: "?"}")
            override fun onClosed(code: Int, reason: String) = handleDown("closed: $code $reason")
        })
    }

    private fun handleOpen() {
        synchronized(lock) {
            if (stopped) return
            state = State.OPEN
            backoff.reset()
            reconnectAttemptCount = 0
            lastServerActivity = clock()
            lastSendOk = clock() // greeting 发送成功亦为活性证据（无心跳帧客户端的看门狗基准）
        }
        greeting?.invoke()?.let { transport?.send(it) }
        onConnected()
        onEvent?.invoke(Event.Connected)
        scheduleHeartbeat()
        scheduleInactivityCheck()
    }

    private fun handleMessage(text: String) {
        synchronized(lock) { lastServerActivity = clock() }
        onServerMessage(text)
    }

    private fun handleDown(cause: String) {
        val delay: Long
        synchronized(lock) {
            if (stopped || state == State.WAITING_RETRY) return
            state = State.WAITING_RETRY
            cancelTimers()
            transport = null
            reconnectAttemptCount += 1
            delay = backoff.nextDelayMillis()
        }
        onEvent?.invoke(Event.Disconnected(cause))
        onEvent?.invoke(Event.ReconnectScheduled(delay))
        scheduler.schedule(delay) { connect() }
    }

    private fun scheduleHeartbeat() {
        val frame = heartbeatFrame ?: return
        synchronized(lock) {
            if (state != State.OPEN) return
            heartbeatHandle = scheduler.schedule(heartbeatIntervalMillis) {
                val now = clock()
                val ok = transport?.send(frame.invoke()) ?: false
                if (ok) {
                    synchronized(lock) { lastSendOk = clock() }
                    onEvent?.invoke(Event.HeartbeatSent(now))
                    scheduleHeartbeat() // 链式下一拍
                } else {
                    // v1 语义：发送失败即重连（spec §3）；v2 由不活跃窗口兜底
                    onEvent?.invoke(Event.HeartbeatSendFailed(now))
                    handleDown("heartbeat-send-failed")
                }
            }
        }
    }

    private fun scheduleInactivityCheck() {
        val timeout = inactivityTimeoutMillis ?: return
        synchronized(lock) {
            if (state != State.OPEN) return
            timeoutHandle = scheduler.schedule(heartbeatIntervalMillis) {
                val idle = clock() - maxOf(lastServerActivity, lastSendOk)
                if (idle > timeout) {
                    onEvent?.invoke(Event.InactivityTimeout(idle))
                    transport?.close(4400, "inactivity-${timeout}ms")
                    handleDown("inactivity-${idle}ms")
                } else {
                    scheduleInactivityCheck()
                }
            }
        }
    }

    private fun cancelTimers() {
        heartbeatHandle?.cancel()
        heartbeatHandle = null
        timeoutHandle?.cancel()
        timeoutHandle = null
    }
}
