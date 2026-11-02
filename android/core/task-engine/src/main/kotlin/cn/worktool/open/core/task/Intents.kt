/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes

/**
 * 动作来源与优先级（docs/02 §5.2 冻结序：**用户接管 > 服务端指令 > 本地规则**）。
 */
enum class Source {
    /** 用户手动接管操作（悬浮窗/控制台一键接管，FR-52）。 */
    USER_TAKEOVER,

    /** 服务端下发指令（模式 B 在线指令）。 */
    SERVER_COMMAND,

    /** 本地规则产物（关键词回复/好友通过/本地 cron，模式 A 核心）。 */
    LOCAL_RULE,
}

/** 来源优先级常量（数值大者胜出；docs/02 §5.2 优先级行，T3.3 ④ 冻结为常量）。 */
object SourcePriority {
    const val USER_TAKEOVER: Int = 300
    const val SERVER_COMMAND: Int = 200
    const val LOCAL_RULE: Int = 100

    fun of(source: Source): Int = when (source) {
        Source.USER_TAKEOVER -> USER_TAKEOVER
        Source.SERVER_COMMAND -> SERVER_COMMAND
        Source.LOCAL_RULE -> LOCAL_RULE
    }
}

/** 指令风险分级（docs/01 FR-53：L0 普通 / L1 批量发送、建群、拉人 / L2 解散群、删联系人、清队列）。 */
enum class RiskLevel { L0, L1, L2 }

/**
 * 协议指令 type → 风险级映射（FR-53 分级表；兜底口径三段式，对齐服务端
 * scheduler guard.go ClassifyRisk：分级表内→对应级；**端侧已实现**普通指令→L0；
 * 未登记类型→L2 fail-closed——docs/01 FR-53 裁决②「未登记类型默认按 L2
 * fail-closed」，原「未列入=按 L0」的 L0-default 已废止，登记后方可降级）。
 *
 * 定位（CR-t31-T31-R3 风险级口径对齐）：本表是**类型级兜底**——同一指令号的
 * **载荷级动态分级**（如 207 含拉人 → L1，见 feature:executor
 * UpdateGroupExecutor.riskLevel）更贴 FR-53 原文，以执行器侧为准；本表仅在
 * 执行器不可得时兜底，且须与服务端类型级表（server scheduler guard.go
 * riskClassByType，两级命名统一 L0/L1/L2）保持一致，不得同指令不同级。
 */
object RiskLevels {
    fun of(commandType: Int): RiskLevel = when (commandType) {
        // L1：批量发送/转发、建群、拉人（含加好友类与改群拉人 207）
        MessageTypes.INIT_GROUP,
        MessageTypes.UPDATE_GROUP,
        MessageTypes.ADD_FRIEND_BY_PHONE,
        MessageTypes.ADD_FRIEND_BY_GROUP,
        MessageTypes.SEND_MULTI_MESSAGE,
        MessageTypes.SEND_MERGE_MESSAGE,
        MessageTypes.RELAY_MULTI_MESSAGE,
        MessageTypes.RELAY_MERGE_MESSAGE,
        -> RiskLevel.L1

        // L2：解散群、删联系人、清队列
        MessageTypes.DISMISS_GROUP,
        MessageTypes.DELETE_CONTACT,
        MessageTypes.ROBOT_QUEUE_CLEAR,
        -> RiskLevel.L2

        // L0：端侧已实现的普通指令（FR-53 裁决③「其余已实现指令=L0」）：
        // defaultP0 非 L1/L2 项 203/212/225 + defaultFirstBatch 查询面 501/502/509。
        // 501/502/509 在 server 侧已登记（dryrun.go plannedActionNames 名单内，
        // guard.go ClassifyRisk 分级表外+已实现 → L0），端侧补齐后两表同指令同级；
        // 503~508 端侧执行器未实现前仍落 else→L2 fail-closed（裁决②），实现后随
        // 「新增执行器时须同步本名单」义务降级。
        MessageTypes.SEND_MESSAGE,
        MessageTypes.PASS_ALL_FRIEND_REQUEST,
        MessageTypes.MODIFY_GROUP_MEMBER_INFO,
        MessageTypes.GET_GROUP_INFO,
        MessageTypes.GET_FRIEND_INFO,
        MessageTypes.GET_LOCAL_FILE,
        -> RiskLevel.L0

        // 未登记类型 → L2 fail-closed（FR-53 裁决②；与服务端 riskUnregistered
        // →RiskL2 一致，防新指令未分级裸奔）
        else -> RiskLevel.L2
    }
}

/**
 * 运行期动作意图（规则/服务端指令归一后的执行单元）。
 * [commandType] = 协议指令 type（protocol/spec.md §4.2）。
 */
data class ActionIntent(
    val commandType: Int,
    val targetRoom: String? = null,
    val content: String? = null,
    val at: List<String> = emptyList(),
    /** 产生该意图的本地规则（服务端指令/用户接管为 null）。 */
    val ruleId: String? = null,
    val source: Source = Source.LOCAL_RULE,
)

/** 入站聊天消息事件快照（T3.1 消息监听 FR-01 的纯数据输入，JVM 可测）。 */
data class MessageEvent(
    val roomType: Int,
    val textType: Int,
    /** 会话名（候选字段 targetRoom，spec §4.4 待 T3.2 冻结）。 */
    val targetRoom: String?,
    /** 发送人显示名列表（spec §4.3 nameList）。 */
    val senderNames: List<String>,
    val text: String,
    val eventTimeMillis: Long,
)

/** 新好友请求事件快照（FR-15 输入；对应 Friend.newFriend/leavingMsg，spec §4.3）。 */
data class FriendRequestEvent(
    val friendName: String,
    val leavingMsg: String?,
    val eventTimeMillis: Long,
)

/** 服务端下发指令（模式 B 在线指令，引擎侧门禁/入队用）。 */
data class ServerCommand(
    val commandId: String,
    val commandType: Int,
    val targetRoom: String? = null,
    val content: String? = null,
    val receivedAtMillis: Long,
)

/** [RuleAction] → [ActionIntent] 归一（本地规则产物固定 source=LOCAL_RULE）。 */
fun RuleAction.toIntent(ruleId: String, fallbackTargetRoom: String? = null): ActionIntent = when (this) {
    is SendMessageAction -> ActionIntent(
        commandType = MessageTypes.SEND_MESSAGE,
        targetRoom = targetRoom ?: fallbackTargetRoom,
        content = content,
        at = at,
        ruleId = ruleId,
        source = Source.LOCAL_RULE,
    )
    is PassFriendAction -> ActionIntent(
        commandType = MessageTypes.PASS_ALL_FRIEND_REQUEST,
        content = greeting,
        ruleId = ruleId,
        source = Source.LOCAL_RULE,
    )
}
