/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

/**
 * 机器人状态计算（主界面状态卡数据源，产品冲刺 A 车道；纯 JVM 可测）。
 *
 * 在线判定（ask ⑥口径）：会话已启动 && 连接在开 && 最近心跳 ≤ 心跳间隔
 * （[DeviceCredentials.heartbeatIntervalSeconds]，默认 15s）。
 *
 * 「最后心跳」的观测点（[SessionStatusTracker]）：V2Session 心跳帧 15s 周期经
 * WsTransport.send 写出——端侧无法从 core:protocol 内部读到该时间戳（V2Session
 * 未暴露），故由 app 车道的 [StatusTrackingTransportFactory] 包装传输层记录
 * 「最近一次成功写出的协议帧」。心跳是唯一周期性出向帧（指令/回执为零星帧），
 * 故该时间戳在稳态即心跳时间；如实登记：偶发指令发送会刷新它（偏保守——
 * 只会把状态判得更「在线」，不会误报在线，因为连接开 + ≤15s 有写出即链路活）。
 */
object RobotStatusComputer {

    /** 状态卡渲染快照。 */
    data class RobotStatus(
        val online: Boolean,
        /** 心跳距今毫秒；null = 从未有心跳。 */
        val heartbeatAgeMillis: Long?,
        /** 状态卡文案锚点：online / offline / not_started。 */
        val stateCode: String,
    )

    const val STATE_ONLINE = "online"
    const val STATE_OFFLINE = "offline"
    const val STATE_NOT_STARTED = "not_started"

    fun compute(
        sessionStarted: Boolean,
        connectionOpen: Boolean,
        lastHeartbeatAtMillis: Long?,
        nowMillis: Long,
        heartbeatIntervalMillis: Long,
    ): RobotStatus {
        if (!sessionStarted) return RobotStatus(false, null, STATE_NOT_STARTED)
        val age = lastHeartbeatAtMillis?.let { nowMillis - it }
        val online = connectionOpen && age != null && age >= 0 && age <= heartbeatIntervalMillis
        val state = when {
            online -> STATE_ONLINE
            sessionStarted -> STATE_OFFLINE
            else -> STATE_NOT_STARTED
        }
        return RobotStatus(online, age, state)
    }

    /** 心跳距世人话文案（纯数字，本地渲染）：如 "3s 前"；null → "—"。 */
    fun heartbeatAgeText(ageMillis: Long?): String =
        when {
            ageMillis == null -> "—"
            ageMillis < 0 -> "0s 前"
            ageMillis < 60_000 -> "${ageMillis / 1000}s 前"
            ageMillis < 3_600_000 -> "${ageMillis / 60_000}m 前"
            else -> "${ageMillis / 3_600_000}h 前"
        }

    /**
     * RTT 彩条分档（docs/16 优化-采纳 #8，对齐上游 Ping 彩条阈值）：
     * <100ms 绿 / <999ms 黄 / 其余红；null = 无样本（灰）。
     */
    enum class RttBand { GREEN, YELLOW, RED, UNKNOWN }

    const val RTT_GREEN_MS = 100L
    const val RTT_YELLOW_MS = 999L

    fun rttBand(rttMillis: Long?): RttBand = when {
        rttMillis == null || rttMillis < 0 -> RttBand.UNKNOWN
        rttMillis < RTT_GREEN_MS -> RttBand.GREEN
        rttMillis < RTT_YELLOW_MS -> RttBand.YELLOW
        else -> RttBand.RED
    }
}

/**
 * 会话心跳/连接观测器（线程安全；app 车道对 core:protocol V2Session 的观测桥）。
 * 数据由 [StatusTrackingTransportFactory]（WsTransport 包装）与 SessionRuntime 生命周期
 * 回调写入，主界面 1s 轮询 [snapshot] 渲染状态卡。
 */
class SessionStatusTracker {

    data class Snapshot(
        val sessionStarted: Boolean,
        val connectionOpen: Boolean,
        /** 最近一次成功写出的协议帧（稳态 = 心跳帧）时间戳；null = 尚无。 */
        val lastHeartbeatAtMillis: Long?,
        /** 最近一次收到服务端帧时间戳；null = 尚无。 */
        val lastInboundAtMillis: Long?,
        /** 最近一次连接建立时间戳；null = 尚未连上过。 */
        val connectedAtMillis: Long?,
        /** 最近一次断连时间戳；null = 未断过。 */
        val disconnectedAtMillis: Long?,
        /**
         * 最近一次「写出→收到」往返时延采样（毫秒；采纳项 #8 RTT 彩条数据源）；
         * null = 尚无成对样本。观测口径如实登记：WS 无应用层 ping-pong 回显，
         * 此值 = 最近一次出向帧（稳态=心跳）到下一个入向帧的间隔——**上界近似**
         * （服务端心跳响应/事件推送才推进），只用于彩条分档不承诺精确 RTT。
         */
        val lastRttMillis: Long? = null,
    )

    private val lock = Any()
    private var started = false
    private var open = false
    private var lastHeartbeat: Long? = null
    private var lastInbound: Long? = null
    private var connectedAt: Long? = null
    private var disconnectedAt: Long? = null
    private var lastSentAt: Long? = null
    private var lastRtt: Long? = null

    fun onSessionStarted() = synchronized(lock) { started = true }

    fun onSessionStopped() = synchronized(lock) {
        started = false
        open = false
    }

    fun onConnected(atMillis: Long) = synchronized(lock) {
        open = true
        connectedAt = atMillis
        lastSentAt = null // 新连接：旧出向时间戳不作 RTT 配对
    }

    fun onDisconnected(atMillis: Long) = synchronized(lock) {
        open = false
        disconnectedAt = atMillis
    }

    fun onFrameSent(atMillis: Long) = synchronized(lock) {
        lastHeartbeat = atMillis
        if (lastSentAt == null) lastSentAt = atMillis // 只配对首个出向帧（多帧连发不重复计时）
    }

    fun onFrameReceived(atMillis: Long) = synchronized(lock) {
        lastInbound = atMillis
        val sent = lastSentAt
        if (sent != null && atMillis >= sent) {
            lastRtt = atMillis - sent
            lastSentAt = null // 配对完成，等待下一出向帧
        }
    }

    fun snapshot(): Snapshot = synchronized(lock) {
        Snapshot(started, open, lastHeartbeat, lastInbound, connectedAt, disconnectedAt, lastRtt)
    }
}
