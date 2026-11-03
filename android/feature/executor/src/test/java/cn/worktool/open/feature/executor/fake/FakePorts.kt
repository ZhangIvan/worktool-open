/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fake

import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.feature.executor.query.FileInfo
import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.report.MessageReportSink
import kotlinx.serialization.json.JsonObject

/**
 * FR-01 / 查询类执行器共用测试假件（端口注入面）。
 */

/** 上报出口假件：记录全部投递尝试；[accept]=false 模拟离线/会话未建。 */
class FakeReportSink(
    var accept: Boolean = true,
) : MessageReportSink {

    /** 一次投递尝试（被拒也记录，断言重试/保序语义用）。 */
    data class Attempt(val beans: List<WeworkMessageBean>, val accepted: Boolean)

    val attempts = mutableListOf<Attempt>()

    override fun sendReport(beans: List<WeworkMessageBean>): Boolean {
        attempts += Attempt(beans, accept)
        return accept
    }

    /** 全部尝试的 bean（含被拒轮次）。 */
    fun allBeans(): List<WeworkMessageBean> = attempts.flatMap { it.beans }

    /** 仅成功接受的 bean（顺序 = 接受序）。 */
    fun acceptedBeans(): List<WeworkMessageBean> =
        attempts.filter { it.accepted }.flatMap { it.beans }
}

/** 查询结果端口假件：按序记录结构化结果。 */
class RecordingQuerySink : QueryResultSink {

    val results = mutableListOf<Pair<Int, QueryResult>>()

    override fun onQueryResult(commandType: Int, result: QueryResult) {
        results += commandType to result
    }
}

/** 本地文件端口假件：路径 → 大小表 + 可选默认日志路径。 */
class FakeLocalFiles(
    private val sizes: Map<String, Long>,
    private val defaultLog: String? = null,
) : LocalFilePort {

    override fun defaultLogFilePath(): String =
        defaultLog ?: error("测试未配置默认日志路径")

    override fun stat(path: String): FileInfo? = sizes[path]?.let { size ->
        FileInfo(
            name = path.substringAfterLast('/'),
            path = path,
            sizeBytes = size,
        )
    }
}

// ---- 对等计划批（docs/10 §B B1~B4）端口假件 ----

/** 201：接管暂停假件（记录请求；[accept]=false 模拟被拒）。 */
class FakeSessionControl(var accept: Boolean = true) :
    cn.worktool.open.feature.executor.ports.SessionControlPort {
    val requests = mutableListOf<String>()
    override fun requestTakeoverPause(source: String): Boolean {
        requests += source
        return accept
    }
}

/** 202：主循环开关假件（初始态可设；设置成功返回 true 并翻转）。 */
class FakeReceiveLoop(var enabled: Boolean = true, var refuse: Boolean = false) :
    cn.worktool.open.feature.executor.ports.ReceiveLoopPort {
    override fun setReceiveLoopEnabled(enabled: Boolean): Boolean {
        if (refuse) return false
        this.enabled = enabled
        return true
    }

    override fun isReceiveLoopEnabled(): Boolean = enabled
}

/** 102：控制台提示假件（记录弹窗/停机调用序）。 */
class FakeConsoleNotice :
    cn.worktool.open.feature.executor.ports.ConsoleNoticePort {
    val notices = mutableListOf<Pair<Int, String>>()
    val stopRequests = mutableListOf<Pair<Int, String>>()
    var mainFunctionStopped = false
        private set

    override fun showErrorNotice(errorCode: Int, errorReason: String) {
        notices += errorCode to errorReason
    }

    override fun requestStopMainFunction(errorCode: Int, errorReason: String) {
        stopRequests += errorCode to errorReason
        mainFunctionStopped = true
    }
}

