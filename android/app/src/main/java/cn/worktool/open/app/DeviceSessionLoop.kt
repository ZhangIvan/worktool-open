/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.ControlFrame
import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.ProtocolClient
import cn.worktool.open.core.protocol.session.SessionCrypto
import cn.worktool.open.core.protocol.V2Frame
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.delivery.InboundCommandGate
import cn.worktool.open.core.protocol.delivery.JournalStore
import cn.worktool.open.core.protocol.transport.Scheduler
import cn.worktool.open.core.protocol.validation.ControlPayloadValidator
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.intent.IntentNormalizer
import cn.worktool.open.feature.executor.intent.NormalizedIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.PauseManager
import cn.worktool.open.feature.executor.runtime.UiDriver
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.concurrent.atomic.AtomicLong

/**
 * 设备执行回路（T3.4 任务③，W-M4 第一优先级的端侧接线）：
 *
 * ```
 * 会话 onFrame（FrameVerifier 已验签/解密）
 *   → IntentNormalizer（载荷规范化）
 *   → ExecutorRegistry（指令号路由）
 *   → CommandRunner（真机 UiDriver = [AccessibilityUiDriver]）
 *   → 执行结果经 AtLeastOnce 队列（先落盘后发送）
 *   → socketType=3 RAW_CONFIRM 回报（metaJson.dispatchId 回填）→ 服务端 E10 闭环
 * ```
 *
 * 控制帧（spec §5.7 T3.4 冻结）：
 * - hello_ack：会话建立信号（resumeFromSeq/serverTime 可读；接收计数器已由协议层按
 *   lastReceivedSeq 预置，spec §5.4.2 v1.2.0）；
 * - 903 RESYNC：服务端随 4405 关流重放——重连机制自动以持久确认点重发，此处仅留痕；
 * - 904 request：以端侧执行台账回 904 补报（journal_replay，spec §5.4.5）；
 * - 901/902：凭据轮换/撤销回调（v0.1 无服务端生产路径，回调消费面按冻结 schema 先行）。
 *
 * 入向门禁：[InboundCommandGate]（journal 持久化 + commandId 去重 + epoch 拒旧）
 * **先于**副作用（state-transitions.csv E09 guard）。暂停（FR-52）：CommandRunner
 * 返回 PAUSED 时不发终态回执（只入队语义归上层调度；服务端侧经 904/对账收敛）。
 *
 * 已知边界（如实登记，dodDeferred）：入向确认 seq 的跨进程持久化（SQLite schema 扩列）
 * 与执行台账持久化归 core:protocol-android 车道（T2.2 领地），当前为进程内状态 +
 * journal 出向持久；跨重启由服务端自持久确认点补发 + commandId 去重兜底（不丢不重）。
 */
