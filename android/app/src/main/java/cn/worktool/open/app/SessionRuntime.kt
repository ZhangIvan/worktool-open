/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.transport.Scheduler
import cn.worktool.open.core.protocol.transport.TransportListener
import cn.worktool.open.core.protocol.transport.WsTransport
import cn.worktool.open.core.protocol.android.SqliteJournalStore
import cn.worktool.open.core.protocol.transport.OkHttpWsTransport
import cn.worktool.open.core.task.PauseManager
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.query.FileInfo
import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.runtime.PauseManager as ExecPauseGate
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * 产品会话运行时（产品冲刺 A 车道）：主界面背后的设备执行回路装配 + 观测面。
 *
 * 装配（镜像 labDebug LabPairingActivity 的 W-M4 装配，产品源集化）：
 * ```
 * V2Session（core:protocol，凭据逐帧 HMAC）
 *   → DeviceSessionLoop（journal=SQLite E09 门禁 + ExecutorRegistry 首批全量
 *     + AccessibilityUiDriver 真机无障碍通道）
 *   → 暂停门 = task-engine PauseManager（FR-52 状态机所有者）经 [TaskEnginePauseGate] 适配
 * ```
 *
 * 观测面（主界面数据源）：
 * - [statusTracker]：连接/心跳时间戳（经 [StatusTrackingTransportFactory] 包装 WS 传输）；
 * - [events]：回路事件环形缓冲（状态 tab 日志）；日志开关开 = 追加落盘 filesDir/logs/；
 * - [snapshotOutcomeByType]：指令列表「上次执行状态」（DeviceSessionLoop.onExecResult）。
 *
 * 会话生命周期（BP-5/SP-2 改造，2026-11-03）：
 * - 单例经 [SessionRuntimeHolder] 装配，生命周期归 [SessionForegroundService]
 *   （foregroundServiceType=dataSync + 常驻通知「机器人运行中」）——start 时经
 *   [foreground] 钩子拉起前台服务、stop 时停，Activity onDestroy **不再**停止会话
 *   （熄屏/后台不被杀 = 持续在线；进程被系统回收期间指令由服务端 queued 暂存 +
 *   重连 flushQueued 补投（docs/05 FR-46 取证链）兜底，不丢）。
 * - 已知边界（如实登记，dodDeferred）：FR-50 悬浮窗控制为后续任务卡；配对时
 *   X25519 临时私钥不持久化（spec §5.1.3 语义：每次配对新生成）→ 重启后的会话为
 *   encryptType 0（TLS + 逐帧 HMAC；spec 允许形态），加密会话 encryptType 2 仅配对
 *   当次有效。持久化加密会话 = 后续任务卡。
 */
