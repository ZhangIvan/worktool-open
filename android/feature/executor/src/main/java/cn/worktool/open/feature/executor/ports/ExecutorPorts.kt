/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.ports

import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.report.MessageReportSink
import kotlinx.serialization.json.JsonObject

/**
 * 对等计划批（docs/10 §B B1~B4）执行器外接端口集。
 *
 * 设计口径与 501/502/509 的 [QueryResultSink]/[LocalFilePort] 相同：执行器不直接
 * 触达 Android/网络/文件系统，一切外界影响经端口注入——JVM 单测用 Fake 注入
 * 断言，生产接线归 app 装配层（W-M3R/W-M5R）。**无默认空实现**（fail-safe：
 * 缺省静默丢结果属接线错误，类型系统上强制显式装配，见 ExecutorRegistry 首批注）。
 */
interface SessionControlPort {

    /**
     * 201 STOP_AND_GO_HOME：请求用户接管暂停（FR-52 联动）。
     * @return false = 接管请求被拒（已有更高优先级状态）
     */
    fun requestTakeoverPause(source: String): Boolean
}

/** 202 LOOP_RECEIVE_NEW_MESSAGE：新消息接收主循环开关（生产=DeviceSessionLoop 接线）。 */
interface ReceiveLoopPort {

    /** @return false = 目标态与当前态冲突或循环组件不可用 */
    fun setReceiveLoopEnabled(enabled: Boolean): Boolean

    /** 当前开关态（干跑报告展示用）。 */
    fun isReceiveLoopEnabled(): Boolean
}

/** 102 TYPE_CONSOLE_TOAST：控制台提示出口（FR-50 悬浮窗/系统对话框）。 */
interface ConsoleNoticePort {

    /** 展示错误提示（错误码+错误信息两行）。 */
    fun showErrorNotice(errorCode: Int, errorReason: String)

    /** 二次熔断：停止无障碍主功能并弹系统对话框。 */
    fun requestStopMainFunction(errorCode: Int, errorReason: String)
}

/** 304/305/306：端侧待执行指令队列操作（生产=指令队列调度器）。 */
interface CommandQueuePort {

    /** 清空全部待执行指令；返回被清理指令的 messageId 列表（去重）。 */
    fun clearPending(): List<String>

    /**
     * 按 messageId 集合移除待执行指令。
     * @return (实际移除涉及 id, 未找到 id)
     */
    fun removePending(messageIds: List<String>): Pair<List<String>, List<String>>

    /**
     * 待执行队列只读快照。
     * @param withDetails true=附带逐条指令 JSON 文本（深拷贝口径）
     * @return (messageId 列表 FIFO 序, 详情文本列表)
     */
    fun pendingSnapshot(withDetails: Boolean): Pair<List<String>, List<String>>
}

/** 513：设备资源快照（纯本地采集；单位 MB；外部存储缺失 = null 非 error）。 */
interface DeviceResourcePort {
    fun snapshot(): DeviceResourceSnapshot
}

data class DeviceResourceSnapshot(
    val totalMemoryMb: Long,
    val availableMemoryMb: Long,
    val appMaxMemoryMb: Long,
    val appAllocatedMemoryMb: Long,
    val totalInternalStorageMb: Long,
    val availableInternalStorageMb: Long,
    val totalExternalStorageMb: Long? = null,
    val availableExternalStorageMb: Long? = null,
    val deviceModel: String,
    val androidVersion: String,
    val sdkVersion: Int,
) {
    val usedMemoryMb: Long get() = totalMemoryMb - availableMemoryMb
    val memoryUsagePercent: Float
        get() = if (totalMemoryMb <= 0) 0f else usedMemoryMb * 100f / totalMemoryMb
}

/** 514 片段采集结果（gzip+base64 载荷 + 元数据；meta 键集对齐回执 metaJson 面）。 */
data class LogSnippet(
    val payloadBase64: String,
    val truncated: Boolean,
    val meta: Map<String, String>,
)

/** 301/302/510/514：本地日志读取（生产=日志文件系统；JVM=Fake 文件表）。 */
interface LogStorePort {

    /** 301：常规/环境日志尾部文本（≤ [maxChars]）。 */
    fun recentLogText(maxChars: Int): String

    /** 302：错误/崩溃日志尾部文本（≤ [maxChars]）。 */
    fun recentErrorText(maxChars: Int): String

    /** 510：指定日期（yyyy-MM-dd）日志打包；null = 该日无日志文件。 */
    fun collectDayLogZip(logDate: String): ByteArray?

    /** 514 messageId 模式：null = 未命中该 messageId。 */
    fun findSnippetByMessageId(targetMessageId: String): LogSnippet?

