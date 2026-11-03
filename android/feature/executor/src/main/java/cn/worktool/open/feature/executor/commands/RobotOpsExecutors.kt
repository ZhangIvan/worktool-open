/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.AppConfigPort
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.ports.CommandQueuePort
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.ports.TargetOutcome
import cn.worktool.open.feature.executor.report.MessageReportSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 301 ROBOT_LOG / 302 ROBOT_ERROR_LOG（docs/10 B3，L0；端→云出站通道的
 * 本地触发面）：取日志尾部文本（intent 指定优先）→ SR-06 口径由端口实现方
 * 负责脱敏 → 经 [MessageReportSink] WS 上报（type=301/302，socketType=2）。
 */
open class RobotLogExecutorBase(
    private val commandTypeValue: Int,
    private val sink: MessageReportSink,
    private val logTextProvider: () -> String,
    private val what: String,
) : TypedCommandExecutor<CommandIntent.RobotLogIntent>() {

    final override val commandType: Int get() = commandTypeValue

    final override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    final override fun buildTypedPlan(intent: CommandIntent.RobotLogIntent): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "channel=$what, logText=${if (intent.logText != null) "指定" else "尾部快照"}",
            steps = listOf(reportStep(intent)),
        )

    private fun reportStep(intent: CommandIntent.RobotLogIntent): PlanStep = PlanStep(
        id = "report-log",
        description = "组装${what}载荷并经 WS 通道上报（SR-06 脱敏归端口实现方）",
    ) { _ ->
        val text = intent.logText?.takeIf { it.isNotBlank() } ?: logTextProvider()
        if (text.isBlank()) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "无可用${what}文本（日志为空）",
            )
        }
        val bean = cn.worktool.open.core.protocol.WeworkMessageBean(
            type = commandType,
            extras = mapOf("log" to kotlinx.serialization.json.JsonPrimitive(text.take(MAX_LOG_CHARS))),
        )
        if (!sink.sendReport(listOf(bean))) {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "${what}上报通道不可投递")
        } else {
            StepOutcome.Success
        }
    }

    private companion object {
        const val MAX_LOG_CHARS = 8_192
    }
}

/** 301 常规/环境运行日志上报。 */
class RobotLogExecutor(
    sink: MessageReportSink,
    logs: cn.worktool.open.feature.executor.ports.LogStorePort,
) : RobotLogExecutorBase(
    commandTypeValue = MessageTypes.ROBOT_LOG,
    sink = sink,
    logTextProvider = { logs.recentLogText(MAX_CHARS) },
    what = "运行日志",
) {
    private companion object {
        const val MAX_CHARS = 8_192
    }
}

/** 302 错误/崩溃日志上报。 */
class RobotErrorLogExecutor(
    sink: MessageReportSink,
    logs: cn.worktool.open.feature.executor.ports.LogStorePort,
) : RobotLogExecutorBase(
    commandTypeValue = MessageTypes.ROBOT_ERROR_LOG,
    sink = sink,
    logTextProvider = { logs.recentErrorText(MAX_CHARS) },
    what = "错误日志",
) {
    private companion object {
        const val MAX_CHARS = 8_192
    }
}

/**
 * 指令 304 ROBOT_QUEUE_CLEAR 清空待执行指令队列（docs/10 B3，**L2**；被清理
 * 指令逐条回 101018 族由回执层组装，端侧队列操作经端口）：
 * clearPending → 被清理 messageId 集合经端口出（successList 数据源）。
 * 上游语义：内联处理不经过暂停门——本侧统一走 CommandRunner（FR-52 门在，
 * 治理差异=我方增强，验收口径注明）。
 */
