/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.provider.Settings
import cn.worktool.open.core.accessibility.WorkToolAccessibilityService

/**
 * 无障碍授权判定（BP-7/SP-3，路由与引导页共用同一口径）。
 *
 * 系统侧事实源 = `Settings.Secure.STRING_ENABLED_ACCESSIBILITY_SERVICES`
 * （冒号分隔的 ComponentName 列表，用户在系统设置里开启服务即写入）。相比
 * [WorkToolAccessibilityService.instance]（服务绑定并 onServiceConnected 才非空），
 * 该设置项反映**用户授权意图**，不受 MIUI 绑定延迟（BP-3：enabled 0→1 往返后才
 * 绑定）影响——路由层用「用户已授权」判定，执行通道可用性仍以 instance 为准
 * （主界面状态卡 CR UI-02 保持不变）。
 */
object AccessibilityServiceSetting {

    /**
     * 纯 JVM：enabled_accessibility_services 设置值是否含我方服务。
     *
     * 兼容两种形态（厂商 ROM 写法不一）：
     * - 全称：`pkg/pkg.cls`（ComponentName.flattenToString）；
     * - 短称：`pkg/.Cls`（flattenToShortString，类名归一为 pkg.cls 后比较）。
     *
     * @param settingValue 设置项原文（null/空 = 未授权任何服务）。
     */
    fun containsService(settingValue: String?, packageName: String, serviceClass: String): Boolean {
        val value = settingValue?.trim().orEmpty()
        if (value.isEmpty()) return false
        return value.split(':').any { raw ->
            val entry = raw.trim()
            val slash = entry.indexOf('/')
            if (slash <= 0) return@any false
            val pkg = entry.substring(0, slash)
            var cls = entry.substring(slash + 1)
            if (cls.startsWith(".")) cls = pkg + cls
            pkg == packageName && cls == serviceClass
        }
    }
}

/** 无障碍授权的系统侧查询（Android 门面：读 Settings.Secure；纯解析见上）。 */
object AccessibilityServiceGate {

    fun isAuthorized(context: Context): Boolean {
        val value = Settings.Secure.getString(
            context.contentResolver,
            Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES,
        )
        return AccessibilityServiceSetting.containsService(
            value,
            context.packageName,
            WorkToolAccessibilityService::class.java.name,
        )
    }
}
