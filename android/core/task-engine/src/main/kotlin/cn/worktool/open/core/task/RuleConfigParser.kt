/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json

/** 规则配置解析失败（格式非法或语义校验不过）。 */
class RuleConfigException(message: String, cause: Throwable? = null) : Exception(message, cause)

/**
 * 规则/本地模式配置解析器（T3.3）。
 *
 * 输入 JSON 文档示例（规则=JSON 配置，docs/03 T3.3 ①）：
 * ```json
 * {
 *   "keywordRules": [{
 *     "ruleId": "kr-price", "priority": 100, "cooldownMillis": 60000,
 *     "match": {"keywords": ["价格", "报价"], "mode": "ANY",
 *                "roomTypes": [1, 2], "textTypes": [1]},
 *     "action": {"type": "SEND_MESSAGE", "content": "报价请联系人工客服"}
 *   }],
 *   "friendAcceptRules": [{
 *     "ruleId": "fr-all",
 *     "match": {"leavingMsgKeywords": ["合作"]},
 *     "action": {"type": "PASS_FRIEND"}
 *   }],
 *   "cronJobs": [{
 *     "jobId": "cron-morning", "expression": "0 9 * * 1-5",
 *     "action": {"type": "SEND_MESSAGE", "targetRoom": "早报群", "content": "早上好"}
 *   }]
 * }
 * ```
 * 未知字段不忽略（strict）——配置漂移应在校验期暴露而非静默吞掉。
 */
object RuleConfigParser {

    private val json = Json {
        ignoreUnknownKeys = false
        encodeDefaults = true
    }

    /** 解析规则集（语义校验：ruleId 非空且全局唯一、关键词非空、cron 表达式合法）。 */
    fun parseRules(text: String): RuleSetConfig {
        val config = try {
            json.decodeFromString(RuleSetConfig.serializer(), text)
        } catch (e: SerializationException) {
            throw RuleConfigException("规则配置 JSON 非法: ${e.message}", e)
        }
        validate(config)
        return config
    }

    /** 解析本地模式总配置（缺省字段取默认值）。 */
    fun parseConfig(text: String): LocalModeConfig = try {
        json.decodeFromString(LocalModeConfig.serializer(), text)
    } catch (e: SerializationException) {
        throw RuleConfigException("本地模式配置 JSON 非法: ${e.message}", e)
    }

    private fun validate(config: RuleSetConfig) {
        val seen = HashSet<String>()
        config.keywordRules.forEachIndexed { i, r ->
            requireId(r.ruleId, "keywordRules[$i]", seen)
            if (r.match.keywords.isEmpty()) {
                throw RuleConfigException("keywordRules[$i](${r.ruleId}).match.keywords 不得为空")
            }
        }
        config.friendAcceptRules.forEachIndexed { i, r ->
            requireId(r.ruleId, "friendAcceptRules[$i]", seen)
            if (r.action !is PassFriendAction) {
                throw RuleConfigException(
                    "friendAcceptRules[$i](${r.ruleId}).action 必须为 PASS_FRIEND（FR-15 语义）",
                )
            }
        }
        config.cronJobs.forEachIndexed { i, j ->
            requireId(j.jobId, "cronJobs[$i]", seen)
            try {
                CronExpression.parse(j.expression)
            } catch (e: RuleConfigException) {
                throw RuleConfigException("cronJobs[$i](${j.jobId}).expression 非法: ${e.message}", e)
            }
        }
    }

    private fun requireId(id: String, where: String, seen: MutableSet<String>) {
        if (id.isBlank()) throw RuleConfigException("$where.ruleId/jobId 不得为空")
        if (!seen.add(id)) throw RuleConfigException("ruleId/jobId 重复: $id（跨规则类型全局唯一）")
    }
}
