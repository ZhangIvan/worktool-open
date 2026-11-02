/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 干跑报告器（FR-54：指令产出「将要做什么」报告，不产生真实副作用）。
 *
 * 关键约束（FR-54 验收）：**干跑与真实执行同引擎同路径**（仅 policy 开关）——
 * [LocalTaskEngine] 的统一 dispatch 以 `dryRun` 分叉：干跑只产出 [DryRunReport]，
 * 绝不调用 [ActionExecutor]；报告必含 目标/参数/风险级 三要素。
 */
class DryRunRunner(private val clock: () -> Long) {

    /** 干跑报告（目标/参数/风险级，FR-54 验收口径）。 */
    data class DryRunReport(
        val ruleId: String?,
        val source: Source,
        val commandType: Int,
        /** 目标（会话名/好友名等；null = 未指定）。 */
        val target: String?,
        /** 动作参数（content/at/greeting 等，脱敏由上层日志管道负责 SR-06）。 */
        val params: Map<String, String>,
        val riskLevel: RiskLevel,
        val atMillis: Long,
    ) {
        override fun toString(): String =
            "DryRunReport(type=$commandType, target=$target, params=${params.keys}, " +
                "risk=$riskLevel, source=$source, ruleId=$ruleId, at=$atMillis) —— 不产生真实副作用"
    }

    /** 由动作意图生成干跑报告（无任何副作用）。 */
    fun report(intent: ActionIntent): DryRunReport {
        val params = LinkedHashMap<String, String>()
        intent.content?.let { params["content"] = it }
        if (intent.at.isNotEmpty()) params["at"] = intent.at.joinToString(",")
        return DryRunReport(
            ruleId = intent.ruleId,
            source = intent.source,
            commandType = intent.commandType,
            target = intent.targetRoom,
            params = params,
            riskLevel = RiskLevels.of(intent.commandType),
            atMillis = clock(),
        )
    }
}
