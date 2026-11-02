/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.event

/**
 * 无障碍事件快照（T2.4 事件监听骨架的 JVM 纯数据面）。
 *
 * 快照而非透传框架 `AccessibilityEvent` 的原因：AccessibilityEvent 是框架对象、
 * 事件后被系统回收复用，且携带 android 依赖无法 JVM 单测；
 * 下游（T3.1 消息监听 FR-01 等）只消费纯数据。
 */
data class AccessibilityEventSnapshot(
    /** 框架 eventType 原值（如 TYPE_WINDOW_STATE_CHANGED=32）。 */
    val eventType: Int,
    val packageName: String?,
    val className: String?,
    val texts: List<String>,
    val eventTimeMillis: Long,
)

/** 事件消费方接口（服务持有注册表；T3.1 起由指令执行器/消息监听器注册）。 */
interface AccessibilityEventSink {
    fun onAccessibilityEvent(event: AccessibilityEventSnapshot)
}
