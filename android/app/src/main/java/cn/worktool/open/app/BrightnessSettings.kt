/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.content.SharedPreferences
import cn.worktool.open.core.task.BrightnessConfigStore

/**
 * FR-55 亮度配置 + 持久化（[BrightnessConfigStore] 生产实现）。
 *
 * 独立 SharedPreferences `auto_brightness`（与 APK k2/h.java:30-38 同 SP 文件名/键位：
 * `enabled`/`delay_seconds`/`applied`/`original_brightness`/`original_mode`；
 * `dim_percent`/`window_start_hour`/`window_end_hour` 为我方增强键——docs/01 FR-55
 * 「亮度阈值与时段可配置」）。
 *
 * 默认值勘误口径（docs/01 FR-55，2026-11-03 APK 实证）：600s 无交互 / 约 20% 亮度 /
 * applied 残留位启动恢复（BrightnessManager.recoverOnStartup 消费）。
 */
class BrightnessSettings(context: Context) : BrightnessConfigStore {

    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    override var enabled: Boolean
        get() = prefs.getBoolean(K_ENABLED, DEFAULT_ENABLED)
        set(value) = prefs.edit().putBoolean(K_ENABLED, value).apply()

    /** 无交互降级阈值（秒，1..86400；钳位在 BrightnessManager.delayMillis 统一执行）。 */
    override var delaySeconds: Int
        get() = prefs.getInt(K_DELAY_SECONDS, DEFAULT_DELAY_SECONDS)
        set(value) = prefs.edit().putInt(K_DELAY_SECONDS, value).apply()

    /** 降级目标亮度百分比（1..99，默认 20 = 约 20% 亮度）。 */
    override var dimPercent: Int
        get() = prefs.getInt(K_DIM_PERCENT, DEFAULT_DIM_PERCENT)
        set(value) = prefs.edit().putInt(K_DIM_PERCENT, value).apply()

    /** 生效时段起点（0..23；-1 = 不限时段）。 */
    override var windowStartHour: Int
        get() = prefs.getInt(K_WINDOW_START, NO_WINDOW)
        set(value) = prefs.edit().putInt(K_WINDOW_START, value).apply()

    /** 生效时段终点（0..23；-1 = 不限时段）。 */
    override var windowEndHour: Int
        get() = prefs.getInt(K_WINDOW_END, NO_WINDOW)
        set(value) = prefs.edit().putInt(K_WINDOW_END, value).apply()

    /** 降亮度态持久化位（崩溃残留恢复依据，APK MyApplication.java:122-137 同键）。 */
    override var applied: Boolean
        get() = prefs.getBoolean(K_APPLIED, false)
        set(value) = prefs.edit().putBoolean(K_APPLIED, value).apply()

    /** 降亮度前的系统亮度留档（original_brightness；-1 = 无留档）。 */
    override var originalBrightness: Int?
        get() = prefs.getInt(K_ORIGINAL_BRIGHTNESS, NO_VALUE).takeIf { it != NO_VALUE }
        set(value) = prefs.edit().putInt(K_ORIGINAL_BRIGHTNESS, value ?: NO_VALUE).apply()

    /** 降亮度前的亮度模式留档（original_mode；-1 = 无留档）。 */
    override var originalMode: Int?
        get() = prefs.getInt(K_ORIGINAL_MODE, NO_VALUE).takeIf { it != NO_VALUE }
        set(value) = prefs.edit().putInt(K_ORIGINAL_MODE, value ?: NO_VALUE).apply()

    private companion object {
        const val PREFS = "auto_brightness"
        const val K_ENABLED = "enabled"
        const val K_DELAY_SECONDS = "delay_seconds"
        const val K_DIM_PERCENT = "dim_percent"
        const val K_WINDOW_START = "window_start_hour"
        const val K_WINDOW_END = "window_end_hour"
        const val K_APPLIED = "applied"
        const val K_ORIGINAL_BRIGHTNESS = "original_brightness"
        const val K_ORIGINAL_MODE = "original_mode"

        const val NO_WINDOW = -1
        const val NO_VALUE = -1

        /** 勘误默认值（APK k2/h.java 同口径）。 */
        const val DEFAULT_ENABLED = true
        const val DEFAULT_DELAY_SECONDS = 600
        const val DEFAULT_DIM_PERCENT = 20
    }
}
