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

    private companion object {
        const val PREFS = "worktool_app_settings"
        const val K_SERVER = "server_address"
        const val K_LOG = "log_enabled"
        const val K_DRY_RUN = "dry_run_enabled"
    }
}
