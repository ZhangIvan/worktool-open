/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.report

import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.feature.executor.intent.MessageEvent
import cn.worktool.open.feature.executor.intent.MessageOrigin

/**
 * 无障碍事件 → [MessageEvent]（FR-01 规范化步）。
 *
 * 输入 = core:accessibility 的纯数据快照 [AccessibilityEventSnapshot]
 * （T2.4 交付；框架 AccessibilityEvent 被系统回收复用，下游只消费快照）。
 *
 * 识别规则（两条通道，**只认企微包名**，其余包一律忽略——SR 组件最小化，
 * 不采集第三方应用通知）：
 * 1. 通知栏（TYPE_NOTIFICATION_STATE_CHANGED=64，className 含 Notification）：
 *    texts[0]=会话/发送人名，texts[1..]=正文片段（拼接）；
 * 2. 会话列表变更（TYPE_WINDOW_CONTENT_CHANGED=2048，className 含 List/RecyclerView）：
 *    texts 期望单元素「会话名: 最新一条消息」（以第一个 "："或": " 分隔）。
 *
 * 真机校准（dodDeferred）：分隔符/标题位次是**行为假设**（上游闭源无还原依据），
 * 待 W-M3R D1 真机 dump 校准后只改本对象常量，不影响下游意图/上报形态；
 * 校准前不可判空强猜——解析不出即返回 null（宁漏勿错，漏报由对端列表兜底）。
 */
class AccessibilityMessageParser(
    /** 企微包名（构造注入，测试可用任意包名构造场景）。 */
    private val weworkPackage: String = WeworkEventRules.DEFAULT_PACKAGE,
    /** 时钟（事件快照缺失时间时兜底；测试确定性注入）。 */
    private val clock: () -> Long = { 0L },
) {

    /** 解析非空 = 命中消息事件；null = 非消息事件/非企微/解析不出（忽略）。 */
    fun parse(event: AccessibilityEventSnapshot): MessageEvent? {
        if (event.packageName != weworkPackage) return null
        return when {
            event.isNotificationEvent() -> parseNotification(event)
            event.isConversationListEvent() -> parseConversationListItem(event)
            else -> null
        }
    }

    // ---- 通道①：通知栏 ----

    private fun parseNotification(event: AccessibilityEventSnapshot): MessageEvent? {
        val title = event.texts.firstOrNull()?.trim().orEmpty()
        val content = event.texts.drop(1).joinToString(" ").trim()
        if (title.isEmpty() || content.isEmpty()) return null
        return MessageEvent(
            origin = MessageOrigin.NOTIFICATION,
            targetRoom = title,
            senderNames = emptyList(),
            content = content,
            eventTimeMillis = event.eventTimeMillis.takeIf { it > 0 } ?: clock(),
        )
    }

    // ---- 通道②：会话列表条目 ----

    private fun parseConversationListItem(event: AccessibilityEventSnapshot): MessageEvent? {
        val raw = event.texts.firstOrNull()?.trim().orEmpty()
        if (raw.isEmpty()) return null
        val separator = WeworkEventRules.CONVERSATION_TEXT_SEPARATORS
            .firstOrNull { raw.contains(it) } ?: return null
        val name = raw.substringBefore(separator).trim()
        val content = raw.substringAfter(separator).trim()
        if (name.isEmpty() || content.isEmpty()) return null
        return MessageEvent(
            origin = MessageOrigin.CONVERSATION_LIST,
            targetRoom = name,
            senderNames = emptyList(),
            content = content,
            eventTimeMillis = event.eventTimeMillis.takeIf { it > 0 } ?: clock(),
        )
    }

    private fun AccessibilityEventSnapshot.isNotificationEvent(): Boolean =
        eventType == WeworkEventRules.TYPE_NOTIFICATION_STATE_CHANGED &&
            (className?.contains("Notification", ignoreCase = true) == true)

    private fun AccessibilityEventSnapshot.isConversationListEvent(): Boolean =
        eventType == WeworkEventRules.TYPE_WINDOW_CONTENT_CHANGED &&
            (className?.contains("ListView") == true || className?.contains("RecyclerView") == true)
}

/** 无障碍事件识别常量（集中一处，真机校准只改这里）。 */
object WeworkEventRules {

    /** 企微包名。 */
    const val DEFAULT_PACKAGE = "com.tencent.wework"

    /** AccessibilityEvent.TYPE_NOTIFICATION_STATE_CHANGED。 */
    const val TYPE_NOTIFICATION_STATE_CHANGED = 64

    /** AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED。 */
    const val TYPE_WINDOW_CONTENT_CHANGED = 2048

    /** 会话条目「名称↔最新消息」候选分隔符（真机校准点，dodDeferred）。 */
    val CONVERSATION_TEXT_SEPARATORS = listOf(": ", "：")
}
