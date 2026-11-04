/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.content.SharedPreferences

/**
 * 产品 App 本地设置（产品冲刺 A 车道）：server 地址 + 日志开关。
 * 私有 SharedPreferences（MODE_PRIVATE，不出设备；G3 零遥测口径下无任何上报）。
 * 凭据不存这里——配对凭据经 core:protocol-android AndroidKeystoreCredentialStore
 * 加密落盘（SR-05/G5），本类只存非敏感的用户偏好。
 */
class AppSettings(context: Context) {

    private val prefs: SharedPreferences =
        context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** 配对时输入的 server 基址（wss://host[:port] 规范形态）。 */
    var serverAddress: String?
        get() = prefs.getString(K_SERVER, null)
        set(value) = prefs.edit().putString(K_SERVER, value).apply()

    /** 会话事件日志落盘开关（默认关；开启后 SessionRuntime 事件追加写入本地日志文件）。 */
    var logEnabled: Boolean
        get() = prefs.getBoolean(K_LOG, false)
        set(value) = prefs.edit().putBoolean(K_LOG, value).apply()

    /**
     * FR-54 端侧干跑开关（默认关）：开启后服务端下发的指令只产出「将要做什么」
     * 预览（feature:executor CommandRunner.dryRun，同引擎同路径零副作用），
     * 回执 metaJson 携 deviceDryRun=true 标记（SessionRuntime.dryRunEnabled →
     * DeviceSessionLoop.dryRunGate）。
     */
    var dryRunEnabled: Boolean
        get() = prefs.getBoolean(K_DRY_RUN, false)
        set(value) = prefs.edit().putBoolean(K_DRY_RUN, value).apply()

    /**
     * FR-58 自动关闭企微新消息通知开关（**默认关**——docs/01 FR-58「开关默认关、
     * 可配置」；与 APK autoCloseNewMessageNotice 默认 true 不同：我方为无障碍动作面，
     * 显式开启才会在主功能初始化时导航企微设置页关通知，
     * NotificationSuppressor.enabledProvider 消费本键）。
     */
    var suppressNoticeEnabled: Boolean
        get() = prefs.getBoolean(K_SUPPRESS_NOTICE, false)
        set(value) = prefs.edit().putBoolean(K_SUPPRESS_NOTICE, value).apply()

    /**
     * FR-49 运行日志上报总开关（默认开；FR-40「日志上报默认脱敏、可整体关闭」的
     * 「可关闭」面——关闭后 LogReportPipeline 零采样零发送；SR-06 脱敏强制不可关）。
     */
    var logReportEnabled: Boolean
        get() = prefs.getBoolean(K_LOG_REPORT, true)
        set(value) = prefs.edit().putBoolean(K_LOG_REPORT, value).apply()

    /**
     * U1 运行时段总开关（docs/16；默认关 = 不拦截）。开启后非时段内机器人休眠
     * （PauseManager 联动，FR-52 同级安全语义：在线指令只入队不执行）。
     */
    var runtimeWindowEnabled: Boolean
        get() = prefs.getBoolean(K_RUNTIME_WINDOW_ENABLED, false)
        set(value) = prefs.edit().putBoolean(K_RUNTIME_WINDOW_ENABLED, value).apply()

    /** U1 运行时段起点（当日分钟数，默认 8:00=480；跨零点窗口支持）。 */
    var runtimeWindowStartMinute: Int
        get() = prefs.getInt(K_RUNTIME_WINDOW_START, 8 * 60)
        set(value) = prefs.edit().putInt(K_RUNTIME_WINDOW_START, value).apply()

    /** U1 运行时段终点（当日分钟数，默认 22:00=1320）。 */
    var runtimeWindowEndMinute: Int
        get() = prefs.getInt(K_RUNTIME_WINDOW_END, 22 * 60)
        set(value) = prefs.edit().putInt(K_RUNTIME_WINDOW_END, value).apply()

