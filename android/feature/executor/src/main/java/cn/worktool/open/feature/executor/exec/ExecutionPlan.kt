/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.UiDriver

/**
 * 执行计划单步（T3.1）：描述 + 动作。
 *
 * [description] 是干跑报告（FR-54「报告含目标/参数/风险级 + 将要做什么」）的
 * 数据来源——干跑只读 [description]，不执行 [action]，两者由同一 [ExecutionPlan]
 * 产出，保证「干跑与真实执行同引擎同路径（仅 policy 开关）」。
 */
class PlanStep(
    /** 稳定步骤 ID（报告/审计引用，如 `locate-conversation`）。 */
    val id: String,
    /** 人类可读描述（干跑报告直接展示）。 */
    val description: String,
    /** 真实执行动作：在驱动上执行一步，返回成功或失败。 */
    val action: (UiDriver) -> StepOutcome,
)

/**
 * 步骤化执行计划（CommandExecutor 的输出）。
 *
 * 计划是**纯数据 + 纯函数**：不含界面状态，构造后可在任意 [UiDriver] 上重放
 * （JVM Fake 与真机同构）；执行顺序语义由 [CommandRunner] 统一驱动
 * （顺序执行、步间暂停断点、首错即停）。
 */
class ExecutionPlan(
    val commandType: Int,
    val riskLevel: RiskLevel,
    /** 目标（会话/群/成员），干跑报告「目标」字段。 */
    val target: String,
    /** 参数摘要（干跑报告「参数」字段；注意不落敏感正文，报告消费方按 SR-06 脱敏）。 */
    val paramsSummary: String,
    val steps: List<PlanStep>,
)

/** 单步结果。 */
sealed class StepOutcome {
    object Success : StepOutcome()

    /** 失败：计划终止（首错即停），错误码见 [ExecutorErrorCodes]。 */
    data class Failure(val errorCode: Int, val reason: String) : StepOutcome()

    companion object {
        fun success(): StepOutcome = Success

        fun failure(errorCode: Int, reason: String): StepOutcome = Failure(errorCode, reason)
    }
}
