/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel

/**
 * 指令执行器接口（T3.1，docs/02 §3 feature:* 行「每指令一执行器+单测」）。
 *
 * 契约：
 * - [commandType] = protocol/spec.md §4.2 指令号（注册表按键路由）；
 * - [riskLevel] = FR-53 风险级标注（端侧执行前复检载体）；
 * - [buildPlan] = 产出步骤化执行计划（纯数据，不触碰界面）。
 *
 * 每执行器内置三件套（docs/03 T3.1）：
 * 1. 风险级标注（FR-53）：见 [riskLevel]；
 * 2. 干跑钩子（FR-54）：同一 [buildPlan] 产物，[CommandRunner.dryRun] 只读不执行；
 * 3. 暂停门检查（FR-52）：由 [CommandRunner] 在执行前/步间断点统一查询 PauseManager。
 *
 * 与 core:task-engine（T3.3）的对接：其 `ActionExecutor` 端口（Ports.kt，
 * 「T3.3 消费、T3.1 实现」）由**适配层**桥接——把 `ActionIntent` 规范化为
 * [CommandIntent] 后经 [ExecutorRegistry] + [cn.worktool.open.feature.executor.runtime.CommandRunner]
 * 执行、[ExecutionReport] 映射回 `ExecOutcome`（错误码族同为 spec §4.3 口径）；
 * 其 `PauseManager.isPaused` 经同名适配实现本侧
 * [cn.worktool.open.feature.executor.runtime.PauseManager]。适配层在 W-M3 集成阶段
 * 随 app 接线落地（本模块不直接依赖并行车道在途代码，避免构建耦合）。
 */
interface CommandExecutor {
    val commandType: Int

    /** FR-53 风险级（按意图参数细化，如 UPDATE_GROUP 含拉人 → L1）。 */
    fun riskLevel(intent: CommandIntent): RiskLevel

    /** 步骤化执行计划。 */
    fun buildPlan(intent: CommandIntent): ExecutionPlan
}

/**
 * 类型化基类：执行器只接收自己声明的意图类型。
 *
 * 类型安全由指令号守卫保证（注册表按 [CommandExecutor.commandType] 路由，
 * 意图自携带 commandType）；号不匹配 = 接线错误，fail-fast。
 */
abstract class TypedCommandExecutor<I : CommandIntent> : CommandExecutor {

    @Suppress("UNCHECKED_CAST")
    final override fun buildPlan(intent: CommandIntent): ExecutionPlan {
        check(intent.commandType == commandType) {
            "执行器 ${this::class.simpleName}(type=$commandType) 收到不匹配意图 " +
                "${intent::class.simpleName}(type=${intent.commandType})"
        }
        return buildTypedPlan(intent as I)
    }

    protected abstract fun buildTypedPlan(intent: I): ExecutionPlan
}
