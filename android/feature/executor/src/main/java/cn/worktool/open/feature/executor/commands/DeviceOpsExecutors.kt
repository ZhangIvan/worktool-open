/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.DeviceResourcePort
import cn.worktool.open.feature.executor.ports.LogSnippet
import cn.worktool.open.feature.executor.ports.LogStorePort
import cn.worktool.open.feature.executor.ports.LogUploadPort
import cn.worktool.open.feature.executor.ports.SingleFlightGuard
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 513 GET_DEVICE_RESOURCE 设备资源信息查询（docs/10 B3，L0）：
 * **纯本地采集，不操作企微 UI**（内存/存储/系统信息）→ 结构化推送。
 * 外部存储缺失 = null 字段（非 error）。
 */
class GetDeviceResourceExecutor(
    private val resources: DeviceResourcePort,
    private val structured: StructuredResultSink,
) : TypedCommandExecutor<CommandIntent.GetDeviceResource>() {

    override val commandType: Int get() = ExtendedCommandTypes.GET_DEVICE_RESOURCE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetDeviceResource): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "ui=无（纯本地采集）",
            steps = listOf(collectAndPushStep()),
        )

    private fun collectAndPushStep(): PlanStep = PlanStep(
        id = "collect-and-push",
        description = "采集内存/存储/系统信息快照并推送（单位 MB）",
    ) { _ ->
        val s = resources.snapshot()
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put(
                    "data",
                    buildJsonObject {
                        put("totalMemory", s.totalMemoryMb)
                        put("availableMemory", s.availableMemoryMb)
                        put("usedMemory", s.usedMemoryMb)
                        put("appMaxMemory", s.appMaxMemoryMb)
                        put("appAllocatedMemory", s.appAllocatedMemoryMb)
                        put("totalInternalStorage", s.totalInternalStorageMb)
                        put("availableInternalStorage", s.availableInternalStorageMb)
                        put("totalExternalStorage", s.totalExternalStorageMb)
                        put("availableExternalStorage", s.availableExternalStorageMb)
                        put("deviceModel", s.deviceModel)
                        put("androidVersion", s.androidVersion)
                        put("sdkVersion", s.sdkVersion)
                        put("memoryUsagePercent", s.memoryUsagePercent.toDouble())
                    },
                )
            },
        )
        StepOutcome.Success
    }
}

/**
 * 指令 514 GET_LOG_SNIPPET 获取日志片段（docs/10 B3，L0；双模式：
 * messageId 定位（objectName）/ 时间段 [logStartTime, logEndTime]；**互斥单飞**）：
 * 单步 try/finally 采集→gzip+base64 载荷与 meta 组装（截断=失败码族）→ 推送。
 * 未命中/空/截断三义均映射 NODE_NOT_FOUND 族（上游 201104），reason 区分场景。
 */
class GetLogSnippetExecutor(
    private val logs: LogStorePort,
    private val structured: StructuredResultSink,
    private val guard: SingleFlightGuard,
) : TypedCommandExecutor<CommandIntent.GetLogSnippet>() {

    override val commandType: Int get() = ExtendedCommandTypes.GET_LOG_SNIPPET

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetLogSnippet): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=${if (intent.targetMessageId != null) "messageId" else "timeRange"}, " +
                "target=${intent.targetMessageId ?: "${intent.logStartTime}~${intent.logEndTime}"}",
            steps = listOf(collectStep(intent)),
        )

    private fun collectStep(intent: CommandIntent.GetLogSnippet): PlanStep = PlanStep(
        id = "collect-snippet",
        description = "互斥采集日志片段（messageId/时间段）并推送 gzip+base64 载荷与 meta",
    ) { _ ->
        if (!guard.tryAcquire()) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.MUTEX_BUSY, "正在采集日志，请稍后重试")
        }
        try {
            val snippet: LogSnippet? = if (intent.targetMessageId != null) {
                logs.findSnippetByMessageId(intent.targetMessageId)
            } else {
                logs.findSnippetByRange(intent.logStartTime.orEmpty(), intent.logEndTime.orEmpty())
            }
            when {
                snippet == null -> StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    if (intent.targetMessageId != null) {
                        "未找到messageId对应日志片段: ${intent.targetMessageId}"
                    } else {
                        "本地未找到该时段日志（可能已清理）"
                    },
                )
                snippet.truncated -> StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "已达到日志大小上限，请缩小时间范围",
                )
                else -> {
                    structured.onStructuredResult(
                        commandType,
                        buildJsonObject {
                            put("fileBase64", snippet.payloadBase64)
                            // B3-514 收口：metaJson=JSON 对象（原 k=v 逗号串与契约不符）
                            put(
                                "metaJson",
                                buildJsonObject {
                                    snippet.meta.forEach { (k, v) -> put(k, v) }
                                },
                            )
                        },
                    )
                    StepOutcome.Success
                }
            }
        } finally {
            guard.release()
        }
    }
}

/**
 * 指令 510 UPLOAD_LOG_FILE 上传日志文件（docs/10 B3，L0；logDate 缺省=今天）：
 * 收集该日期日志打 zip（命名 log_yyyy_MM_dd.zip）→ multipart 上传出口。
 * 无该日日志 → NODE_NOT_FOUND；上传失败 → ACTION_REJECTED。
 */
class UploadLogFileExecutor(
    private val logs: LogStorePort,
    private val logUpload: LogUploadPort,
) : TypedCommandExecutor<CommandIntent.UploadLogFile>() {

    override val commandType: Int get() = ExtendedCommandTypes.UPLOAD_LOG_FILE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.UploadLogFile): ExecutionPlan {
        val date = intent.logDate ?: "今天"
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "logDate=$date",
            steps = listOf(collectStep(intent), uploadStep(intent)),
        )
    }

    private fun collectStep(intent: CommandIntent.UploadLogFile): PlanStep = PlanStep(
        id = "collect-day-zip",
        description = "收集 ${intent.logDate ?: "今天"} 日志并打包 log_${intent.logDate ?: ""}.zip",
    ) { _ ->
        val zip = logs.collectDayLogZip(intent.logDate ?: java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.US).format(java.util.Date()))
        if (zip == null) {
            StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "未找到指定日期的日志文件: ${intent.logDate ?: "今天"}",
            )
        } else {
            pendingZip = zip
            StepOutcome.Success
        }
    }

    private fun uploadStep(intent: CommandIntent.UploadLogFile): PlanStep = PlanStep(
        id = "upload-zip",
        description = "multipart 上传日志包（robotId/clientVersion/deviceInfo form 字段）",
    ) { _ ->
        val zip = pendingZip
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "日志包缺失（收集步未产出）")
        val name = "log_${intent.logDate ?: java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.US).format(java.util.Date())}.zip"
        if (!logUpload.uploadLogZip(name, zip)) {
            StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "处理日志上传时发生异常（上传失败）")
        } else {
            StepOutcome.Success
        }
    }

    /** 收集步→上传步的包传递（单执行器实例；顺序执行下安全）。 */
    private var pendingZip: ByteArray? = null
}