class SessionRuntime(
    context: Context,
    private val clock: () -> Long = System::currentTimeMillis,
) {

    /** FR-52 暂停状态机（task-engine 所有者；主界面暂停/恢复按钮直接绑定）。 */
    val pauseManager = PauseManager(clock)

    /** 状态卡数据源（主界面 1s 轮询 snapshot）。 */
    val statusTracker = SessionStatusTracker()

    /** 会话事件 UI 观察口（主线程回调不保证——UI 侧自行 runOnUiThread）。 */
    var onEventLine: ((String) -> Unit)? = null

    /** 902 凭据撤销（外部走强制重配对 → 引导流）。 */
    var onCredentialRevoked: (() -> Unit)? = null

    /**
     * 前台服务拉起/停止钩子（BP-5/SP-2；由 [SessionRuntimeHolder] 装配 Android 实现
     * [AndroidSessionForegroundController]）。接口隔离：SessionRuntime 本体不触碰
     * Service API（可装配出无前台服务的形态），缺省 null = 不拉前台服务。
     */
    interface Foreground {
        /** 会话启动成功后拉起前台服务（常驻通知）。 */
        fun startRequested()

        /** 会话停止后停掉前台服务（撤常驻通知）。 */
        fun stopRequested()
    }

    @Volatile
    var foreground: Foreground? = null

    private val appContext = context.applicationContext
    private val lock = Any()
    private var loop: DeviceSessionLoop? = null
    private val eventBuffer = ArrayDeque<String>()
    private val lastOutcome = LinkedHashMap<Int, CommandCatalog.Row.Outcome>()
    private val timeFmt = SimpleDateFormat("MM-dd HH:mm:ss.SSS", Locale.US)
    private val logFile: File get() = File(File(appContext.filesDir, "logs"), LOG_NAME)

    /** 由设置页写入（AppSettings.logEnabled 同步）。 */
    @Volatile
    var logEnabled: Boolean = false

    /**
     * FR-54 端侧干跑门（主界面快捷操作「干跑开关」写入，AppSettings.dryRunEnabled
     * 同步；注入 DeviceSessionLoop.dryRunGate）——开 = 下发指令只产出预览零副作用。
     */
    @Volatile
    var dryRunEnabled: Boolean = false

    /** 当前会话心跳间隔（start 时取自凭据 heartbeatIntervalSeconds；主界面在线判定口径）。 */
    @Volatile
    var heartbeatIntervalMillis: Long = 15_000

    /** 会话是否在跑（主界面状态卡 not_started 判定）。 */
    fun isRunning(): Boolean = synchronized(lock) { loop != null }

    /**
     * 按已存凭据启动会话（幂等：已在跑 = 先停再起，凭据/地址以参数为准）。
     * @return false = 装配失败（如凭据缺 wsPath 且 sessionId 空——URL 不可构造）。
     */
    fun start(credentials: DeviceCredentials, baseUrl: String): Boolean {
        val wsPath = credentials.wsPath
        if (wsPath.isNullOrBlank() && credentials.sessionId.isNullOrBlank()) {
            appendEvent("凭据缺 wsPath/sessionId，会话不可启动（请重新配对）")
            return false
        }
        stop()
        val registry = ExecutorRegistry.defaultFirstBatch(
            querySink = QueryResultSink { type, result ->
                val summary = when (result) {
                    is QueryResult.GroupList -> "群×${result.groups.size}: ${result.groups.take(3)}"
                    is QueryResult.FriendList -> "好友×${result.friends.size}: ${result.friends.take(3)}"
                    is QueryResult.LocalFile -> "${result.fileName} ${result.sizeBytes}B"
                }
                appendEvent("查询结果 type=$type → $summary")
            },
            files = object : LocalFilePort {
                override fun defaultLogFilePath(): String = logFile.absolutePath
                override fun stat(path: String): FileInfo? =
                    File(path).takeIf { it.isFile }?.let { FileInfo(it.name, it.absolutePath, it.length()) }
            },
        )
        val l = DeviceSessionLoop(
            credentials = credentials,
            baseUrl = baseUrl,
            sessionCrypto = null, // 重启无临时私钥 → encryptType 0（TLS + 逐帧 HMAC，见类注边界）
            journal = SqliteJournalStore(appContext),
            scheduler = Scheduler.shared(),
            clock = clock,
            registry = registry,
            driverProvider = { AccessibilityUiDriver() },
            pauseManager = TaskEnginePauseGate(pauseManager),
            clientVersion = AppInfo.VERSION,
            transportFactory = StatusTrackingTransportFactory(
                delegate = OkHttpWsTransport.Factory(OkHttpWsTransport.defaultClient()),
                tracker = statusTracker,
                clock = clock,
            ),
            onCredentialRevoked = {
                statusTracker.onSessionStopped()
                onCredentialRevoked?.invoke()
            },
            onEvent = { line -> appendEvent(line) },
            onExecResult = { _, commandType, errorCode, success ->
                synchronized(lock) {
                    lastOutcome[commandType] = CommandCatalog.Row.Outcome(success, errorCode, clock())
                    if (lastOutcome.size > 64) { // 有界（指令号空间小，护栏而已）
                        val it = lastOutcome.entries.iterator()
                        it.next(); it.remove()
                    }
                }
            },
            dryRunGate = { dryRunEnabled },
        )
        l.start()
        synchronized(lock) { loop = l }
        statusTracker.onSessionStarted()
        foreground?.startRequested() // SP-2：会话起来 → 前台服务常驻（熄屏/后台不被杀）
        heartbeatIntervalMillis = credentials.heartbeatIntervalSeconds * 1000L
        appendEvent("会话启动 deviceId=${credentials.deviceId} → $baseUrl")
        return true
    }

    fun stop() {
        val l = synchronized(lock) { loop ?: return }
        synchronized(lock) { loop = null }
        foreground?.stopRequested() // SP-2：无会话 → 无前台服务/常驻通知
        runCatching { l.stop() }
        statusTracker.onSessionStopped()
        appendEvent("会话停止")
    }

    /** 指令列表「上次执行状态」快照（type → 最近回执）。 */
    fun snapshotOutcomeByType(): Map<Int, CommandCatalog.Row.Outcome> =
        synchronized(lock) { lastOutcome.toMap() }

    /** 事件环形缓冲快照（最新在末尾）。 */
    fun events(): List<String> = synchronized(lock) { eventBuffer.toList() }

    private fun appendEvent(line: String) {
        val stamped = "[${timeFmt.format(Date(clock()))}] $line"
        synchronized(lock) {
            eventBuffer.addLast(stamped)
            while (eventBuffer.size > EVENT_BUFFER_MAX) eventBuffer.removeFirst()
            if (logEnabled) runCatching {
                logFile.parentFile?.mkdirs()
                logFile.appendText(stamped + "\n")
            }
        }
        onEventLine?.invoke(stamped)
    }

    private companion object {
        const val EVENT_BUFFER_MAX = 200
        const val LOG_NAME = "worktool-session.log"
    }
}

