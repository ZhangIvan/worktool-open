/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.intent

import cn.worktool.open.core.protocol.RoomTypes
import cn.worktool.open.core.protocol.TextTypes

/**
 * 新消息事件意图（FR-01 上报方向，type=101 TYPE_RECEIVE_MESSAGE_LIST，端→云）。
 *
 * 来源链路（docs/01 FR-01）：无障碍事件（通知栏 / 会话列表变更）经
 * [cn.worktool.open.feature.executor.report.AccessibilityMessageParser]
 * 规范化为本意图，再由 [cn.worktool.open.feature.executor.report.MessageEventReporter]
 * 映射为 `WeworkMessageBean(type=101)` 经 core:protocol 上报（socketType=2，
 * 线形态对齐 protocol/fixtures/wire-v1-envelope.report-101.json）。
 *
 * 命名说明：core:task-engine（T3.3）已有同名的入站消息快照
 * `cn.worktool.open.core.task.MessageEvent`（本地规则输入）——两者字段语义一致，
 * 由 app 装配层做映射（范式同 FeatureExecutorBinding.mapIntent）；本模块按
 * docs/02 §3 分工**不直接依赖** core:task-engine（并行车道，构建解耦）。
 *
 * 字段口径：
 * - [roomType]/[textType] 取 spec §4.3 枚举；无法从事件可靠判定时 = UNKNOWN（0），
 *   不猜测（真机校准归 W-M3R dump 阶段）；
 * - [targetRoom] 为会话名候选字段（spec §4.4 待 T3.2 冻结，PayloadFields.TARGET_ROOM）；
 * - [content] 为消息正文——**仅入上报载荷**，落日志必须过
 *   [cn.worktool.open.feature.executor.report.SensitiveRedactor]（SR-06）。
 */
data class MessageEvent(
    /** 事件来源通道（通知栏 / 会话列表）。 */
    val origin: MessageOrigin,
    /** 会话类型（RoomTypes；无法判定 = [RoomTypes.UNKNOWN]）。 */
    val roomType: Int = RoomTypes.UNKNOWN,
    /** 消息类型（TextTypes；通知/列表事件默认 [TextTypes.TEXT]）。 */
    val textType: Int = TextTypes.TEXT,
    /** 会话名（群名/联系人名；候选字段，spec §4.4）。 */
    val targetRoom: String,
    /** 发送人显示名（群聊时为成员名；单聊可与会话名一致）。 */
    val senderNames: List<String> = emptyList(),
    /** 消息正文（SR-06：不落日志，仅入加密上报载荷）。 */
    val content: String,
    /** 事件时间（毫秒；来源 AccessibilityEventSnapshot.eventTimeMillis）。 */
    val eventTimeMillis: Long,
) {
    init {
        require(targetRoom.isNotBlank()) { "MessageEvent.targetRoom 必填且非空" }
        require(content.isNotBlank()) { "MessageEvent.content 必填且非空" }
    }

    /** 去重指纹（同窗口内通知/会话列表双通道重复上报判定，FR-01 消抖）。 */
    fun eventHash(): String = "$targetRoom|$content|${senderNames.joinToString(",")}"
}

/** FR-01 事件来源通道（决定解析规则与上报优先级）。 */
enum class MessageOrigin {
    /** 通知栏事件（TYPE_NOTIFICATION_STATE_CHANGED）。 */
    NOTIFICATION,

    /** 会话列表变更事件（主界面消息列表内容变更）。 */
    CONVERSATION_LIST,
}
