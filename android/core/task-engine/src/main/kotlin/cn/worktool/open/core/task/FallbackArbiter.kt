/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 本地调度兜底仲裁器（docs/02 §5.2：「端侧仅事件触发规则 + 模式A本地 cron（仅当
 * **server 不可达超阈值 或 纯本地模式**时启用，配置 fallbackLocalScheduling，默认关）」）。
 *
 * 事实源分工（02 §5.2 原文）：定时调度属服务端；端侧 cron 是失联兜底，不是常驻调度器——
 * 因此默认关，开启需同时满足：配置允许（fallbackLocalScheduling）+ 不可达时长 ≥ 阈值。
 * 纯本地模式（模式 A，未配置服务端）恒启用。
 *
 * 可达性输入由协议层连接状态回调驱动（app 接线层调 [onServerReachable]/[onServerUnreachable]）。
 */
class FallbackArbiter(
    private val config: LocalModeConfig,
    private val clock: () -> Long,
) {

    private val lock = Any()
    private var reachable = true
    private var unreachableSinceMillis: Long? = null

    /** 协议层连接断开/失联时调用。 */
    fun onServerUnreachable(now: Long = clock()) {
        synchronized(lock) {
            if (reachable) {
                reachable = false
                unreachableSinceMillis = now
            }
        }
    }

    /** 协议层重连成功时调用（不可达计时清零）。 */
    fun onServerReachable(now: Long = clock()) {
        synchronized(lock) {
            reachable = true
            unreachableSinceMillis = null
        }
    }

    /** 当前不可达持续时长（毫秒；可达/从未失联 = 0）。 */
    fun unreachableDurationMillis(now: Long = clock()): Long = synchronized(lock) {
        val since = unreachableSinceMillis ?: return 0
        if (reachable) 0 else (now - since).coerceAtLeast(0)
    }

    /**
     * 本地 cron 是否允许运行：
     * - 纯本地模式（模式 A）→ 恒 true；
     * - 否则须 fallbackLocalScheduling 开启 **且** 当前处于不可达态、时长 ≥ 阈值
     *   （「不可达超阈值」前提是确实发生失联；重连即退出兜底——事实源回到服务端）。
     */
    fun localSchedulingEnabled(pureLocalMode: Boolean, now: Long = clock()): Boolean {
        if (pureLocalMode) return true
        if (!config.fallbackLocalScheduling) return false
        synchronized(lock) {
            if (reachable) return false
            return unreachableDurationMillis(now) >= config.unreachableThresholdMillis
        }
    }
}
