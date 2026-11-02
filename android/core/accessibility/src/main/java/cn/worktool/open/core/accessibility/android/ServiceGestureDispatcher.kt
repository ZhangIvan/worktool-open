/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.graphics.Path
import cn.worktool.open.core.accessibility.ops.GestureDispatcher

/**
 * [GestureDispatcher] 真机实现：经无障碍手势通道注入点按/长按。
 * 依赖服务配置 `android:canPerformGestures="true"`（见 res/xml/accessibility_service_config.xml）。
 */
class ServiceGestureDispatcher(private val service: AccessibilityService) : GestureDispatcher {

    override fun tap(x: Int, y: Int): Boolean = dispatch(x, y, TAP_DURATION_MS)

    override fun longPress(x: Int, y: Int): Boolean = dispatch(x, y, LONG_PRESS_DURATION_MS)

    private fun dispatch(x: Int, y: Int, durationMs: Long): Boolean {
        val path = Path().apply { moveTo(x.toFloat(), y.toFloat()) }
        val stroke = GestureDescription.StrokeDescription(path, 0L, durationMs)
        val gesture = GestureDescription.Builder().addStroke(stroke).build()
        return service.dispatchGesture(gesture, null, null)
    }

    companion object {
        /** 系统手势点按判定阈值内（框架惯例 ~100ms 以下视为点按）。 */
        private const val TAP_DURATION_MS = 80L

        /** 长按阈值（系统默认 ViewConfiguration ~400ms 起，取 600ms 稳妥）。 */
        private const val LONG_PRESS_DURATION_MS = 600L
    }
}
