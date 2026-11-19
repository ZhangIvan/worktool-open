package io.worktool.open.prototype.ui

import android.accessibilityservice.AccessibilityServiceInfo
import android.app.Activity
import io.worktool.open.prototype.policy.VariantFeatures
import android.content.ComponentName
import android.content.Intent
import android.provider.Settings
import android.view.accessibility.AccessibilityManager
import io.worktool.open.prototype.accessibility.FixtureServiceRegistry
import io.worktool.open.prototype.accessibility.OwnFixtureAccessibilityService

/** Settings navigation never enables a service, clicks permission UI, or edits secure settings. */
internal object AccessibilityUi {
    fun systemEnabled(activity: Activity): Boolean = try {
        check(VariantFeatures.ownFixtureAccessibility)
        val expected = ComponentName(activity, OwnFixtureAccessibilityService::class.java)
        val manager = activity.getSystemService(AccessibilityManager::class.java)
        manager.getEnabledAccessibilityServiceList(AccessibilityServiceInfo.FEEDBACK_ALL_MASK).any {
            val declared = it.resolveInfo.serviceInfo
            ComponentName(declared.packageName, declared.name) == expected
        }
    } catch (_: Exception) { false }

    /** Must only be invoked by an explicit user click. */
    fun openSettings(activity: Activity): Boolean = try {
        check(VariantFeatures.ownFixtureAccessibility)
        activity.startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        true
    } catch (_: Exception) { false }

    fun describe(activity: Activity): String {
        if(!VariantFeatures.ownFixtureAccessibility)return "VARIANT_DRIVER_DENIED"
        val value = FixtureServiceRegistry.status()
        return "候选已声明 BIND_ACCESSIBILITY_SERVICE 受保护服务，不是首个 INTERNET-only APK。\n" +
            "系统设置启用：${systemEnabled(activity)} · 服务已连接：${value.serviceConnected}\n" +
            "本地动作开关：${value.locallyEnabled} · 本页前台租约：${value.foreground}\n" +
            "实际安装包：${activity.packageName}\n" +
            "代码：${UiViews.code(value.reasonCode)}\n" +
            "SDK/设备能力需独立验收；系统启用不等于动作已获批准。"
    }
}
