/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore

/**
 * 开机自启（BP-1/SP-1 ③）：BOOT_COMPLETED → 已存凭据 + 可用 server 地址 → 直接
 * [SessionRuntime.start]（配对过的设备开机即在线，无 UI 路径）。
 *
 * - manifest：exported=false + RECEIVE_BOOT_COMPLETED（见 AndroidManifest 注释）；
 * - BOOT_COMPLETED 属 API 31+ 前台服务后台启动限制的豁免广播，[startForegroundService]
 *   合法拉起（由 SessionRuntime.foreground 钩子自动触发）；
 * - ⚠ 前瞻风险（CR R1）：Android 15（targetSdk ≥ 35）起 BOOT_COMPLETED receiver 不再
 *   允许拉起 dataSync 型前台服务（ForegroundServiceStartNotAllowedException）——本
 *   receiver → SessionRuntime.start → dataSync 前台化的自愈链届时整条失效。升级
 *   targetSdk 前置项：boot 路径改 WorkManager 调度恢复或服务迁 specialUse 型
 *   （详见 SessionForegroundService 类注），并真机回归 boot 场景；
 * - 判定复用 [SessionAutoStart.decide]（与主界面/服务自愈同一口径）；FBE 设备上
 *   BOOT_COMPLETED 在用户解锁后广播，Keystore 凭据/SharedPreferences 均可读。
 * - 非本 Receiver 的其他 intent（如 QUICKBOOT_POWERON）不消费（如实窄口径，不猜）。
 */
class BootCompletedReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val app = context.applicationContext
        val runtime = SessionRuntimeHolder.get(app)
        val creds = AndroidKeystoreCredentialStore(app).load() ?: return
        val baseUrl = AppSettings(app).serverAddress
        if (SessionAutoStart.decide(runtime.isRunning(), creds, baseUrl) ==
            SessionAutoStart.Decision.START
        ) {
            runtime.start(creds, baseUrl!!)
        }
    }
}
