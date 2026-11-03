/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 运行时段（防打扰）判定核心（docs/16 U1；纯 JVM 可测）。
 *
 * 语义（对齐 APK SettingsActivity 双 TimePicker + useTimeEnabled，默认 8:00–22:00）：
 * - 时段以「当日分钟数」表达（0~1439），支持**跨零点窗口**（如 22:00–06:00）；
 * - 边界口径：**含起点、不含终点**（8:00 在窗内，22:00 已出窗）——起点即生效、
 *   终点到点退出，避免 22:00:00 整分二义；
 * - start == end 视为**全天窗口**（等价禁用）——APK 双 TimePicker 同值即不拦截，
 *   本类以 [RuntimeWindowConfig.enabled=false] 表达同一意图更直白，但同值时
 *   仍按全天在窗处理（防御：不因配置退化把机器人锁死）。
 *
 * 迁移事件（[RuntimeWindowTracker.tick]）：调度方每分钟 tick 一次，本类只在
 * **边界迁移**时产出动作（ENTER=入窗恢复 / EXIT=出窗暂停），窗内/窗外稳态返回
 * NONE（幂等，不反复触发 pause/resume）。
 */
object RuntimeWindowPolicy {

    /** 默认窗口 8:00–22:00（分钟数；APK SettingsActivity 默认值对齐）。 */
    const val DEFAULT_START_MINUTE = 8 * 60
    const val DEFAULT_END_MINUTE = 22 * 60

    /** 窗判定：IN_WINDOW / OUT_OF_WINDOW。 */
    fun verdict(config: RuntimeWindowConfig, minuteOfDay: Int): WindowVerdict {
        if (!config.enabled) return WindowVerdict.IN_WINDOW // 未启用 = 不拦截（恒在窗）
        val start = config.startMinuteOfDay.coerceIn(0, MINUTES_PER_DAY - 1)
        val end = config.endMinuteOfDay.coerceIn(0, MINUTES_PER_DAY - 1)
        val inWindow = if (start == end) {
            true // 同值 = 全天（不锁死）
        } else if (start < end) {
            minuteOfDay in start until end
        } else {
            // 跨零点：[start, 24:00) ∪ [0, end)
            minuteOfDay >= start || minuteOfDay < end
        }
        return if (inWindow) WindowVerdict.IN_WINDOW else WindowVerdict.OUT_OF_WINDOW
    }

    const val MINUTES_PER_DAY = 24 * 60
}

enum class WindowVerdict { IN_WINDOW, OUT_OF_WINDOW }

/** 运行时段配置（AppSettings 持久化；分钟数 0~1439）。 */
data class RuntimeWindowConfig(
    val enabled: Boolean = false,
    val startMinuteOfDay: Int = RuntimeWindowPolicy.DEFAULT_START_MINUTE,
    val endMinuteOfDay: Int = RuntimeWindowPolicy.DEFAULT_END_MINUTE,
)

/**
 * 边界迁移跟踪器：逐次喂入 tick 的判定结果，只在 IN↔OUT 迁移时产出动作。
 *
 * 调用方（app 壳层 RuntimeWindowScheduler）每分钟：
 * 1. `verdict(config, minuteOfDay)` → [tick]；
 * 2. [tick] 返回 [Transition.EXIT] → PauseManager.pause(reason="runtime-window")；
 * 3. [Transition.ENTER] → 入窗恢复（两步确认的「显式确认」= 用户配置时段时的预授权，
 *    见 app 侧 RuntimeWindowScheduler KDoc 的例外登记）。
 */
class RuntimeWindowTracker(initial: WindowVerdict = WindowVerdict.IN_WINDOW) {

    enum class Transition { NONE, ENTER, EXIT }

    private var last: WindowVerdict = initial

    /** 最近一次判定（诊断/测试面）。 */
    fun current(): WindowVerdict = last

    fun tick(v: WindowVerdict): Transition {
        val t = when {
            last == WindowVerdict.OUT_OF_WINDOW && v == WindowVerdict.IN_WINDOW -> Transition.ENTER
            last == WindowVerdict.IN_WINDOW && v == WindowVerdict.OUT_OF_WINDOW -> Transition.EXIT
            else -> Transition.NONE
        }
        last = v
        return t
    }

    /** 强制重置（配置变更/会话重启后避免用旧态误判迁移）。 */
    fun reset(v: WindowVerdict = WindowVerdict.IN_WINDOW) {
        last = v
    }
}