/**
 * task-engine [PauseManager]（FR-52 状态机）→ feature:executor 执行暂停门
 * （[ExecPauseGate]）适配器：执行器在执行前/步间断点查询的只读视图。
 */
class TaskEnginePauseGate(private val delegate: PauseManager) : ExecPauseGate {
    override fun isPaused(): Boolean = delegate.isPaused
    override fun pauseSource(): String? = delegate.pauseSource()
}

/**
 * WS 传输包装（状态卡观测桥）：连接开/断、入向帧、成功写出的出向帧（稳态 = 15s
 * 心跳帧）喂给 [SessionStatusTracker]。零协议语义改动——纯回调转发。
 */
class StatusTrackingTransportFactory(
    private val delegate: WsTransport.Factory,
    private val tracker: SessionStatusTracker,
    private val clock: () -> Long,
) : WsTransport.Factory {

    override fun create(): WsTransport = TrackingTransport(delegate.create())

    private inner class TrackingTransport(private val inner: WsTransport) : WsTransport {

        override fun connect(url: String, listener: TransportListener) =
            inner.connect(
                url,
                object : TransportListener {
                    override fun onOpen() {
                        tracker.onConnected(clock())
                        listener.onOpen()
                    }

                    override fun onMessage(text: String) {
                        tracker.onFrameReceived(clock())
                        listener.onMessage(text)
                    }

                    override fun onFailure(t: Throwable?) {
                        tracker.onDisconnected(clock())
                        listener.onFailure(t)
                    }

                    override fun onClosed(code: Int, reason: String) {
                        tracker.onDisconnected(clock())
                        listener.onClosed(code, reason)
                    }
                },
            )

        override fun send(text: String): Boolean =
            inner.send(text).also { ok -> if (ok) tracker.onFrameSent(clock()) }

        override fun close(code: Int, reason: String) {
            tracker.onDisconnected(clock())
            inner.close(code, reason)
        }
    }
}