    /**
     * U2 悬浮窗开关（FR-50 最小集：暂停/恢复两钮；默认关）。
     * 开 = FloatControlService 常驻气泡（需 SYSTEM_ALERT_WINDOW 授权）。
     */
    var floatWindowEnabled: Boolean
        get() = prefs.getBoolean(K_FLOAT_WINDOW, false)
        set(value) = prefs.edit().putBoolean(K_FLOAT_WINDOW, value).apply()

    /**
     * U4 跳过过期指令开关（默认开，docs/16 U4；对齐 APK skipExpiredQueueCommand
     * 默认语义）。关 = 过期指令照常执行（旧行为）。
     */
    var skipExpiredEnabled: Boolean
        get() = prefs.getBoolean(K_SKIP_EXPIRED, true)
        set(value) = prefs.edit().putBoolean(K_SKIP_EXPIRED, value).apply()

    /** U4 过期判定 TTL 小时数（默认 2h；对齐 APK「过期指令小时数」默认值）。 */
    var expiredTtlHours: Int
        get() = prefs.getInt(K_EXPIRED_TTL_HOURS, 2)
        set(value) = prefs.edit().putInt(K_EXPIRED_TTL_HOURS, value).apply()

    /**
     * 采纳项（docs/16 优化-采纳 #12）：执行失败重试次数（默认 0 = 保持引擎
     * 「首错即停」语义；可配 0~5，对齐 APK customOperationRetryCount 可配面）。
     */
    var retryCount: Int
        get() = prefs.getInt(K_RETRY_COUNT, 0)
        set(value) = prefs.edit().putInt(K_RETRY_COUNT, value).apply()

    /**
     * U7 引导「不再询问」三卡各自记忆（docs/16；默认 false）。
     * skip 只影响**启动路由强制**（AppLaunchRoute），不影响卡内实时检测与
     * 主界面状态卡警示（防误关后失明）。
     */
    var guideSkipAccessibility: Boolean
        get() = prefs.getBoolean(K_GUIDE_SKIP_A11Y, false)
        set(value) = prefs.edit().putBoolean(K_GUIDE_SKIP_A11Y, value).apply()

    var guideSkipBattery: Boolean
        get() = prefs.getBoolean(K_GUIDE_SKIP_BATTERY, false)
        set(value) = prefs.edit().putBoolean(K_GUIDE_SKIP_BATTERY, value).apply()

    var guideSkipMiui: Boolean
        get() = prefs.getBoolean(K_GUIDE_SKIP_MIUI, false)
        set(value) = prefs.edit().putBoolean(K_GUIDE_SKIP_MIUI, value).apply()

    // internal（安卓恢复链车道）：SharedPreferencesAppSettingsPersistence 与本类共用
    // 同一 prefs 文件同一键（单一事实源，杜绝双份键字面量漂移）。
    internal companion object {
        const val PREFS = "worktool_app_settings"
        const val K_SERVER = "server_address"
        const val K_LOG = "log_enabled"
        const val K_DRY_RUN = "dry_run_enabled"
        const val K_SUPPRESS_NOTICE = "suppress_notice_enabled"
        const val K_LOG_REPORT = "log_report_enabled"
        const val K_RUNTIME_WINDOW_ENABLED = "runtime_window_enabled"
        const val K_RUNTIME_WINDOW_START = "runtime_window_start_min"
        const val K_RUNTIME_WINDOW_END = "runtime_window_end_min"
        const val K_FLOAT_WINDOW = "float_window_enabled"
        const val K_SKIP_EXPIRED = "skip_expired_enabled"
        const val K_EXPIRED_TTL_HOURS = "expired_ttl_hours"
        const val K_RETRY_COUNT = "retry_count"
        const val K_GUIDE_SKIP_A11Y = "guide_skip_accessibility"
        const val K_GUIDE_SKIP_BATTERY = "guide_skip_battery"
        const val K_GUIDE_SKIP_MIUI = "guide_skip_miui"
    }
}
