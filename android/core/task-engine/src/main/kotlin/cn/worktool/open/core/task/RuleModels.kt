/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * 本地模式规则配置模型（T3.3；docs/01 FR-15/38/39，docs/02 §2 模式A）。
 *
 * 规则 = JSON 配置：匹配（[KeywordMatch]/[FriendAcceptMatch]）→ 动作（[RuleAction]）。
 * 动作意图在运行期映射为协议指令 type（203 发消息 / 212 通过好友，protocol/spec.md §4.2），
 * 由 T3.1 执行器（feature:* 车道）实现 [ActionExecutor] 后经引擎注册执行。
 */
@Serializable
enum class KeywordMatchMode {
    /** 任一关键词命中即匹配。 */
    ANY,

    /** 全部关键词命中才匹配。 */
    ALL,
}

/** 关键词匹配条件（FR-39）。null 过滤项 = 不限。 */
@Serializable
data class KeywordMatch(
    val keywords: List<String>,
    val mode: KeywordMatchMode = KeywordMatchMode.ANY,
    val caseSensitive: Boolean = false,
    /** 生效会话类型（RoomTypes，spec §4.3）；null = 不限。 */
    @SerialName("roomTypes") val roomTypes: List<Int>? = null,
    /** 生效消息类型（TextTypes，spec §4.3）；null = 不限。 */
    @SerialName("textTypes") val textTypes: List<Int>? = null,
    /** 生效发送人（精确匹配发送人名，忽略大小写）；null = 不限。 */
    val senders: List<String>? = null,
)

/** 新好友自动通过匹配条件（FR-15）。空集合 = 不限（全通过）。 */
@Serializable
data class FriendAcceptMatch(
    /** 好友名包含任一模式即命中（空 = 不限）。 */
    val namePatterns: List<String> = emptyList(),
    /** 打招呼消息包含任一关键词即命中（空 = 不限）。 */
    val leavingMsgKeywords: List<String> = emptyList(),
    val caseSensitive: Boolean = false,
)

/**
 * 好友请求处置策略（FR-15 增强：通讯录通知事件→过滤→**通过/拒绝**）。
 *
 * - [ACCEPT]：命中即产出 PASS_FRIEND 意图（212）——原语义；
 * - [REJECT]：命中即**拒绝通过**（黑名单式：不产意图、留决策记录；「拒绝」的端侧动作
 *   如点击拒绝按钮——协议无对应指令 type，执行面归 feature:* 车道，本层只做决策）。
 */
@Serializable
enum class FriendDecisionPolicy { ACCEPT, REJECT }

/**
 * 规则动作（判别字段 `type`）。当前本地模式两种动作：
 * - [SendMessageAction] → 协议 type 203 SEND_MESSAGE（spec §4.2）
 * - [PassFriendAction] → 协议 type 212 PASS_ALL_FRIEND_REQUEST（FR-15，T3.1 执行器）
 */
@Serializable
sealed interface RuleAction

/** 发送消息动作（type=SEND_MESSAGE → 203）。 */
@Serializable
@SerialName("SEND_MESSAGE")
data class SendMessageAction(
    /** 目标会话名（候选字段 targetRoom，spec §4.4 待冻结；回复触发会话时可为 null）。 */
    val targetRoom: String? = null,
    val content: String,
    val at: List<String> = emptyList(),
) : RuleAction

/** 通过好友请求动作（type=PASS_FRIEND → 212，FR-15）。 */
@Serializable
@SerialName("PASS_FRIEND")
data class PassFriendAction(
    /** 通过后的打招呼内容（可选）。 */
    val greeting: String? = null,
) : RuleAction

/** 冷却窗口作用域（FR-39 增强：原实现仅按 ruleId 计水位，跨会话互相误抑制）。 */
@Serializable
enum class CooldownScope {
    /** 全局按规则计（默认=原行为：任一会话触发即全局进入冷却）。 */
    RULE,

    /** 按「规则+会话」计（同一规则在不同会话独立冷却；会话名 null 归并为全局桶）。 */
    RULE_AND_ROOM,
}

/** 关键词自动回复规则（FR-39）。 */
@Serializable
data class KeywordRule(
    val ruleId: String,
    val enabled: Boolean = true,
    /** 规则间排序权重，大者优先（同级按配置顺序稳定排序）。 */
    val priority: Int = 0,
    val match: KeywordMatch,
    val action: RuleAction,
    /** 同规则两次触发最小间隔（防刷屏/账号风控 R2；0 = 不限）。 */
    val cooldownMillis: Long = 0,
    /** 冷却窗口作用域（默认 [CooldownScope.RULE] 保持既有行为不变）。 */
    val cooldownScope: CooldownScope = CooldownScope.RULE,
)

/** 新好友自动通过规则（FR-15，纯端侧，无需服务端）。 */
@Serializable
data class FriendAcceptRule(
    val ruleId: String,
    val enabled: Boolean = true,
    val match: FriendAcceptMatch = FriendAcceptMatch(),
    /** 处置策略（默认 [FriendDecisionPolicy.ACCEPT]；REJECT=命中即拒绝通过）。 */
    val decision: FriendDecisionPolicy = FriendDecisionPolicy.ACCEPT,
    /** 必须为 [PassFriendAction]（[RuleConfigParser] 解析期校验；多态字段统一 `type` 判别）。 */
    val action: RuleAction,
)

/** 本地 cron 任务（FR-38 模式A：本地定时独立跑；5 字段 cron 表达式，分/时/日/月/周）。 */
@Serializable
data class CronJob(
    val jobId: String,
    /** 标准 5 字段 cron：分(0-59) 时(0-23) 日(1-31) 月(1-12) 周(0-6，0=周日)。 */
    val expression: String,
    val enabled: Boolean = true,
    val action: RuleAction,
)

/** 规则集（一份 JSON 配置的顶层形态）。 */
@Serializable
data class RuleSetConfig(
    val keywordRules: List<KeywordRule> = emptyList(),
    val friendAcceptRules: List<FriendAcceptRule> = emptyList(),
    val cronJobs: List<CronJob> = emptyList(),
)

/**
 * 本地模式总配置（docs/02 §5.2 fallback 仲裁输入）。
 *
 * `fallbackLocalScheduling` 默认关（02 §5.2：「仅当 server 不可达超阈值 或 纯本地模式
 * 时启用，配置 fallbackLocalScheduling，默认关」）；阈值 docs 未定数值，暴露为配置项，
 * 默认 120s（实现侧选择，非文档冻结值）。
 */
@Serializable
data class LocalModeConfig(
    /** server 不可达超阈值时是否启用本地 cron 兜底（02 §5.2，默认关）。 */
    val fallbackLocalScheduling: Boolean = false,
    /** server 不可达判定阈值（毫秒）。 */
    val unreachableThresholdMillis: Long = DEFAULT_UNREACHABLE_THRESHOLD_MILLIS,
    /** 全局干跑开关（FR-54；调用方可按次覆盖）。 */
    val dryRun: Boolean = false,
) {
    companion object {
        const val DEFAULT_UNREACHABLE_THRESHOLD_MILLIS: Long = 120_000L
    }
}
