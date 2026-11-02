/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility

import android.accessibilityservice.AccessibilityService
import android.view.accessibility.AccessibilityEvent

/**
 * 无障碍服务骨架（T1.2）。
 *
 * 默认弱权限（res/xml/accessibility_service_config.xml）：
 * canRetrieveWindowContent=false、不监听任何包 —— 本阶段服务被启用后也无任何行为。
 *
 * T2.4 落地：节点定位规则引擎 + 5 项原子操作（点击/长按/输入/滑动/截图），
 * 权限按最小必要原则逐项放开并接受 G10 manifest/config diff 审查。
 */
class WorkToolAccessibilityService : AccessibilityService() {

    override fun onServiceConnected() {
        super.onServiceConnected()
        // 骨架：无副作用。T2.4 在此初始化原子操作库与规则引擎。
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        // 骨架：配置未声明 accessibilityEventTypes/packageNames，正常不会收到事件；
        // 即使被调用也不做任何处理。
    }

    override fun onInterrupt() {
        // 骨架：无反馈通道需要中断。
    }
}
