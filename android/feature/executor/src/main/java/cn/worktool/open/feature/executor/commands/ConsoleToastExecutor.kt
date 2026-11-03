/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.ConsoleNoticePort
import cn.worktool.open.feature.executor.risk.RiskLevel

/**
 * 指令 102 TYPE_CONSOLE_TOAST（docs/10 B3：服务端→端错误弹窗通知）：
 * 服务端向端推送错误提示；**60s 节流 + 二次熔断**——
 * - 窗口外首条：仅记录（无弹窗，主功能不受影响）；
 * - 60s 窗口内第二条：停止无障碍主功能 + 弹系统对话框（错误码/错误信息两行）；
 * - 已停机后再次到达：幂等（维持停机态，重复弹提示）。
 *
 * 本指令**不产生回执**（上游分支无回执调用，契约语义保持）——执行状态恒为
 * SUCCEEDED（节流/熔断是端侧保护语义，非指令失败）。
 *
 * 风险级（FR-53）：L0（本地提示面，零企微副作用；docs/10 B3 行）。
 *
 * 时钟：经构造注入（JVM 单测假时钟确定性复现 60s 窗口）。
 */
class ConsoleToastExecutor(
    private val console: ConsoleNoticePort,
    private val clock: () -> Long = { System.currentTimeMillis() },
) : TypedCommandExecutor<CommandIntent.ConsoleToast>() {

    override val commandType: Int get() = MessageTypes.TYPE_CONSOLE_TOAST

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    /** 上次提示时间戳（null=冷启动未收过）。 */
    private var lastNoticeAtMillis: Long? = null

    /** 是否已触发二次熔断（主功能停机态）。 */
    private var mainFunctionStopped: Boolean = false

    override fun buildTypedPlan(intent: CommandIntent.ConsoleToast): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "errorCode=${intent.errorCode ?: 0}, errorReason.length=${intent.errorReason?.length ?: 0}",
            steps = listOf(noticeStep(intent)),
        )

    private fun noticeStep(intent: CommandIntent.ConsoleToast): PlanStep = PlanStep(
        id = "console-notice",
        description = "按 60s 节流规则处置服务端错误提示（首条=仅记录；窗口内第二条=熔断停机+弹窗）",
    ) { _ ->
        val code = intent.errorCode ?: 0
        val reason = intent.errorReason.orEmpty()
        val now = clock()
        val withinWindow = lastNoticeAtMillis?.let { now - it < THROTTLE_WINDOW_MS } == true
        if (withinWindow || mainFunctionStopped) {
            console.requestStopMainFunction(code, reason)
            console.showErrorNotice(code, reason)
            mainFunctionStopped = true
        }
        lastNoticeAtMillis = now
        StepOutcome.Success
    }

    private companion object {
        /** 节流窗口（毫秒）：窗口内第二条触发熔断。 */
        const val THROTTLE_WINDOW_MS: Long = 60_000
    }
}
