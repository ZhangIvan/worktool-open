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
import cn.worktool.open.feature.executor.query.FileInfo
import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel

/**
 * 指令 509 GET_LOCAL_FILE（FR-33 上传本地文件/日志；docs/05 P0 首批行）：
 * 解析目标文件（默认当前日志，audit/02 F30 语义）→ 大小元数据 → 投递报告
 * 经 [QueryResultSink] 输出。
 *
 * **干跑语义（本任务卡冻结）**：dry-run 报告「将上传哪个文件 + 大小」——
 * 文件名/大小在 [buildPlan] 内经 [LocalFilePort] 解析（只读 stat，不读内容），
 * 落入 paramsSummary 与步骤描述，[cn.worktool.open.feature.executor.runtime.CommandRunner.dryRun]
 * 直接展示；与真实执行同引擎同路径（同一计划产物，FR-54）。
 *
 * **字节流上传通道 = dodDeferred**：端→云文件传输（分片/编码/REST 或 WS 通道）
 * 未在 protocol/spec.md 冻结（§9 待 apifox 对照）；本执行器交付到「投递报告
 * 出端口」为止，通道由 app 装配层按冻结结果接线——dry-run 语义（本卡验收）
 * 已完整。
 *
 * 无界面步骤（纯文件系统面）→ 不触碰 [cn.worktool.open.feature.executor.runtime.UiDriver]；
 * 风险级 L0（只读 stat + 报告，零写入零界面副作用；FR-53 裁决③）。
 */
class GetLocalFileExecutor(
    private val files: LocalFilePort,
    private val sink: QueryResultSink,
) : TypedCommandExecutor<CommandIntent.GetLocalFile>() {

    override val commandType: Int get() = MessageTypes.GET_LOCAL_FILE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetLocalFile): ExecutionPlan {
        // 计划期解析（干跑报告数据源）：仅 stat 元数据，不读文件内容
        val path = intent.filePath ?: files.defaultLogFilePath()
        val info = files.stat(path)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = path,
            paramsSummary = if (info != null) {
                "file=${info.name}, sizeBytes=${info.sizeBytes}"
            } else {
                "file=$path, sizeBytes=<未知：文件当前不存在>"
            },
            steps = listOf(
                resolveFileStep(path),
                emitReportStep(path),
            ),
        )
    }

    /** 执行期复检（计划与执行间文件可能被滚动删除）；缺失 → LOCAL_FILE_MISSING。 */
    private fun resolveFileStep(path: String): PlanStep = PlanStep(
        id = "resolve-file",
        description = "解析待上传文件并复核存在性与大小（$path）",
    ) { _ ->
        val info = files.stat(path)
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.LOCAL_FILE_MISSING,
                "本地文件不存在或不可读（$path）",
            )
        StepOutcome.Success
    }

    /** 投递报告出端口（文件名+路径+大小；SR-06 无涉——路径/大小非敏感正文）。 */
    private fun emitReportStep(path: String): PlanStep = PlanStep(
        id = "emit-file-report",
        description = "输出待上传文件报告（文件名+大小）至查询结果端口",
    ) { _ ->
        val info: FileInfo = files.stat(path)
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.LOCAL_FILE_MISSING,
                "本地文件在投递前消失（$path）",
            )
        sink.onQueryResult(
            commandType,
            QueryResult.LocalFile(fileName = info.name, path = info.path, sizeBytes = info.sizeBytes),
        )
        StepOutcome.Success
    }
}