class RobotQueueClearExecutor(
    private val queue: CommandQueuePort,
    private val batch: BatchOutcomeSink,
) : TypedCommandExecutor<CommandIntent.RobotQueueClear>() {

    override val commandType: Int get() = MessageTypes.ROBOT_QUEUE_CLEAR

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L2

    override fun buildTypedPlan(intent: CommandIntent.RobotQueueClear): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "clear=all-pending",
            steps = listOf(clearStep()),
        )

    private fun clearStep(): PlanStep = PlanStep(
        id = "clear-pending",
        description = "清空全部待执行指令（被清理者逐条 101018 回执归回执层）",
    ) { _ ->
        val cleared = queue.clearPending()
        batch.onBatchOutcome(
            commandType,
            cleared.map { TargetOutcome(target = it, succeeded = true) },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 305 ROBOT_QUEUE_REMOVE 清除指定待执行指令（docs/10 B3，L0；
 * originalContent=messageId 中英逗号分隔批量）：移除 → 实际取消集/未找到集
 * 出端口；**自身回执恒成功**（幂等友好，不存在的 id 不报错）。
 */
class RobotQueueRemoveExecutor(
    private val queue: CommandQueuePort,
    private val batch: BatchOutcomeSink,
) : TypedCommandExecutor<CommandIntent.RobotQueueRemove>() {

    override val commandType: Int get() = ExtendedCommandTypes.ROBOT_QUEUE_REMOVE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.RobotQueueRemove): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "ids=${intent.messageIds.size}",
            steps = listOf(removeStep(intent)),
        )

    private fun removeStep(intent: CommandIntent.RobotQueueRemove): PlanStep = PlanStep(
        id = "remove-pending",
        description = "按 messageId 移除待执行指令（未找到的 id 记说明不报错）",
    ) { _ ->
        val (removed, notFound) = queue.removePending(intent.messageIds)
        batch.onBatchOutcome(
            commandType,
            removed.map { TargetOutcome(target = it, succeeded = true) } +
                notFound.map { TargetOutcome(target = it, succeeded = false, reason = "未在待执行队列找到") },
        )
        StepOutcome.Success // 幂等友好：恒成功（上游语义）
    }
}

/**
 * 指令 306 ROBOT_QUEUE_GET 查询待执行队列（docs/10 B3，L0；只读快照）：
 * pendingSnapshot → messageIdList（+ showMessageHistory=true 附 commandList）推送。
 */
class RobotQueueGetExecutor(
    private val queue: CommandQueuePort,
    private val structured: StructuredResultSink,
) : TypedCommandExecutor<CommandIntent.RobotQueueGet>() {

    override val commandType: Int get() = ExtendedCommandTypes.ROBOT_QUEUE_GET

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.RobotQueueGet): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "showMessageHistory=${intent.showMessageHistory}",
            steps = listOf(snapshotStep(intent)),
        )

    private fun snapshotStep(intent: CommandIntent.RobotQueueGet): PlanStep = PlanStep(
        id = "snapshot-pending",
        description = "只读快照待执行队列（messageIdList${if (intent.showMessageHistory) " + commandList 深拷贝" else ""}）",
    ) { _ ->
        val (ids, details) = queue.pendingSnapshot(intent.showMessageHistory)
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("messageIdList", JsonArray(ids.map { JsonPrimitive(it) }))
                if (intent.showMessageHistory) {
                    put("commandList", JsonArray(details.map { JsonPrimitive(it) }))
                }
            },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 307 ROBOT_CONFIG_UPDATED 配置更新通知（docs/10 B3，L0；**载荷忽略**——
 * 通知即全量重拉，上游极简设计）：AppConfigPort.refreshRemoteConfig；
 * 失败=保持旧配置（fail 报告，无回执语义归上游）。
 */
class RobotConfigUpdatedExecutor(
    private val appConfig: AppConfigPort,
) : TypedCommandExecutor<CommandIntent.RobotConfigUpdated>() {

    override val commandType: Int get() = ExtendedCommandTypes.ROBOT_CONFIG_UPDATED

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.RobotConfigUpdated): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "payload=ignored(全量重拉)",
            steps = listOf(refreshStep()),
        )

    private fun refreshStep(): PlanStep = PlanStep(
        id = "refresh-remote-config",
        description = "收到配置更新通知，全量重拉服务端配置（robotInfo/get）",
    ) { _ ->
        if (appConfig.refreshRemoteConfig()) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(
                ExecutorErrorCodes.ACTION_REJECTED,
                "配置重拉失败（保持旧配置，无自动重试帧）",
            )
        }
    }
}

/**
 * 指令 303 ROBOT_CONTROLLER_TEST 控制器连通性测试（docs/10 B3，L0）：
 * **回显即测试**——整条 bean JSON 进设备日志，无 UI 动作、无执行回执
 * （上游分支语义保持；观测面=日志/514 片段回收）。
 */
class RobotControllerTestExecutor(
    private val log: (String) -> Unit = {},
) : TypedCommandExecutor<CommandIntent.RobotControllerTest>() {

    override val commandType: Int get() = MessageTypes.ROBOT_CONTROLLER_TEST

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.RobotControllerTest): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "echo=${intent.echo ?: "无"}",
            steps = listOf(echoStep(intent)),
        )

    private fun echoStep(intent: CommandIntent.RobotControllerTest): PlanStep = PlanStep(
        id = "echo-to-log",
        description = "把探活 bean 回显到设备日志（链路贯通=通道健康）",
    ) { _ ->
        log("controller-test echo=${intent.echo.orEmpty()} type=$commandType")
        StepOutcome.Success
    }
}
