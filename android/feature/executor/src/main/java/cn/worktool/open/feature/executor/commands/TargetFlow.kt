/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.ports.TargetOutcome

/**
 * 多目标指令逐格成败收集器（204/205/221/227/228/229/230/235/236/238 共用）。
 *
 * 两级语义（对等计划：部分成功不压平）：
 * - **目标级**（进房/定位失败）：[failTarget] 置标记 → 本目标后续结构步经
 *   [guardedStep] 空过（零副作用），由 [finishTarget] 记一条目标级败果；
 * - **格级**（238 文本格 / 229 消息格）：[recordCell]/[failCell] 逐格记录，
 *   **单格失败继续后续格**（契约「单条失败继续后续」）。
 *
 * 末步 [emitStep] 把全量矩阵经 [BatchOutcomeSink] 出端口，再按
 * 「全成=成功 / 有败=失败（首败码+计数）」决定计划终态——回执 successList/failList
 * 映射以 T3.2 冻结为准，端口数据是单一事实源。
 */
internal class TargetFlow(
    private val commandType: Int,
    private val sink: BatchOutcomeSink,
) {

    private val outcomes = ArrayList<TargetOutcome>()

    private var currentTarget = ""
    private var targetFailed = false
    private var targetError: Pair<Int, String>? = null

    val failureCount: Int get() = outcomes.count { !it.succeeded }

    // ---- 目标级 ----

    fun beginTarget(target: String) {
        currentTarget = target
        targetFailed = false
        targetError = null
    }

    /** 当前目标是否已失败（guardedStep 空过判定）。 */
    val failed: Boolean get() = targetFailed

    fun failTarget(errorCode: Int, reason: String) {
        if (targetFailed) return // 已失败：后续空过步不再覆盖首败原因
        targetFailed = true
        targetError = errorCode to reason
    }

    /** 记录失败步结果并转空过成功（记败不中断，后续目标继续）；成功原样返回。 */
    fun record(outcome: StepOutcome): StepOutcome =
        if (outcome is StepOutcome.Failure) {
            failTarget(outcome.errorCode, outcome.reason)
            StepOutcome.Success
        } else {
            outcome
        }

    /** 记目标级败并返回空过成功（guardedStep 内联失败用）。 */
    fun failAsGuarded(errorCode: Int, reason: String): StepOutcome {
        failTarget(errorCode, reason)
        return StepOutcome.Success
    }

    /** 生成带守卫的步骤（当前目标已失败时零副作用空过）。 */
    fun guardedStep(
        id: String,
        description: String,
        action: (cn.worktool.open.feature.executor.runtime.UiDriver) -> StepOutcome,
    ): PlanStep = PlanStep(id = id, description = description) { driver ->
        if (targetFailed) return@PlanStep StepOutcome.Success
        action(driver)
    }

    /** 记一条目标级结果（目标级执行器末步调用：一目标一果）。 */
    fun finishTarget() {
        outcomes += TargetOutcome(
            target = currentTarget,
            cell = null,
            succeeded = !targetFailed,
            errorCode = targetError?.first,
            reason = targetError?.second,
        )
        targetFailed = false
        targetError = null
    }

    // ---- 格级（238/229 二维矩阵；单格失败不置目标级标记） ----

    fun recordCell(cell: String, succeeded: Boolean, errorCode: Int? = null, reason: String? = null) {
        outcomes += TargetOutcome(
            target = currentTarget,
            cell = cell,
            succeeded = succeeded,
            errorCode = errorCode,
            reason = reason,
        )
    }

    fun failCell(cell: String, errorCode: Int, reason: String) =
        recordCell(cell, succeeded = false, errorCode = errorCode, reason = reason)

    // ---- 末步 ----

    /** 末步：出端口 + 终态判定（FAILED/PAUSED 路径不会到达 → 不外发部分结果）。 */
    fun emitStep(): PlanStep = PlanStep(
        id = "emit-outcomes",
        description = "输出逐目标/逐格成败矩阵并判定终态（全成=成功；有败=失败+首败码）",
    ) { _ ->
        sink.onBatchOutcome(commandType, outcomes.toList())
        val firstFailure = outcomes.firstOrNull { !it.succeeded }
        if (firstFailure == null) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(
                firstFailure.errorCode ?: ExecutorErrorCodes.STEP_ERROR,
                "部分目标失败：${failureCount}/${outcomes.size} 格" +
                    "（首败=${firstFailure.target}${firstFailure.cell?.let { "#$it" } ?: ""}，" +
                    "${firstFailure.reason.orEmpty()}）",
            )
        }
    }
}