/** 304/305/306：指令队列假件（FIFO 待执行表，可预置 messageId）。 */
class FakeCommandQueue(pending: List<String> = emptyList()) :
    cn.worktool.open.feature.executor.ports.CommandQueuePort {
    val queue = ArrayDeque(pending)
    val details = mutableMapOf<String, String>()
    val clearedIds = mutableListOf<String>()
    val removedIds = mutableListOf<String>()

    override fun clearPending(): List<String> {
        val ids = queue.distinct().toList()
        queue.clear()
        clearedIds += ids
        return ids
    }

    override fun removePending(messageIds: List<String>): Pair<List<String>, List<String>> {
        val removed = messageIds.filter { queue.remove(it) }
        removedIds += removed
        val missing = messageIds.filter { it !in removed }
        return removed to missing
    }

    override fun pendingSnapshot(withDetails: Boolean): Pair<List<String>, List<String>> {
        val ids = queue.toList()
        val texts = if (withDetails) ids.map { details[it] ?: "{}" } else emptyList()
        return ids to texts
    }
}

/** 513：设备资源假件。 */
class FakeDeviceResources(
    private val snapshot: cn.worktool.open.feature.executor.ports.DeviceResourceSnapshot,
) : cn.worktool.open.feature.executor.ports.DeviceResourcePort {
    var queried = 0
        private set
    override fun snapshot(): cn.worktool.open.feature.executor.ports.DeviceResourceSnapshot {
        queried++
        return snapshot
    }
}

/** 301/302/510/514：日志存取假件（文本 + 片段表 + 日包表）。 */
class FakeLogStore(
    private val recentText: String = "环境快照: appVersion=1.0.0 workVersion=1.0.0",
    private val errorText: String = "未捕获异常: KotlinNullPointerException at Main.kt:42",
    private val dayZips: Map<String, ByteArray> = emptyMap(),
    private val snippetsByMessageId: Map<String, cn.worktool.open.feature.executor.ports.LogSnippet> = emptyMap(),
    private val snippetsByRange: List<Pair<Pair<String, String>, cn.worktool.open.feature.executor.ports.LogSnippet>> = emptyList(),
) : cn.worktool.open.feature.executor.ports.LogStorePort {
    override fun recentLogText(maxChars: Int): String = recentText.take(maxChars)
    override fun recentErrorText(maxChars: Int): String = errorText.take(maxChars)
    override fun collectDayLogZip(logDate: String): ByteArray? = dayZips[logDate]
    override fun findSnippetByMessageId(targetMessageId: String) = snippetsByMessageId[targetMessageId]
    override fun findSnippetByRange(startTime: String, endTime: String): cn.worktool.open.feature.executor.ports.LogSnippet? =
        snippetsByRange.firstOrNull { it.first == startTime to endTime }?.second
}

/** 510：日志上传假件（[accept]=false 模拟上传失败）。 */
class FakeLogUpload(var accept: Boolean = true) :
    cn.worktool.open.feature.executor.ports.LogUploadPort {
    val uploaded = mutableListOf<String>()
    override fun uploadLogZip(fileName: String, zip: ByteArray): Boolean {
        uploaded += fileName
        return accept
    }
}

/** 218/243：远端文件假件（可达 URL 表；越域/404 = false）。 */
class FakeRemoteFiles(private val reachable: Set<String>) :
    cn.worktool.open.feature.executor.ports.RemoteFilePort {
    val fetched = mutableListOf<Pair<String, String>>()
    override fun fetch(url: String, objectName: String): Boolean {
        fetched += url to objectName
        return url in reachable
    }
}

/** 243：扫码入群假件（albumOk 控制写相册；joinOutcome 控制个微侧结果）。 */
class FakeQrJoin(
    private val albumOk: Boolean = true,
    private val joinOutcome: cn.worktool.open.feature.executor.ports.QrJoinOutcome =
        cn.worktool.open.feature.executor.ports.QrJoinOutcome.JOINED,
) : cn.worktool.open.feature.executor.ports.QrCodeJoinPort {
    val savedUrls = mutableListOf<String>()
    var joinAttempts = 0
        private set
    override fun downloadAndSaveToAlbum(fileUrl: String): Boolean {
        savedUrls += fileUrl
        return albumOk
    }

    override fun weChatScanAlbumJoin(): cn.worktool.open.feature.executor.ports.QrJoinOutcome {
        joinAttempts++
        return joinOutcome
    }
}

/** 307：配置重拉假件。 */
class FakeAppConfig(var refreshOk: Boolean = true) :
    cn.worktool.open.feature.executor.ports.AppConfigPort {
    var refreshCount = 0
        private set
    override fun refreshRemoteConfig(): Boolean {
        refreshCount++
        return refreshOk
    }
}

