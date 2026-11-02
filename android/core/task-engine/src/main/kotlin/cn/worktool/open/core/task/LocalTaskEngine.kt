/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.transport.Cancellable
import cn.worktool.open.core.protocol.transport.Scheduler
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import java.time.ZoneId

/**
 * 本地任务引擎门面（T3.3；docs/02 §2 模式A + §5.2 fallback 仲裁）。
 *
 * 统一分派路径（FR-54：干跑与真实执行同引擎同路径，仅 policy 开关）：
 * ```
 * 事件（消息/好友/服务端指令/cron）→ 仲裁门禁（暂停态：服务端只入队、本地规则阻断）
 *   → dryRun ? DryRunRunner 报告（零副作用、零台账，CR-t33-R-03）
 *            : (ruleId,eventHash) 去重记账（先记账后副作用）→ ActionExecutor 执行（T3.1 注入）
 *   → 重连后 pendingReports 以 source=local 上报（服务端同键去重，spec §5.4.5）
 * ```
 *
 * 线程模型：公开方法在内部锁内完成状态读写；[ActionExecutor.execute] 在锁外调用
 * （执行器可能耗时，且需能在安全断点轮询 [PauseManager.isPaused]）。
 */
class LocalTaskEngine(
    val config: LocalModeConfig,
    val rules: RuleSetConfig,
    /** 纯本地模式（模式 A：未配置服务端）→ 本地 cron 恒可用（docs/02 §5.2）。 */
    private val pureLocalMode: Boolean = false,
    private val reporter: LocalEventReporter? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    zone: ZoneId = ZoneId.systemDefault(),
    pauseAuditor: (PauseManager.AuditEvent) -> Unit = {},
) {

    private val lock = Any()
    private val executors = HashMap<Int, ActionExecutor>()
    private val pauseManager = PauseManager(clock, pauseAuditor)
    private val fallbackArbiter = FallbackArbiter(config, clock)
    private val keywordEngine = KeywordRuleEngine(clock)
    private val friendHook = FriendAutoAcceptHook()
    private val cronScheduler = LocalCronScheduler(rules.cronJobs, zone, clock)
    private val ledger = LocalEventLedger(InMemoryLocalEventStore())
    private val dryRunRunner = DryRunRunner(clock)

    /** 单次分派结果。 */
    sealed interface Outcome {
        data class Executed(
            val ruleId: String?,
            val commandType: Int,
            val errorCode: Int,
            val errorReason: String?,
        ) : Outcome

        data class DryRun(val report: DryRunRunner.DryRunReport) : Outcome

        data class Deferred(val commandId: String, val queueSize: Int) : Outcome

        /** 暂停态阻断（本地规则，FR-52：暂停生效后无新副作用）。 */
        data class BlockedPaused(val ruleId: String?, val source: Source) : Outcome

        /** (ruleId,eventHash) 重复观测：不执行、不上报。 */
        data class DuplicateSkipped(val ruleId: String, val eventHash: String) : Outcome

        /** 未注册对应执行器（T3.1 未合并/未注入时可见）。 */
        data class NoExecutor(val commandType: Int, val ruleId: String?) : Outcome
    }

    // ---- 注入与只读视图 ----

    /** 注册 T3.1 指令执行器（FR-15 PASS_FRIEND = supportedType 212；发消息 = 203）。 */
    fun registerExecutor(executor: ActionExecutor) {
        synchronized(lock) { executors[executor.supportedType] = executor }
    }

    fun pauseManager(): PauseManager = pauseManager

    fun fallbackArbiter(): FallbackArbiter = fallbackArbiter

    // ---- 事件入口（模式 A 核心） ----

    /**
     * 入站消息事件（FR-01 监听回调 → FR-39 关键词规则）。
     * 返回各命中规则的分派结果（顺序 = 规则优先级序）。
     */
    fun onMessageEvent(event: MessageEvent, dryRun: Boolean = config.dryRun): List<Outcome> {
        val matched = synchronized(lock) { keywordEngine.evaluate(event, rules.keywordRules) }
        return matched.map { m ->
            val hash = EventHashes.compute(
                ruleId = m.rule.ruleId,
                commandType = m.intent.commandType,
                targetRoom = event.targetRoom,
                senderNames = event.senderNames,
                text = event.text,
                eventTimeMillis = event.eventTimeMillis,
            )
            dispatchLocal(m.intent, event.roomType, hash, dryRun)
        }
    }

    /** 新好友请求事件（FR-15：自动通过钩子 → PASS_FRIEND 意图 → T3.1 执行器）。 */
    fun onFriendRequest(event: FriendRequestEvent, dryRun: Boolean = config.dryRun): List<Outcome> {
        val matched = synchronized(lock) { friendHook.evaluate(event, rules.friendAcceptRules) }
        return matched.map { m ->
            val hash = EventHashes.compute(
                ruleId = m.rule.ruleId,
                commandType = m.intent.commandType,
                targetRoom = event.friendName,
                senderNames = listOf(event.friendName),
                text = event.leavingMsg ?: "",
                eventTimeMillis = event.eventTimeMillis,
            )
            dispatchLocal(m.intent, roomType = null, hash, dryRun)
        }
    }

    /**
     * 服务端在线指令（模式 B；docs/02 §5.2：暂停态下同样只入队不执行）。
     * 入向去重/epoch 拒旧由协议层 InboundCommandGate 承担（T2.2，复用不重造），此处只做门禁与执行。
     */
    fun onServerCommand(command: ServerCommand): Outcome {
        val intent = ActionIntent(
            commandType = command.commandType,
            targetRoom = command.targetRoom,
            content = command.content,
            source = Source.SERVER_COMMAND,
        )
        if (CommandArbiter.gate(pauseManager.isPaused, Source.SERVER_COMMAND) ==
            CommandArbiter.GateDecision.ENQUEUE_ONLY
        ) {
            pauseManager.enqueue(
                PauseManager.DeferredCommand(
                    commandId = command.commandId,
                    commandType = command.commandType,
                    targetRoom = command.targetRoom,
                    content = command.content,
                    receivedAtMillis = command.receivedAtMillis,
                ),
            )
            return Outcome.Deferred(command.commandId, pauseManager.deferred().size)
        }
        return executeIntent(intent, dryRun = false)
    }

    /**
     * cron 到期驱动（FR-38 模式A；由 [startCronLoop] 或持有者定时调用）。
     * 门禁：本地 cron 仅当 FallbackArbiter 放行（纯本地模式，或 fallback 开启且
     * server 不可达时长 ≥ 阈值——docs/02 §5.2 事实源分工：定时调度属服务端）。
     */
    fun cronTick(now: Long = clock(), dryRun: Boolean = config.dryRun): List<Outcome> {
        if (!fallbackArbiter.localSchedulingEnabled(pureLocalMode, now)) return emptyList()
        val due = synchronized(lock) { cronScheduler.due(now) }
        return due.map { d ->
            val intent = d.job.action.toIntent(d.job.jobId)
            val hash = EventHashes.compute(
                ruleId = d.job.jobId,
                commandType = intent.commandType,
                targetRoom = intent.targetRoom,
                senderNames = emptyList(),
                text = "cron:${d.job.expression}@${d.scheduledForMillis}",
                eventTimeMillis = d.scheduledForMillis,
            )
            dispatchLocal(intent, roomType = null, hash, dryRun)
        }
    }

    // ---- 重连合并（docs/02 §5.2 / spec §5.4.5） ----

    /**
     * 重连成功：未上报的已执行本地事件以 `source=local` 载荷上报
     * （服务端按 (ruleId, eventHash) 去重）；提交发送队列成功后标记已上报。
     * 纯本地模式或未接 reporter 时 no-op（无服务端可报）。
     */
    fun onReconnected(): List<LocalExecEvent> {
        val r = reporter ?: return emptyList()
        val pending = ledger.pendingReports()
        if (pending.isEmpty()) return emptyList()
        if (!r.report(pending.map { it.toReportBean() })) {
            // 发送队列不可用：保留未上报态，下次重连重试（服务端同键去重兜底重复上报）
            return emptyList()
        }
        pending.forEach { ledger.markReported(it) }
        return pending
    }

    /** source=local 上报载荷（spec §5.4.5：source="local"；ruleId/eventHash 入 extras 供服务端去重）。 */
    internal fun LocalExecEvent.toReportBean(): WeworkMessageBean {
        val extra = buildJsonObject {
            put(EXTRA_RULE_ID, JsonPrimitive(ruleId))
            put(EXTRA_EVENT_HASH, JsonPrimitive(eventHash))
            put("executedAt", JsonPrimitive(executedAtMillis))
            targetRoom?.let { put("targetRoom", JsonPrimitive(it)) } // 候选字段（spec §4.4）
            content?.let { put("receivedContent", JsonPrimitive(it)) } // 候选字段（spec §4.4）
        }
        return WeworkMessageBean(
            type = commandType,
            roomType = roomType,
            source = SOURCE_LOCAL,
            extras = extra,
        )
    }

    // ---- cron 驱动循环（薄封装：复用 T2.2 协议层 Scheduler 抽象，不重造定时器） ----

    private var cronHandle: Cancellable? = null

    /**
     * 门禁期短轮询间隔：[nextWakeMillis] 已到期但 fallback 仲裁未放行（模式 B 在线）
     * 时的重查节奏——水位不推进（防丢任务：放行后仍须补跑），也不能 0 延迟忙转。
     */
    private val gatedCronPollMillis: Long = 1_000L

    /** 启动 cron 驱动（每次醒来 tick 并按 nextWake 重排）；重复调用先停旧循环。 */
    fun startCronLoop(scheduler: Scheduler) {
        stopCronLoop()
        scheduleNext(scheduler)
    }

    /**
     * 排程下一次唤醒（CR-t33-R-02：驱动环不得自灭）。
     *
     * - 到期时刻含已到期未消费水位（[LocalCronScheduler.nextWakeMillis] 不再过滤 ≤now）：
     *   门禁挡住 tick 时水位不动，若按「未来时刻」排程将取到 null → 循环死亡；
     * - 门禁放行：到期（含过期）→ 0 延迟立即消费（tick 内 [LocalCronScheduler.due]
     *   会推进水位，不会忙转）；未到期 → 正常延时；
     * - 门禁未放行且有到期水位：短轮询重查（[gatedCronPollMillis]），失联超阈值
     *   放行后最迟一个轮询间隔内补跑（docs/02 §5.2 兜底语义）。
     */
    private fun scheduleNext(scheduler: Scheduler) {
        val now = clock()
        val wake = synchronized(lock) { cronScheduler.nextWakeMillis(now) } ?: return
        val enabled = fallbackArbiter.localSchedulingEnabled(pureLocalMode, now)
        val delay = when {
            wake > now -> wake - now
            enabled -> 0L
            else -> gatedCronPollMillis
        }
        cronHandle = scheduler.schedule(delay) {
            cronTick(clock())
            scheduleNext(scheduler)
        }
    }

    fun stopCronLoop() {
        cronHandle?.cancel()
        cronHandle = null
    }

    // ---- 统一分派内核 ----

    /**
     * 本地规则产物分派：门禁 → 干跑/执行 → 去重记账（**先记账后副作用**，对齐协议层
     * AtLeastOnce 语义：崩溃恢复重放同事件被去重，不会双发）。
     *
     * 干跑（FR-54）**不记账**（CR-t33-R-03）：干跑不是副作用——记账会让
     * 「干跑预检后真实执行」被去重键吞掉（DuplicateSkipped 抑制 FR-39 真实回复），
     * 且 onReconnected 会把从未发生的执行以 source=local 上报，污染服务端台账。
     */
    private fun dispatchLocal(
        intent: ActionIntent,
        roomType: Int?,
        eventHash: String,
        dryRun: Boolean,
    ): Outcome {
        val ruleId = requireNotNull(intent.ruleId) { "本地规则意图必须携带 ruleId" }
        if (CommandArbiter.gate(pauseManager.isPaused, intent.source) ==
            CommandArbiter.GateDecision.BLOCKED
        ) {
            return Outcome.BlockedPaused(ruleId, intent.source)
        }
        if (dryRun) {
            return executeIntent(intent, dryRun)
        }
        val event = LocalExecEvent(
            ruleId = ruleId,
            eventHash = eventHash,
            commandType = intent.commandType,
            roomType = roomType,
            targetRoom = intent.targetRoom,
            content = intent.content,
            executedAtMillis = clock(),
        )
        if (!ledger.recordExecuted(event)) return Outcome.DuplicateSkipped(ruleId, eventHash)
        return executeIntent(intent, dryRun)
    }

    /** 执行/干跑（执行器在锁外调用）。 */
    private fun executeIntent(intent: ActionIntent, dryRun: Boolean): Outcome {
        if (dryRun) return Outcome.DryRun(dryRunRunner.report(intent))
        val executor = synchronized(lock) { executors[intent.commandType] }
            ?: return Outcome.NoExecutor(intent.commandType, intent.ruleId)
        val outcome = executor.execute(intent) // 锁外：执行器可在安全断点轮询暂停态
        return Outcome.Executed(intent.ruleId, intent.commandType, outcome.errorCode, outcome.errorReason)
    }

    companion object {
        /** 上报载荷 source 字段值（spec §5.4.5 冻结值 "local"）。 */
        const val SOURCE_LOCAL: String = "local"

        /** 上报 extras 键：ruleId / eventHash（服务端 (ruleId,eventHash) 去重的端侧输入）。 */
        const val EXTRA_RULE_ID: String = "ruleId"
        const val EXTRA_EVENT_HASH: String = "eventHash"
    }
}
