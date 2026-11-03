/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.os.Build
import android.provider.Settings
import cn.worktool.open.core.task.BrightnessManager
import cn.worktool.open.core.task.BrightnessSettingsWriter

/**
 * FR-55 系统亮度写入器（[BrightnessSettingsWriter] 生产实现）。
 *
 * 两条路径（docs/01 FR-55「写回系统亮度设置」；APK 实证 MyApplication.java:122-137
 * 同为 `Settings.System.putInt/getInt("screen_brightness"/"screen_brightness_mode")`）：
 * - **API<30**：`Settings.System.putInt/getInt` 直写，写返回值即成败（全局 Settings 面）；
 * - **API≥30**：写入后**回读校验**——R+ 亮度按用户隔离且 ROM 侧 float 化，int 写可能
 *   被自动亮度/浮点亮度静默改写，回读一致才算成功（等价 @hide `putIntForUser(…,
 *   USER_CURRENT)` 的可见性语义；公共 SDK 无 forUser 面，Hidden API 限制下不反射）。
 *
 * 权限面：写 SCREEN_BRIGHTNESS 需要 WRITE_SETTINGS（设置页「修改系统设置」授权），
 * 未授权时写入直接返回 false（BrightnessManager 按失败处理，不置持久化位——零半态）。
 * 读无需权限，设置项缺失（SettingNotFoundException）返回 null。
 *
 * JVM 可测性：本类仅是薄壳（值/成败透传），状态机与幂等逻辑全部在纯 JVM 的
 * BrightnessManager（Fake 注入测试）；本类不在 JVM 单测面内。
 */
class SystemBrightnessWriter(context: Context) : BrightnessSettingsWriter {

    private val appContext: Context = context.applicationContext
    private val resolver = appContext.contentResolver

    override fun readBrightness(): Int? =
        getInt(Settings.System.SCREEN_BRIGHTNESS)?.takeIf { it in 0..BRIGHTNESS_MAX }

    override fun readBrightnessMode(): Int? =
        getInt(Settings.System.SCREEN_BRIGHTNESS_MODE)?.takeIf {
            it == BrightnessManager.BRIGHTNESS_MODE_MANUAL ||
                it == BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC
        }

    override fun writeBrightness(value: Int): Boolean {
        if (!canWrite()) return false
        val clamped = value.coerceIn(1, BRIGHTNESS_MAX) // 不写 0（避免全黑无法交互恢复）
        return putInt(Settings.System.SCREEN_BRIGHTNESS, clamped)
    }

    override fun writeBrightnessMode(value: Int): Boolean {
        if (!canWrite()) return false
        return putInt(Settings.System.SCREEN_BRIGHTNESS_MODE, value)
    }

    /** WRITE_SETTINGS 授权检查（API 23+ 起需「修改系统设置」用户授权）。 */
    private fun canWrite(): Boolean = Settings.System.canWrite(appContext)

    private fun getInt(key: String): Int? = try {
        Settings.System.getInt(resolver, key)
    } catch (_: Settings.SettingNotFoundException) {
        null
    }

    private fun putInt(key: String, value: Int): Boolean {
        val ok = Settings.System.putInt(resolver, key, value)
        // API≥30：回读校验（见类注两路径说明）；API<30：直写返回值即可信
        return if (Build.VERSION.SDK_INT >= 30) ok && getInt(key) == value else ok
    }

    companion object {
        /** 亮度物理上限（0..255 口径；读取超界按无效丢弃）。 */
        const val BRIGHTNESS_MAX: Int = 255
    }
}
