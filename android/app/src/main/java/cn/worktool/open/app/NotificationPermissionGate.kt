/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build

/**
 * API 33+ POST_NOTIFICATIONS 运行时请求门（CR Q4 修复）：判定逻辑纯 JVM 可测
 * （[needsRequest]），Activity 侧胶水经 [requestOnce] 复用。
 *
 * 为什么两个入口共享一个门（缺口背景）：请求原本只在 [SetupGuideActivity]（引导页
 * 仅在「无障碍未授权」时可达）。已开过无障碍/或授权后被系统裁撤通知权限的设备，
 * 主界面路径永不进引导页 → API 33+ 常驻通知（SessionForegroundService「机器人运行中」）
 * 被静默抑制——服务照跑、状态卡仍在线，但用户失去「运行中」可见凭证。现在
 * MainActivity 在「已配对且会话在跑」时补一次请求（见 MainActivity.onStart）。
 *
 * 「拒绝后不重复骚扰」语义保持：`requestedThisProcess` 进程内只请求一次——引导页
 * 与主界面共享同一标记，任一入口请求过（无论用户授予/拒绝）后本进程不再弹；
 * 系统自身对运行时权限弹窗亦有节流。授予结果不影响服务运行（未授权时
 * startForeground 仍生效，仅通知不显示），故不监听 onRequestPermissionsResult。
 */
object NotificationPermissionGate {

    /** 进程内已请求过（含引导页入口；拒绝后不再骚扰）。 */
    @Volatile
    var requestedThisProcess = false
        private set

    /**
     * 纯判定（JVM 单测口径）：API 33+ 且未授予且本进程未请求过才需要请求。
     * API < 33 通知默认开启（无运行时权限），恒 false。
     */
    fun needsRequest(sdkInt: Int, granted: Boolean, alreadyRequested: Boolean): Boolean =
        sdkInt >= 33 && !granted && !alreadyRequested

    /**
     * 幂等请求（Activity 胶水）：满足 [needsRequest] 才弹系统权限对话框；
     * 请求前置 [requestedThisProcess] 标记（防连点/并发重复弹）。
     */
    fun requestOnce(activity: Activity) {
        val granted = if (Build.VERSION.SDK_INT >= 33) {
            activity.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) ==
                PackageManager.PERMISSION_GRANTED
        } else {
            true // API < 33：无运行时通知权限概念，视为已授予
        }
        if (!needsRequest(Build.VERSION.SDK_INT, granted, requestedThisProcess)) return
        requestedThisProcess = true
        activity.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_CODE)
    }

    /** 仅供 JVM 测试重置进程内标记（生产代码勿调）。 */
    fun resetForTest() {
        requestedThisProcess = false
    }

    private const val REQUEST_CODE = 1001
}
