/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context

/**
 * 会话运行时单例持有（BP-1/SP-1 + BP-5/SP-2 接线）。
 *
 * 为什么必须单例：改造前 MainActivity 每次 onCreate 自建 SessionRuntime、onDestroy
 * 自停 —— 会话生命周期被 Activity 绑死（BP-1：重启 App 离线；BP-5：熄屏/后台被杀）。
 * 改造后会话归 [SessionForegroundService] 所有，三处启动方（配对成功回调 /
 * MainActivity 自动恢复 / BootCompletedReceiver / 服务自愈）必须操作**同一**运行时
 * 实例，否则状态卡观测面与执行回路脱钩。
 *
 * 装配点：首次 [get] 时绑定 [AndroidSessionForegroundController]（SP-2 前台服务钩子）。
 */
object SessionRuntimeHolder {

    @Volatile
    private var instance: SessionRuntime? = null

    /** 取（或惰性创建）进程级唯一会话运行时。 */
    fun get(context: Context): SessionRuntime {
        val app = context.applicationContext
        return instance ?: synchronized(this) {
            instance ?: SessionRuntime(app).also { r ->
                r.foreground = AndroidSessionForegroundController(app)
                instance = r
            }
        }
    }

    /** 已创建则返回（不创建）；服务/接收器自愈路径判断是否需要全新装配。 */
    fun peek(): SessionRuntime? = instance
}

/** [SessionRuntime.Foreground] 的 Android 实现：拉起/停止 [SessionForegroundService]。 */
class AndroidSessionForegroundController(private val context: Context) : SessionRuntime.Foreground {

    override fun startRequested() = SessionForegroundService.start(context.applicationContext)

    override fun stopRequested() = SessionForegroundService.stop(context.applicationContext)
}
