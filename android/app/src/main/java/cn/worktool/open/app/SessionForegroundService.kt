/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore

/**
 * 会话前台服务（BP-5/SP-2）：foregroundServiceType=dataSync + 常驻通知
 * 「机器人运行中」（点击回主界面）。熄屏/后台不再被系统回收 = 持续在线（DoD：
 * 熄屏 30 分钟后会话仍 alive——真机验证归 SP-5 回归，见 docs/14）。
 *
 * 生命周期归 SessionRuntime：SessionRuntime.start（经 SessionRuntimeHolder 装配的
 * [AndroidSessionForegroundController]）→ [start]；SessionRuntime.stop → [stop]。
 *
 * START_STICKY 自愈：进程被系统强杀后系统重启本服务 → [onStartCommand] 经
 * [SessionAutoStart.decide] 用已存凭据自愈重启会话（持续在线兜底）。
 *
 * 权限（manifest）：FOREGROUND_SERVICE + FOREGROUND_SERVICE_DATA_SYNC（API 34
 * target 必需）+ POST_NOTIFICATIONS（API 33+ 运行时请求经进程级共享门
 * [NotificationPermissionGate]：引导页 + 主界面「会话在跑」两入口，本进程只弹一次；
 * 未授权时 startForeground 仍生效，仅通知不显示，服务不受影响）。
 *
 * ⚠ 前瞻风险（CR R1，升级 targetSdk ≥ 35 = Android 15 前必读）：Android 15 起禁止
 * BOOT_COMPLETED receiver 拉起 dataSync 型前台服务（直接抛
 * ForegroundServiceStartNotAllowedException；camera/mediaPlayback/phoneCall 等同样受限，
 * 见 developer.android.com/about/versions/15/behavior-changes-15 与
 * .../changes/foreground-service-types）。本类 dataSync 型 + BootCompletedReceiver 开机
 * 恢复链在 targetSdk≥35 后会整条运行时失效。升级前置项：保活型服务迁 specialUse 型或
 * WorkManager expedited、boot 路径改调度恢复，并真机回归 boot 场景后才可动 targetSdk。
 *
 * 上游对照：闭源版 PlayNotifyService（媒体投影前台服务）保活；我方 dataSync 型
 * 常驻 + 自愈重启，无媒体投影（G12 零 hook / SR-10 组件透明命名）。
 */
class SessionForegroundService : Service() {

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        ensureChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForegroundCompat()
        selfHealSessionIfNeeded()
        return START_STICKY
    }

    /** dataSync 型前台化（API 29+ 显式带类型；26~28 无类型概念）。 */
    private fun startForegroundCompat() {
        val notification = buildNotification()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    /** START_STICKY 重启（进程被杀后）自愈：凭据仍在 → 重新拉起会话执行回路。 */
    private fun selfHealSessionIfNeeded() {
        val runtime = SessionRuntimeHolder.get(applicationContext)
        val creds = AndroidKeystoreCredentialStore(applicationContext).load() ?: return
        val baseUrl = AppSettings(applicationContext).serverAddress
        if (SessionAutoStart.decide(runtime.isRunning(), creds, baseUrl) ==
            SessionAutoStart.Decision.START
        ) {
            runtime.start(creds, baseUrl!!)
        }
    }

    /** 常驻通知「机器人运行中」：点击回 MainActivity；ongoing = 不可下滑清除。 */
    private fun buildNotification(): Notification {
        val openMain = PendingIntent.getActivity(
            this,
            0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_fg_session)
            .setContentTitle(getString(R.string.fg_notification_title))
            .setContentText(getString(R.string.fg_notification_text))
            .setOngoing(true)
            .setContentIntent(openMain)
            .build()
    }

    private fun ensureChannel() {
        val manager = getSystemService(NotificationManager::class.java)
        if (manager.getNotificationChannel(CHANNEL_ID) == null) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                getString(R.string.fg_channel_name),
                NotificationManager.IMPORTANCE_LOW, // 常驻不扰（无声音/横幅）
            ).apply { description = getString(R.string.fg_channel_desc) }
            manager.createNotificationChannel(channel)
        }
    }

    companion object {
        const val CHANNEL_ID = "worktool_session_foreground"
        const val NOTIFICATION_ID = 1

        /** startForegroundService（minSdk 26 恒可用；前台服务 5s 内须 startForeground——onStartCommand 首行即做）。 */
        fun start(context: Context) {
            context.startForegroundService(Intent(context, SessionForegroundService::class.java))
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, SessionForegroundService::class.java))
        }
    }
}