    /** 514 时间段模式：null = 该时段无日志（可能已清理）。 */
    fun findSnippetByRange(startTime: String, endTime: String): LogSnippet?
}

/** 510：日志包上传出口（生产=自建文件 API multipart 上传）。 */
interface LogUploadPort {

    /** @return false = 上传失败（网络/服务端） */
    fun uploadLogZip(fileName: String, zip: ByteArray): Boolean
}

/** 218/224(icon)/243：远端文件拉取（下载 → 按 objectName 落盘；生产=端侧网络栈）。 */
interface RemoteFilePort {

    /** @return false = 下载或落盘失败（不可达/越域/存储异常） */
    fun fetch(url: String, objectName: String): Boolean
}

/** 243 扫码入群结果（个微侧流程出口）。 */
enum class QrJoinOutcome { JOINED, QR_EXPIRED, QR_NOT_RECOGNIZED }

/** 243：二维码图片入群（下载写相册 + 个微侧扫码入群；生产=FR-56 WeChat 规则包接线）。 */
interface QrCodeJoinPort {

    /** 下载二维码图片并写入系统相册。 @return false = 下载/写相册失败。 */
    fun downloadAndSaveToAlbum(fileUrl: String): Boolean

    /** 个微「扫一扫→相册第一张→加入群聊」流程。 */
    fun weChatScanAlbumJoin(): QrJoinOutcome
}

/** 307：服务端配置全量重拉（生产=robotInfo/get 拉取链）。 */
interface AppConfigPort {

    /** @return false = 拉取失败（保持旧配置） */
    fun refreshRemoteConfig(): Boolean
}

/** 223：当前企业名（切换去重判定：已在目标企业直接成功）。 */
interface CurrentCorpPort {
    fun currentCorpName(): String?
}

/**
 * 503/504/505/506/507/508/511/512/513/515：结构化查询结果推送出口
 * （生产=消息回调通道 HTTP POST；payload 形状 = 各查询指令回调 body）。
 * 调用时序保证与 [QueryResultSink] 相同：仅计划末步调用；FAILED/PAUSED 不调用。
 */
fun interface StructuredResultSink {
    fun onStructuredResult(commandType: Int, payload: JsonObject)
}

/** 多目标指令逐格成败（204/205/221/227/229/230/235/236/238 部分成功语义）。 */
data class TargetOutcome(
    /** 目标标识（会话/群/接收人名）。 */
    val target: String,
    /** 二维矩阵格坐标（238：`会话#序号`；一维指令为 null）。 */
    val cell: String? = null,
    val succeeded: Boolean,
    val errorCode: Int? = null,
    val reason: String? = null,
)

/** 多目标指令结果出口（生产=回执 successList/failList 组装，映射以 T3.2 冻结为准）。 */
fun interface BatchOutcomeSink {
    fun onBatchOutcome(commandType: Int, outcomes: List<TargetOutcome>)
}

/** 237/514 互斥单飞护栏（清理进行中/采集进行中再触发 = MUTEX_BUSY）。 */
interface SingleFlightGuard {
    fun tryAcquire(): Boolean
    fun release()
}

/** 生产默认互斥护栏（进程内单实例；跨进程互斥归生产接线层）。 */
class AtomicSingleFlightGuard : SingleFlightGuard {
    private val held = java.util.concurrent.atomic.AtomicBoolean(false)
    override fun tryAcquire(): Boolean = held.compareAndSet(false, true)
    override fun release() {
        held.set(false)
    }
}

/**
 * 全量批执行器装配端口集（[cn.worktool.open.feature.executor.exec.ExecutorRegistry.defaultFullBatch]）。
 *
 * 无默认值：生产/测试装配方必须显式提供全部端口（fail-safe，同首批口径）。
 */
class ExecutorBindings(
    val querySink: QueryResultSink,
    val files: LocalFilePort,
    val structured: StructuredResultSink,
    val batch: BatchOutcomeSink,
    val report: MessageReportSink,
    val session: SessionControlPort,
    val loop: ReceiveLoopPort,
    val console: ConsoleNoticePort,
    val queue: CommandQueuePort,
    val resources: DeviceResourcePort,
    val logs: LogStorePort,
    val logUpload: LogUploadPort,
    val remoteFiles: RemoteFilePort,
    val qrJoin: QrCodeJoinPort,
    val appConfig: AppConfigPort,
    val corp: CurrentCorpPort,
    val cacheCleanGuard: SingleFlightGuard = AtomicSingleFlightGuard(),
    val logSnippetGuard: SingleFlightGuard = AtomicSingleFlightGuard(),
)
