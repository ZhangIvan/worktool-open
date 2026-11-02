/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.task.ActionExecutor
import cn.worktool.open.core.task.ActionIntent
import cn.worktool.open.core.task.ExecOutcome
import cn.worktool.open.core.task.LocalTaskEngine
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus

/**
 * T3.1 指令执行框架 ↔ T3.3 本地任务引擎 的装配适配器（app 接线层，车道合流点）。
 *
 * 职责（docs/02 §3 分工）：
 * - 把 T3.1 `CommandRunner/ExecutorRegistry`（意图→执行器路由→暂停门→计划执行）
 *   包成本引擎的 [ActionExecutor] 端口按指令号注册——本地规则（关键词回复 FR-39 /
 *   新好友通过 FR-15 / 本地 cron FR-38）产出的 [ActionIntent] 经同一执行面落地；
 * - FR-52 暂停语义分层：引擎侧（[cn.worktool.open.core.task.PauseManager]）管
 *   「在线指令只入队 + 显式确认恢复」，T3.1 `CommandRunner` 侧管「执行前/步间断点
 *   零副作用」——两侧经同一暂停状态源（app 装配时把引擎的 PauseManager 传给
 *   CommandRunner 的只读暂停门实现）保持一致；
 * - 真机 UiDriver = T3.1 侧接口（设备驱动归后续任务卡/W-M3R），本适配器只做
 *   意图映射与结果回传，不引入设备耦合。
 */
object FeatureExecutorBinding {

    /** T3.1 执行面 → 引擎 ActionExecutor（单指令号一实例）。 */
    class RunnerActionExecutor(
        private val runner: CommandRunner,
        private val registry: ExecutorRegistry,
        override val supportedType: Int,
    ) : ActionExecutor {

        override fun execute(intent: ActionIntent): ExecOutcome {
            val normalized = mapIntent(intent)
                ?: return ExecOutcome(
                    success = false,
                    // 10xxx=格式/权限族（spec §4.3）；具体值暂定（错误码全表待 T3.2 冻结，spec §9 Q2）
                    errorCode = 101010,
                    errorReason = "本地规则意图 ${intent.commandType} 不在 T3.1 P0 意图映射内",
                )
            val report = runner.execute(normalized, registry)
            return when (report.status) {
                ExecutionStatus.SUCCEEDED -> ExecOutcome.OK
                // PAUSED 非 error 语义（FR-52）：错误码透传（PAUSED_BY_USER_TAKEOVER），
                // 引擎侧记账/上报保留「已尝试-被暂停」事实。
                ExecutionStatus.PAUSED, ExecutionStatus.FAILED -> ExecOutcome(
                    success = false,
                    errorCode = report.errorCode,
                    errorReason = report.errorReason,
                )
            }
        }
    }

    /** T3.1 只读暂停门 ← 引擎 PauseManager（app 装配时传给 CommandRunner 构造器）。 */
    class EnginePauseGate(private val engine: LocalTaskEngine) :
        cn.worktool.open.feature.executor.runtime.PauseManager {
        override fun isPaused(): Boolean = engine.pauseManager().isPaused
        override fun pauseSource(): String? = engine.pauseManager().pauseSource()
    }

    /**
     * 把 T3.1 注册面全部指令号绑定进引擎（幂等：后注册覆盖同号适配器）。
     * P0 映射：203 → SendText（FR-02）、212 → PassFriend（FR-15）；
     * 其余 T3.1 指令号（206/207/225）当前注册为 fail-closed 占位：mapIntent 暂不映射（返回
     * 101010），待 W-M3R 真机联调接线后放开——CR 收口 C2 登记，非永久限制。
     */
    fun bind(engine: LocalTaskEngine, runner: CommandRunner, registry: ExecutorRegistry) {
        registry.supportedTypes.forEach { type ->
            engine.registerExecutor(RunnerActionExecutor(runner, registry, type))
        }
    }

    /** 引擎意图 → T3.1 强类型意图（P0 子集；未覆盖返回 null → 执行面报不支持）。 */
    internal fun mapIntent(intent: ActionIntent): CommandIntent? = when (intent.commandType) {
        MessageTypes.SEND_MESSAGE -> intent.targetRoom?.let { room ->
            CommandIntent.SendText(
                targetRoom = room,
                content = intent.content ?: "",
                atList = intent.at,
            )
        }
        // FR-15：通过当前全部待处理好友请求（T3.1 语义；greeting 在 UI 自动化面无对应步骤）
        MessageTypes.PASS_ALL_FRIEND_REQUEST -> CommandIntent.PassFriend()
        else -> null
    }
}
