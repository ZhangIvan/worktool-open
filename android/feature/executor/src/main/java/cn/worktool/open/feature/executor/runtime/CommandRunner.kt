/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.feature.executor.exec.CommandExecutor
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel

/** 终态：成功 / 失败 / 暂停拦截（FR-52 非错误语义，单独状态）。 */
enum class ExecutionStatus { SUCCEEDED, FAILED, PAUSED }

/** 已执行步骤留痕（每步一条，审计/回执映射消费）。 */
data class StepRecord(
    val id: String,
    val description: String,
    val index: Int,
    val succeeded: Boolean,
    val errorCode: Int?,
    val errorReason: String?,
)

/**
 * 真实执行报告（socketType=3 回执的端侧数据源，映射以 T3.2 冻结为准）。
 *
 * - PAUSED（执行前拦截）：executedSteps 为空、skippedStepIds=全量（在线指令只入队
 *   不执行的入队动作归上层调度）；
 * - PAUSED（步间断点）：executedSteps=已安全完成部分（FR-52「进行中动作序列执行至
 *   安全断点停止」，断点 = 步边界）；
 * - FAILED：首错即停，errorStep 定位失败步骤。
 */
data class ExecutionReport(
    val commandType: Int,
    val riskLevel: RiskLevel,
    val status: ExecutionStatus,
    val errorCode: Int,
    val errorReason: String?,
    val errorStepId: String?,
    val executedSteps: List<StepRecord>,
    val skippedStepIds: List<String>,
    val plannedStepIds: List<String>,
    val startedAtMillis: Long,
    val finishedAtMillis: Long,
    /** PAUSED 时的暂停来源（FR-52 三处一致性核对用）。 */
    val pausedBySource: String?,
)

/**
 * 干跑报告（FR-54）：指令「将要做什么」，不产生任何真实副作用。
 *
 * 与真实执行同引擎同路径：同一 [ExecutionPlan] 产物，仅策略开关
 * （[dryRun] 不触碰 [UiDriver]，也不受暂停门拦截——干跑无副作用，属安全操作）。
 */
data class DryRunReport(
    val commandType: Int,
    val riskLevel: RiskLevel,
    val target: String,
    val paramsSummary: String,
    /** 计划步骤（id: description 逐条），即「将要做什么」。 */
    val plannedSteps: List<String>,
)

/**
 * 执行引擎（T3.1）：意图 → 执行器路由 → 暂停门（FR-52）→ 计划逐步执行 / 干跑（FR-54）。
 *
 * 纯状态机：不持有界面状态，一切界面交互经 [UiDriver]；时钟取自驱动
 * （[UiDriver.nowMillis]），JVM 单测可用假时钟确定性复现超时。
 */