/** 223：当前企业名假件。 */
class FakeCurrentCorp(var corp: String?) :
    cn.worktool.open.feature.executor.ports.CurrentCorpPort {
    override fun currentCorpName(): String? = corp
}

/** 结构化查询结果推送假件（记录 commandType + payload）。 */
class RecordingStructuredSink :
    cn.worktool.open.feature.executor.ports.StructuredResultSink {
    val results = mutableListOf<Pair<Int, JsonObject>>()
    override fun onStructuredResult(commandType: Int, payload: JsonObject) {
        results += commandType to payload
    }
}

/** 多目标逐格成败假件。 */
class RecordingBatchSink :
    cn.worktool.open.feature.executor.ports.BatchOutcomeSink {
    val batches = mutableListOf<Pair<Int, List<cn.worktool.open.feature.executor.ports.TargetOutcome>>>()
    override fun onBatchOutcome(
        commandType: Int,
        outcomes: List<cn.worktool.open.feature.executor.ports.TargetOutcome>,
    ) {
        batches += commandType to outcomes
    }
}

/** 互斥护栏假件（可预置占用态，验证 MUTEX_BUSY 分支）。 */
class FakeSingleFlightGuard(var held: Boolean = false) :
    cn.worktool.open.feature.executor.ports.SingleFlightGuard {
    override fun tryAcquire(): Boolean {
        if (held) return false
        held = true
        return true
    }

    override fun release() {
        held = false
    }
}

/** 全量批端口假件聚合（各字段可按测试覆写）。 */
fun testBindings(
    querySink: RecordingQuerySink = RecordingQuerySink(),
    files: FakeLocalFiles = FakeLocalFiles(mapOf(), null),
    structured: RecordingStructuredSink = RecordingStructuredSink(),
    batch: RecordingBatchSink = RecordingBatchSink(),
    report: FakeReportSink = FakeReportSink(),
    session: FakeSessionControl = FakeSessionControl(),
    loop: FakeReceiveLoop = FakeReceiveLoop(),
    console: FakeConsoleNotice = FakeConsoleNotice(),
    queue: FakeCommandQueue = FakeCommandQueue(),
    resources: FakeDeviceResources = FakeDeviceResources(defaultResourceSnapshot()),
    logs: FakeLogStore = FakeLogStore(),
    logUpload: FakeLogUpload = FakeLogUpload(),
    remoteFiles: FakeRemoteFiles = FakeRemoteFiles(emptySet()),
    qrJoin: FakeQrJoin = FakeQrJoin(),
    appConfig: FakeAppConfig = FakeAppConfig(),
    corp: FakeCurrentCorp = FakeCurrentCorp("A公司"),
    cacheCleanGuard: FakeSingleFlightGuard = FakeSingleFlightGuard(),
    logSnippetGuard: FakeSingleFlightGuard = FakeSingleFlightGuard(),
): cn.worktool.open.feature.executor.ports.ExecutorBindings =
    cn.worktool.open.feature.executor.ports.ExecutorBindings(
        querySink = querySink,
        files = files,
        structured = structured,
        batch = batch,
        report = report,
        session = session,
        loop = loop,
        console = console,
        queue = queue,
        resources = resources,
        logs = logs,
        logUpload = logUpload,
        remoteFiles = remoteFiles,
        qrJoin = qrJoin,
        appConfig = appConfig,
        corp = corp,
        cacheCleanGuard = cacheCleanGuard,
        logSnippetGuard = logSnippetGuard,
    )

/** 513 测试默认快照（数值满足 total=available+used 恒等式）。 */
fun defaultResourceSnapshot(): cn.worktool.open.feature.executor.ports.DeviceResourceSnapshot =
    cn.worktool.open.feature.executor.ports.DeviceResourceSnapshot(
        totalMemoryMb = 15190,
        availableMemoryMb = 4660,
        appMaxMemoryMb = 512,
        appAllocatedMemoryMb = 128,
        totalInternalStorageMb = 118_000,
        availableInternalStorageMb = 60_000,
        totalExternalStorageMb = null,
        availableExternalStorageMb = null,
        deviceModel = "Synthetic-Model-X",
        androidVersion = "16",
        sdkVersion = 36,
    )
