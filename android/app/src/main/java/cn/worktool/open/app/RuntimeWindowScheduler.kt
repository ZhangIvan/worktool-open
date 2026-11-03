/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager
import cn.worktool.open.core.task.RuntimeWindowConfig
import cn.worktool.open.core.task.RuntimeWindowPolicy
import cn.worktool.open.core.task.RuntimeWindowTracker
import cn.worktool.open.core.task.WindowVerdict
import java.util.Calendar

/**
 * 运行时段调度器（docs/16 U1）：每分钟 tick 一次，把 [RuntimeWindowPolicy] 的
 * 边界迁移翻译成 PauseManager 动作（判定核心在 core:task-engine，本类只做联动壳）。
 *
 * 联动语义（FR-52 同级安全语义）：
 * - **出窗（EXIT）** → `pause(reason="runtime-window", actor="runtime-window")`：
 *   休眠期间在线指令经执行器暂停门只入队不执行（DeviceSessionLoop deferredSink →
 *   PauseManager.deferred，恢复时冲刷），与用户手动暂停同一状态机；
 * - **入窗（ENTER）** → `requestResume()+confirmResume()` **仅当暂停来源仍是
 *   runtime-window**——两步确认的「显式确认」= 用户配置运行时段时的预授权
 *   （例外在此登记：时段配置本身即用户对入窗恢复的显式授权，自动化代行两步）。
 *   若用户在窗外手动暂停过（pauseSource ≠ runtime-window），入窗**不**自动恢复
 *   （用户接管优先级最高，docs/02 §5.2）；
 * - **休眠提示**：出窗迁移经 [onSleepNotice] 回调（主界面状态卡非模态提示行
 *   「机器人休眠中」，不弹模态框打断执行——docs/16 U1 口径）。
 *
 * 纯 JVM 可测：tick 核心逻辑为 [evaluateTick]（时钟/分钟注入），Android 侧
 * [startWith] 仅负责 Handler 定时循环（主线程 Looper，进程存活即有效——会话
 * 生命周期归 SessionRuntime/前台服务，与 Activity 无关）。
 */
class RuntimeWindowScheduler(
    private val configProvider: () -> RuntimeWindowConfig,
    private val pauseManager: PauseManager,
    private val minuteOfDay: (nowMillis: Long) -> Int = ::defaultMinuteOfDay,
    private val onSleepNotice: ((inWindow: Boolean) -> Unit)? = null,
    private val onEvent: ((line: String) -> Unit)? = null,
) {

    private val tracker = RuntimeWindowTracker()

    /** 单次评估（调度循环的每分钟调用体；测试直接驱动）。返回本 tick 的迁移。 */
    fun evaluateTick(nowMillis: Long): RuntimeWindowTracker.Transition {
        val config = configProvider()
        val verdict = RuntimeWindowPolicy.verdict(config, minuteOfDay(nowMillis))
        val transition = tracker.tick(verdict)
        when (transition) {
            RuntimeWindowTracker.Transition.EXIT -> {
                // 只从 RUNNING 进入休眠：用户已手动暂停（PAUSED/RESUME_PENDING）时不
                // 覆盖其 pauseSource（用户接管优先，docs/02 §5.2；来源单一事实源）。
                if (pauseManager.state == PauseManager.State.RUNNING) {
                    pauseManager.pause(reason = PAUSE_REASON, actor = ACTOR, now = nowMillis)
                }
                onSleepNotice?.invoke(false)
                onEvent?.invoke("运行时段外（${describe(config)}）→ 机器人休眠（指令只入队不执行）")
            }
            RuntimeWindowTracker.Transition.ENTER -> {
                // 仅恢复「本调度器造成的暂停」；用户手动暂停不被自动覆盖
                if (pauseManager.state != PauseManager.State.RUNNING &&
                    pauseManager.pauseSource() == ACTOR
                ) {
                    pauseManager.requestResume(actor = ACTOR, now = nowMillis)
                    pauseManager.confirmResume(actor = ACTOR, now = nowMillis)
                    onSleepNotice?.invoke(true)
                    onEvent?.invoke("进入运行时段（${describe(config)}）→ 自动恢复执行（时段配置预授权）")
                } else {
                    onEvent?.invoke("进入运行时段，但暂停来源=${pauseManager.pauseSource() ?: "?"}（用户接管）→ 不自动恢复")
                }
            }
            RuntimeWindowTracker.Transition.NONE -> Unit
        }
        return transition
    }

    /** 配置变更后的基线重置（避免用旧窗口态误判迁移；下一次 tick 重新建立基线）。 */
    fun rebase(nowMillis: Long) {
        val config = configProvider()
        tracker.reset(RuntimeWindowPolicy.verdict(config, minuteOfDay(nowMillis)))
    }

    /** 当前判定（诊断/状态卡）。 */
    fun currentVerdict(nowMillis: Long): WindowVerdict =
        RuntimeWindowPolicy.verdict(configProvider(), minuteOfDay(nowMillis))

    // ---- Android 定时循环（壳；进程主线程 Handler，无 WakeLock——分钟粒度由
    //      系统调度，进程被回收后由前台服务/START_STICKY 自愈重启恢复 tick）。
    //      Handler 惰性创建：evaluateTick 核心逻辑纯 JVM 可测（不触 Android 框架）。----

    private var mainHandler: android.os.Handler? = null
    private var tickRunnable: Runnable? = null

    @Volatile
    private var running = false

    /** 启动分钟 tick（幂等）。立即评估一次以建立基线（出窗态即刻休眠）。 */
    fun startWith(nowMillis: Long = System.currentTimeMillis()) {
        if (running) return
        running = true
        evaluateTick(nowMillis)
        val handler = android.os.Handler(android.os.Looper.getMainLooper())
        val runnable = object : Runnable {
            override fun run() {
                evaluateTick(System.currentTimeMillis())
                handler.postDelayed(this, TICK_MILLIS)
            }
        }
        mainHandler = handler
        tickRunnable = runnable
        handler.postDelayed(runnable, TICK_MILLIS)
    }

    fun stop() {
        running = false
        tickRunnable?.let { mainHandler?.removeCallbacks(it) }
        tickRunnable = null
        mainHandler = null
    }

    fun isRunning(): Boolean = running

    private fun describe(config: RuntimeWindowConfig): String =
        "${config.startMinuteOfDay / 60}:${"%02d".format(config.startMinuteOfDay % 60)}-" +
            "${config.endMinuteOfDay / 60}:${"%02d".format(config.endMinuteOfDay % 60)}"

    companion object {
        const val PAUSE_REASON = "runtime-window"
        const val ACTOR = "runtime-window"
        const val TICK_MILLIS = 60_000L

        /** epochMillis → 当日分钟数（设备默认时区；纯函数可测）。 */
        fun defaultMinuteOfDay(nowMillis: Long): Int {
            val cal = Calendar.getInstance()
            cal.timeInMillis = nowMillis
            return cal.get(Calendar.HOUR_OF_DAY) * 60 + cal.get(Calendar.MINUTE)
        }
    }
}