class CommandRunner(
    private val driver: UiDriver,
    private val pauseManager: PauseManager,
) {

    /** 干跑（FR-54）：只构建计划并产出报告，不执行任何步骤动作。 */
    fun dryRun(
        intent: CommandIntent,
        registry: ExecutorRegistry,
    ): DryRunReport {
        val executor = registry.executorFor(intent.commandType)
            ?: return unsupportedDryRun(intent)
        return planToDryRunReport(executor, intent)
    }

    /**
     * 真实执行。
     *
     * 门序（固定，便于测试）：执行前暂停门 → 逐步执行（每步前再查暂停门 = 安全断点）
     * → 首错即停。
     */
    fun execute(
        intent: CommandIntent,
        registry: ExecutorRegistry,
    ): ExecutionReport {
        val startedAt = driver.nowMillis()

        fun report(
            status: ExecutionStatus,
            errorCode: Int,
            reason: String?,
            errorStepId: String?,
            plan: ExecutionPlan?,
            executed: List<StepRecord>,
            pausedSource: String? = null,
        ) = ExecutionReport(
            commandType = intent.commandType,
            riskLevel = plan?.riskLevel
                ?: registry.executorFor(intent.commandType)?.riskLevel(intent)
                ?: RiskLevel.L0,
            status = status,
            errorCode = errorCode,
            errorReason = reason,
            errorStepId = errorStepId,
            executedSteps = executed,
            skippedStepIds = plan?.steps?.map { it.id }?.drop(executed.size) ?: emptyList(),
            plannedStepIds = plan?.steps?.map { it.id } ?: emptyList(),
            startedAtMillis = startedAt,
            finishedAtMillis = driver.nowMillis(),
            pausedBySource = pausedSource,
        )

        val executor: CommandExecutor = registry.executorFor(intent.commandType)
            ?: return report(
                ExecutionStatus.FAILED,
                ExecutorErrorCodes.COMMAND_UNSUPPORTED,
                "指令 ${intent.commandType} 未注册执行器（P0 子集=${registry.supportedTypes.sorted()})",
                null,
                null,
                emptyList(),
            )

        val plan = executor.buildPlan(intent)

        // FR-52 暂停门（执行前）：接管期间在线指令只入队不执行 → 端侧零副作用返回。
        if (pauseManager.isPaused()) {
            return report(
                ExecutionStatus.PAUSED,
                ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER,
                "用户接管暂停中，指令未执行（只入队语义归上层调度）",
                null,
                plan,
                emptyList(),
                pauseManager.pauseSource(),
            )
        }

        // 目标前置门（2026-11-03 会话 9 补位）：企微不在前台时先拉起（对齐原 APK
        // 行为）；无法达成（服务未连/未安装/超时）以 201010 失败——不再让计划
        // 首步盲跑出 NODE_NOT_FOUND。dryRun 不经过本门（零副作用）。
        if (!driver.ensureTargetForeground()) {
            return report(
                ExecutionStatus.FAILED,
                ExecutorErrorCodes.TARGET_APP_LAUNCH_FAILED,
                driver.lastTargetForegroundFailure ?: "企微未能拉起到前台（无障碍未连接/企微未安装/超时未就绪）",
                "ensure-target-foreground",
                plan,
                emptyList(),
            )
        }

        val executed = ArrayList<StepRecord>(plan.steps.size)
        for ((index, step) in plan.steps.withIndex()) {
            // FR-52 安全断点：步边界重查暂停门（进行中序列执行至安全断点停止）。
            if (pauseManager.isPaused()) {
                return report(
                    ExecutionStatus.PAUSED,
                    ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER,
                    "用户接管：于安全断点（步骤 ${step.id} 前）停止",
                    null,
                    plan,
                    executed,
                    pauseManager.pauseSource(),
                )
            }
            val outcome = try {
                step.action(driver)
            } catch (t: Throwable) {
                StepOutcome.Failure(
                    ExecutorErrorCodes.STEP_ERROR,
                    "步骤 ${step.id} 异常：${t::class.simpleName}: ${t.message}",
                )
            }
            when (outcome) {
                is StepOutcome.Success ->
                    executed += StepRecord(step.id, step.description, index, true, null, null)

                is StepOutcome.Failure -> {
                    executed += StepRecord(
                        step.id, step.description, index,
                        succeeded = false,
                        errorCode = outcome.errorCode,
                        errorReason = outcome.reason,
                    )
                    return report(
                        ExecutionStatus.FAILED,
                        outcome.errorCode,
                        outcome.reason,
                        step.id,
                        plan,
                        executed,
                    )
                }
            }
        }
        return report(ExecutionStatus.SUCCEEDED, 0, null, null, plan, executed)
    }

    private fun unsupportedDryRun(intent: CommandIntent): DryRunReport = DryRunReport(
        commandType = intent.commandType,
        // 未登记指令按 L2 fail-closed 展示（docs/01 FR-53 裁决②；CR 收口 C4）
        riskLevel = RiskLevel.L2,
        target = "",
        paramsSummary = "",
        plannedSteps = listOf(
            "<不可执行：指令 ${intent.commandType} 未注册执行器>",
        ),
    )

    private fun planToDryRunReport(
        executor: CommandExecutor,
        intent: CommandIntent,
    ): DryRunReport {
        val plan = executor.buildPlan(intent)
        return DryRunReport(
            commandType = plan.commandType,
            riskLevel = plan.riskLevel,
            target = plan.target,
            paramsSummary = plan.paramsSummary,
            plannedSteps = plan.steps.map { "${it.id}: ${it.description}" },
        )
    }
}