class DeviceSessionLoop(
    private val credentials: DeviceCredentials,
    private val baseUrl: String,
    private val sessionCrypto: SessionCrypto?,
    /** 出向 AtLeastOnce journal + 入向命令门禁持久层（真机 = SQLite；JVM 测试 = 内存实现）。 */
    private val journal: JournalStore,
    private val scheduler: Scheduler,
    private val clock: () -> Long,
    /** 执行器注册表（单一事实来源：IntentNormalizer 支持集取自 registry.supportedTypes）。 */
    private val registry: ExecutorRegistry,
    /** 界面驱动提供者：真机 = AccessibilityUiDriver；JVM 级联测试 = Fake 驱动。 */
    private val driverProvider: () -> UiDriver,
    private val pauseManager: PauseManager,
    clientVersion: String = "0.1.0",
    /** 传输工厂注入（JVM 级联测试 = Fake；真机默认 OkHttp）。 */
    private val transportFactory: cn.worktool.open.core.protocol.transport.WsTransport.Factory? = null,
    /** 901 凭据轮换回调（新凭据由外部管理者替换存储并重建会话，spec §5.5.1）。 */
    private val onCredentialRotated: ((rotatedTokenHex: String, tokenVersion: Int, serverX25519PublicKeyHex: String) -> Unit)? = null,
    /** 902 撤销回调（外部走强制重配对，spec §5.5.2）。 */
    private val onCredentialRevoked: (() -> Unit)? = null,
    /** 回路事件观察口（审计/日志面；不参与控制流）。 */
    private val onEvent: ((String) -> Unit)? = null,
    /**
     * 执行回执结构化观察口（产品冲刺 A 车道：主界面指令列表「上次执行状态」数据源）。
     * 与 onEvent 的行文本解耦（文本面向日志，本回调面向 UI 状态机）；不参与控制流。
     */
    private val onExecResult: ((commandId: String, commandType: Int, errorCode: Int, success: Boolean) -> Unit)? = null,
    /**
     * FR-54 端侧干跑门（产品冲刺 A 车道：主界面快捷操作「干跑开关」经
     * SessionRuntime.dryRunEnabled 注入；null/false = 真实执行）。
     * 开 = 指令经 feature:executor CommandRunner.dryRun（同引擎同路径仅构建计划，
     * 零副作用）产出报告，并回 RAW_CONFIRM（errorCode=0）+ metaJson.deviceDryRun=true
     * 标记。口径：干跑无副作用属安全操作，不经暂停门拦截（与 CommandRunner.dryRun
     * 语义一致）；端侧干跑为设备主人的本地预览决定，与服务端「干跑绝不下发设备」
     * （server scheduler pipeline，FR-54）互不冲突——服务端干跑指令不会到达本门。
     */
    private val dryRunGate: (() -> Boolean)? = null,
) {

    private val codec = EnvelopeCodec()
    private val normalizer = IntentNormalizer(registry.supportedTypes)
    private val runner = CommandRunner(driver = driverProvider(), pauseManager = pauseManager)

    /** 端侧已确认的最高服务端→设备 seq（hello.lastReceivedSeq 来源；逐帧推进）。 */
    private val lastReceivedSeq = AtomicLong(0L)

    /** 执行台账（904 补报数据源）：commandId → 条目。 */
    private val replayLedger = LinkedHashMap<String, ReplayEntry>()

    private data class ReplayEntry(val epoch: Long, val started: Boolean, var outcome: String)

    /** 入向准入上下文（admit → gate.apply → execute 的传参桥，单线程会话回调内使用）。 */
    private var pendingBean: WeworkMessageBean? = null
    private var pendingEpoch: Long = 0L

    /** 入向命令门禁（journal 持久化先于副作用，E09 guard）。 */
    private val gate = InboundCommandGate(journal) { command -> executeCommand(command.commandId, command.type) }

    private val session: ProtocolClient.V2Session = ProtocolClient.V2Session(
        credentials = credentials,
        baseUrl = baseUrl,
        sessionCrypto = sessionCrypto,
        journal = journal,
        scheduler = scheduler,
        clock = clock,
        capabilities = ProtocolClient.Capabilities(
            clientVersion = clientVersion,
            encryptTypeMax = if (sessionCrypto != null) 2 else 0,
            commands = registry.supportedTypes.sorted(),
        ),
        lastEpoch = { journal.appliedEpoch() },
        lastReceivedSeq = { lastReceivedSeq.get() },
        onHelloAck = { ack -> handleHelloAck(ack) },
        onCommand = { frame, payload, duplicateReAck ->
            handleCommandFrame(frame, payload, duplicateReAck)
        },
        onRejected = { r ->
            onEvent?.invoke("拒帧 rule=${r.rule} detail=${r.detail} close=${r.closeCode}")
        },
        transportFactory = transportFactory,
    )

    fun start() {
        session.start()
        onEvent?.invoke("执行回路启动 deviceId=${credentials.deviceId}")
    }

    fun stop() {
        session.stop()
        onEvent?.invoke("执行回路停止")
    }

    /** 当前已确认入向 seq（诊断/测试面）。 */
    fun confirmedSeq(): Long = lastReceivedSeq.get()

    /** 幂等回执源（诊断/测试面）：commandId → 最近一次 RAW_CONFIRM 的 eventId。 */
    fun lastResultEventId(commandId: String): String? = session.lastResultEventId(commandId)

    // ---- hello_ack：会话建立（T3.4 冻结形态；计数推进在 FrameVerifier 完成）----

    private fun handleHelloAck(ack: ControlFrame) {
        lastReceivedSeq.set(ack.meta.seq)
        onEvent?.invoke(
            "hello_ack 会话建立 resumeFromSeq=${ack.longField("resumeFromSeq")} serverTime=${ack.longField("serverTime")}",
        )
    }

    // ---- 载荷分发 ----

    private fun handleCommandFrame(
        frame: V2Frame,
        payload: kotlinx.serialization.json.JsonElement,
        duplicateReAck: Boolean,
    ) {
        lastReceivedSeq.set(frame.meta.seq)
        val beans = try {
            codec.beans(payload)
        } catch (e: Exception) {
            onEvent?.invoke("载荷解析失败: ${e.message}")
            return
        }
        for (bean in beans) {
            when {
                bean.type in MessageTypes.V2_CONTROL_RANGE -> handleControlPayload(frame, bean)
                duplicateReAck -> reAckOriginal(frame.meta.commandId)
                else -> admitCommand(frame, bean)
            }
        }
    }

    /**
     * 幂等回执（§5.4.3，不重复副作用）：重复 eventId 同内容 / journal commandId 去重命中
     * → 以**同 eventId 重建回执帧**（载荷逐值一致 = 同 payloadDigest；新 nonce/ts/seq +
     * 重算 HMAC，CR T34-01）。原样重发旧帧文本会被冻结入站规则拒收（4402/4403/4405+903）。
     */
    private fun reAckOriginal(commandId: String) {
        if (commandId.isEmpty()) return
        if (session.resendExecResult(commandId)) {
            onEvent?.invoke("重复下发 commandId=$commandId → 幂等回执（同 eventId 重建帧：新 nonce/ts/seq + 重算 HMAC）")
        } else {
            onEvent?.invoke("重复下发 commandId=$commandId 无原回执（静默；journal 重放兜底）")
        }
    }

    /** E09 门禁：持久化 + commandId 去重 + epoch 拒旧，先于副作用。 */
    private fun admitCommand(frame: V2Frame, bean: WeworkMessageBean) {
        val commandId = frame.meta.commandId
        if (commandId.isEmpty()) {
            onEvent?.invoke("业务载荷缺 meta.commandId → 拒收 type=${bean.type}")
            return
        }
        pendingBean = bean
        pendingEpoch = epochOf(bean)
        val decision = gate.apply(InboundCommandGate.InboundCommand(commandId, bean.type, pendingEpoch))
        when (decision) {
            InboundCommandGate.Decision.Applied -> Unit
            InboundCommandGate.Decision.AlreadyApplied -> {
                onEvent?.invoke("commandId=$commandId 已执行（journal 去重）→ 幂等回执")
                reAckOriginal(commandId)
            }
            is InboundCommandGate.Decision.RejectedOld ->
                onEvent?.invoke("commandId=$commandId epoch=${decision.epoch} < 水位 ${decision.appliedEpoch} → 拒旧")
        }
        pendingBean = null
    }

    /** dispatch 载荷 epoch 字段（spec §5.7.2 v1.2.0 冻结；缺失按 0 = 不拒旧仅去重）。 */
    private fun epochOf(bean: WeworkMessageBean): Long =
        (bean.extras["epoch"] as? JsonPrimitive)?.takeIf { !it.isString }?.longOrNull ?: 0L

    // ---- 真实执行 + RAW_CONFIRM（本类唯一副作用入口，经 gate 调用）----

    private fun executeCommand(commandId: String, type: Int) {
        val bean = pendingBean ?: run {
            reportResult(commandId, type, ExecutorErrorCodes.PAYLOAD_INVALID, "执行上下文缺失", null, 0L, success = false)
            return
        }
        val epoch = pendingEpoch
        val startedAt = clock()
        val normalized = normalizer.normalize(bean)
        if (normalized !is NormalizedIntent.Valid) {
            val (code, reason) = when (normalized) {
                is NormalizedIntent.Unsupported ->
                    ExecutorErrorCodes.COMMAND_UNSUPPORTED to "指令 $type 未注册执行器"
                is NormalizedIntent.Invalid ->
                    ExecutorErrorCodes.PAYLOAD_INVALID to "载荷不合法：${normalized.field} ${normalized.reason}"
                else -> ExecutorErrorCodes.STEP_ERROR to "不可达"
            }
            ledger(commandId, epoch, started = false, outcome = "never_started")
            reportResult(commandId, type, code, reason, null, clock() - startedAt, success = false)
            return
        }
        // FR-54 端侧干跑门（主界面快捷操作开关）：同引擎同路径仅构建计划，零副作用。
        // 回执结构同真实成功路径（docs/04 §5.1「干跑报告与真实路径同构」），另携
        // metaJson.deviceDryRun=true 审计标记；台账 outcome=device_dry_run。
        if (dryRunGate?.invoke() == true) {
            val dry = runner.dryRun(normalized.intent, registry)
            ledger(commandId, epoch, started = false, outcome = "device_dry_run")
            reportResult(
                commandId, type, 0, null, dry.target,
                clock() - startedAt, success = true, deviceDryRun = true,
            )
            onEvent?.invoke(
                "端侧干跑 commandId=$commandId type=$type target=${dry.target} 步骤=${dry.plannedSteps}（零副作用，FR-54）",
            )
            return
        }
        val report = runner.execute(normalized.intent, registry)
        val target = normalized.intent.targetDescription
        when (report.status) {
            ExecutionStatus.PAUSED -> {
                // FR-52：接管暂停——只入队语义归上层调度；不发终态回执（服务端经对账收敛）
                ledger(commandId, epoch, started = false, outcome = "")
                onEvent?.invoke("commandId=$commandId 暂停拦截（${report.pausedBySource}）→ 不发终态回执")
            }
            ExecutionStatus.SUCCEEDED -> {
                ledger(commandId, epoch, started = true, outcome = "success")
                reportResult(commandId, type, 0, null, target, report.finishedAtMillis - report.startedAtMillis, success = true)
            }
            ExecutionStatus.FAILED -> {
                ledger(commandId, epoch, started = true, outcome = "failure")
                reportResult(
                    commandId, type, report.errorCode, report.errorReason, target,
                    report.finishedAtMillis - report.startedAtMillis, success = false,
                )
            }
        }
    }

    private fun ledger(commandId: String, epoch: Long, started: Boolean, outcome: String) {
        synchronized(replayLedger) {
            replayLedger[commandId] = ReplayEntry(epoch, started, outcome)
            if (replayLedger.size > 1024) { // 有界（跨进程持久化 = dodDeferred，见类注）
                val it = replayLedger.entries.iterator()
                it.next()
                it.remove()
            }
        }
    }

    // ---- 控制载荷 901~904（spec §5.7 冻结）----

    private fun handleControlPayload(frame: V2Frame, bean: WeworkMessageBean) {
        val tree = codec.listElement(listOf(bean))[0] as JsonObject
        val violations = ControlPayloadValidator.validate(tree)
        if (violations.isNotEmpty()) {
            onEvent?.invoke("控制载荷 ${bean.type} 校验失败: $violations（拒收）")
            return
        }
        when (bean.type) {
            MessageTypes.CREDENTIAL_ROTATE -> {
                if (frame.encryptType != 2) {
                    onEvent?.invoke("901 未走加密会话（encryptType=${frame.encryptType}）→ 拒收（spec §5.5.1）")
                    return
                }
                val token = bean.extraString("deviceToken")
                val version = (bean.extras["tokenVersion"] as? JsonPrimitive)?.takeIf { !it.isString }?.content?.toIntOrNull()
                val serverPub = bean.extraString("serverX25519PublicKey")
                if (token != null && version != null && serverPub != null) {
                    onCredentialRotated?.invoke(token, version, serverPub)
                    onEvent?.invoke("901 凭据轮换 → tokenVersion=$version（回调换发）")
                } else {
                    onEvent?.invoke("901 字段缺失（校验通过但取值失败）→ 拒收")
                }
            }
            MessageTypes.CREDENTIAL_REVOKED -> {
                onEvent?.invoke("902 凭据撤销（meta.tokenVersion=${frame.meta.tokenVersion}）→ 停止会话走强制重配对")
                onCredentialRevoked?.invoke()
                stop()
            }
            MessageTypes.RESYNC_REQUEST ->
                // 服务端随 4405 关流；重连机制自动以持久确认点补发（spec §5.4.4）
                onEvent?.invoke("903 重同步请求（服务端将关流重放）")
            MessageTypes.JOURNAL_REPLAY ->
                if (bean.extraString("request") == "journal_replay") {
                    sendJournalReplay()
                } else {
                    onEvent?.invoke("收到端→云 904 补报载荷（设备侧不消费）")
                }
        }
    }

    /** 904 补报：执行台账 → journalReplay 载荷（socketType=2，AtLeastOnce 经队列）。 */
    fun sendJournalReplay() {
        val entries = synchronized(replayLedger) {
            replayLedger.map { (commandId, e) ->
                buildJsonObject {
                    put("commandId", commandId)
                    put("epoch", e.epoch)
                    put("started", e.started)
                    put("outcome", e.outcome)
                    put("source", "local")
                }
            }
        }
        if (entries.isEmpty()) {
            onEvent?.invoke("904 补报请求：台账为空（无已执行记录）")
            return
        }
        val bean = WeworkMessageBean(
            type = MessageTypes.JOURNAL_REPLAY,
            extras = mapOf("entries" to buildJsonArray { entries.forEach { add(it) } }),
        )
        session.sendReport(bean)
        onEvent?.invoke("904 补报已入队：${entries.size} 条")
    }

    // ---- RAW_CONFIRM 结果回报（socketType=3，ExecCallbackBean）----

    private fun reportResult(
        commandId: String,
        commandType: Int,
        errorCode: Int,
        errorReason: String?,
        target: String?,
        timeCostMillis: Long,
        success: Boolean,
        deviceDryRun: Boolean = false,
    ) {
        val bean = WeworkMessageBean(
            type = commandType,
            errorCode = errorCode,
            errorReason = errorReason ?: "",
            runTime = RUN_TIME_FORMAT.format(Instant.ofEpochMilli(clock()).atZone(ZoneId.systemDefault())),
            timeCost = timeCostMillis,
            successList = if (success && target != null) listOf(target) else emptyList(),
            failList = if (!success && target != null) listOf(target) else emptyList(),
            metaJson = buildJsonObject {
                put("dispatchId", commandId)
                if (deviceDryRun) put("deviceDryRun", true) // FR-54 端侧干跑审计标记
            }.toString(),
        )
        session.sendExecResult(commandId, bean)
        onExecResult?.invoke(commandId, commandType, errorCode, success)
        onEvent?.invoke("RAW_CONFIRM commandId=$commandId type=$commandType errorCode=$errorCode")
    }

    private companion object {
        val RUN_TIME_FORMAT: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss")
    }
}
