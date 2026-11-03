/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 暂停管理器（FR-52：一键暂停机器人；接管期间在线指令只入队不执行；恢复须显式确认）。
 *
 * 状态机：
 * ```
 * RUNNING --pause--> PAUSED --requestResume--> RESUME_PENDING --confirmResume--> RUNNING
 *    ^                   ^                          |                                   |
 *    |                   +-------cancelResume--------+          pause（再暂停，任意态可入）
 *    +-------------------（confirmResume 仅从 RESUME_PENDING 出发；PAUSED 直连确认 = 拒绝）+
 * ```
 * - 「恢复须显式确认」= 两步：[requestResume]（发起）→ [confirmResume]（确认）。
 *   没有 one-shot resume；从 PAUSED 直接 confirm 被拒绝（[PauseConfirmationRequired]）。
 * - 暂停期间 [enqueue] 收集服务端在线指令（FIFO）；[confirmResume] 返回全部暂存指令
 *   交调用方按序执行（引擎侧再过一次门禁与执行器）。
 * - 「进行中动作序列执行至安全断点停止」由 T3.1 执行器实现：在原子操作间隙轮询
 *   [isPaused]（[ActionExecutor.execute] 契约，Ports.kt）；被切断的执行按 T4.1
 *   **三分法**上报 [PausePhase] 并经 [recordInterruptedExecution] 留步骤证据与可恢复态。
 * - 全部状态迁移经 [audit] 回调留痕（SR-12：接管事件全量审计）。
 */
class PauseManager(
    private val clock: () -> Long,
    private val audit: (AuditEvent) -> Unit = {},
) {

    enum class State { RUNNING, PAUSED, RESUME_PENDING }

    /**
     * 暂停三分法（T4.1，docs/11 B2）：被暂停动作序列的**进度语义**，替代统一
     * 「started=false / 零副作用」的粗粒度口径——暂停既已发生，「没做」与「做了一半」
     * 与「做了但不知结果」对上层（服务端终态/重试决策/用户展示）是完全不同的世界。
     */
    enum class PausePhase {
        /** 执行前拦截：零副作用，可直接重投（原 FR-52「只入队不执行」语义保留于此相）。 */
        NOT_STARTED,

        /** 步间断点：≥1 步已安全完成且无在途动作；有步骤证据，[InterruptedExecution.resumeState] 非空时可续跑。 */
        PARTIALLY_EXECUTED,

        /** 最终动作已提交但效果未验证（如 click 已派发、上屏校验未过）：不得盲目重放，先探明效果。 */
        EFFECT_UNKNOWN,
    }

    /** 暂停期间只入队不执行的服务端指令。 */
    data class DeferredCommand(
        val commandId: String,
        val commandType: Int,
        val targetRoom: String? = null,
        val content: String? = null,
        val receivedAtMillis: Long,
    )

    /** 已完成步骤证据（T4.1「步骤证据」；payload 仅元数据，不含消息正文）。 */
    data class StepEvidence(
        val stepId: String,
        val description: String? = null,
        val succeeded: Boolean,
        val errorCode: Int? = null,
        val errorReason: String? = null,
    )

    /**
     * 被暂停切断的执行记录（T4.1「三分法 + 步骤证据 + 可恢复态 + 原回执保留」）。
     *
     * - 证据：[executedSteps]（已完成步骤清单，恢复与审计共用）；
     * - 可恢复态：[resumeState] 为执行器产出的**不透明续跑令牌**（如剩余步骤计划摘要）；
     *   null=不可恢复（须整单重投）；
     * - 重复投递：同 [commandId] 重复上报 = upsert 覆盖（不重复计证据）；
     * - EFFECT_UNKNOWN 默认不可恢复（须先效果探测，禁盲重放）。
     */
    data class InterruptedExecution(
        val commandId: String?,
        val commandType: Int,
        val phase: PausePhase,
        val executedSteps: List<StepEvidence> = emptyList(),
        val remainingStepIds: List<String> = emptyList(),
        val resumeState: String? = null,
        val pausedAtMillis: Long,
        val pauseSource: String? = null,
    ) {
        val isResumable: Boolean
            get() = phase == PausePhase.PARTIALLY_EXECUTED && resumeState != null
    }

    /** 审计事件（SR-12；payload 仅元数据，不含消息正文）。 */
    data class AuditEvent(
        val type: String, // paused / resume_requested / resume_confirmed / resume_cancelled / command_deferred / execution_interrupted
        val at: Long,
        val actor: String? = null,
        val reason: String? = null,
        val commandCount: Int = 0,
    )

    /** 显式确认缺失（从 PAUSED 直接确认恢复等非法迁移）。 */
    class PauseConfirmationRequired(message: String) : IllegalStateException(message)

    private val lock = Any()
    private var pauseStateValue: State = State.RUNNING
    private var pauseSourceValue: String? = null
    private val deferredCommands = ArrayDeque<DeferredCommand>()
    private val interruptedExecutions = LinkedHashMap<String, InterruptedExecution>()
    private val auditLog = ArrayList<AuditEvent>()

    val state: State get() = synchronized(lock) { pauseStateValue }
    val isPaused: Boolean get() = synchronized(lock) { pauseStateValue != State.RUNNING }

    /**
     * 暂停来源标识（最近一次 pause 的 actor；未暂停/无 actor 返回 null）。
     * T3.1 执行器侧只读暂停门（feature:executor runtime.PauseManager）经 app 适配器
     * 消费本方法（FR-52 三处一致性核对 + 执行报告留痕）。
     */
    fun pauseSource(): String? = synchronized(lock) { if (pauseStateValue == State.RUNNING) null else pauseSourceValue }

    /** 一键暂停（RUNNING/RESUME_PENDING → PAUSED；重复暂停幂等）。 */
    fun pause(reason: String? = null, actor: String? = null, now: Long = clock()) {
        synchronized(lock) {
            pauseStateValue = State.PAUSED
            pauseSourceValue = actor ?: pauseSourceValue
            val e = AuditEvent("paused", now, actor, reason)
            auditLog.add(e)
            audit(e)
        }
    }

    /**
     * 暂停态入队服务端指令（只入队不执行，FR-52）。
     * RUNNING 态调用 = 调用方误用（门禁应在引擎层先判 [isPaused]）。
     */
    fun enqueue(command: DeferredCommand, now: Long = clock()): DeferredCommand {
        synchronized(lock) {
            check(pauseStateValue != State.RUNNING) { "RUNNING 态无入队语义：在线指令应直接执行" }
            deferredCommands.addLast(command)
            val e = AuditEvent("command_deferred", now, commandCount = deferredCommands.size)
            auditLog.add(e)
            audit(e)
            return command
        }
    }

    /** 发起恢复（PAUSED → RESUME_PENDING；第一步，等待显式确认）。 */
    fun requestResume(actor: String? = null, now: Long = clock()) {
        synchronized(lock) {
            check(pauseStateValue == State.PAUSED) { "仅 PAUSED 态可发起恢复（当前 $pauseStateValue）" }
            pauseStateValue = State.RESUME_PENDING
            val e = AuditEvent("resume_requested", now, actor)
            auditLog.add(e)
            audit(e)
        }
    }

    /**
     * 确认恢复（RESUME_PENDING → RUNNING）：返回全部暂存指令（FIFO）交调用方执行。
     * 从 PAUSED 直接确认 = 缺显式确认步骤，抛 [PauseConfirmationRequired]。
     */
    fun confirmResume(actor: String? = null, now: Long = clock()): List<DeferredCommand> {
        synchronized(lock) {
            if (pauseStateValue == State.PAUSED) {
                throw PauseConfirmationRequired("恢复须先 requestResume 显式确认（FR-52），当前 PAUSED")
            }
            check(pauseStateValue == State.RESUME_PENDING) { "仅 RESUME_PENDING 态可确认恢复（当前 $pauseStateValue）" }
            pauseStateValue = State.RUNNING
            val flushed = deferredCommands.toList()
            deferredCommands.clear()
            val e = AuditEvent("resume_confirmed", now, actor, commandCount = flushed.size)
            auditLog.add(e)
            audit(e)
            return flushed
        }
    }

    /** 撤回恢复请求（RESUME_PENDING → PAUSED）。 */
    fun cancelResumeRequest(now: Long = clock()) {
        synchronized(lock) {
            check(pauseStateValue == State.RESUME_PENDING) { "仅 RESUME_PENDING 态可撤回（当前 $pauseStateValue）" }
            pauseStateValue = State.PAUSED
            val e = AuditEvent("resume_cancelled", now)
            auditLog.add(e)
            audit(e)
        }
    }

    /** 暂存指令快照（悬浮窗/控制台展示，FR-50/52 暂停态一致性）。 */
    fun deferred(): List<DeferredCommand> = synchronized(lock) { deferredCommands.toList() }

    /** 审计留痕快照（SR-12）。 */
    fun auditTrail(): List<AuditEvent> = synchronized(lock) { auditLog.toList() }

    // ---- T4.1 三分法：被切断执行的证据与可恢复态 ----

    /**
     * 记录一条被暂停切断的执行（执行器在安全断点上报；任意暂停态可调用）。
     *
     * 重复投递防护：同 [InterruptedExecution.commandId]（非 null）重复上报 = **upsert 覆盖**
     * （证据以最后一次上报为准，不重复累计）；commandId=null（如本地规则意图）= 追加。
     */
    fun recordInterruptedExecution(execution: InterruptedExecution, now: Long = clock()) {
        synchronized(lock) {
            if (execution.commandId != null) {
                interruptedExecutions[execution.commandId] = execution
            } else {
                interruptedExecutions["#${interruptedExecutions.size + 1}@${execution.pausedAtMillis}"] = execution
            }
            val e = AuditEvent(
                type = "execution_interrupted",
                at = now,
                reason = "phase=${execution.phase} steps=${execution.executedSteps.size} " +
                    "resumable=${execution.isResumable}",
                commandCount = interruptedExecutions.size,
            )
            auditLog.add(e)
            audit(e)
        }
    }

    /** 全部被切断执行快照（证据只增不清；审计/悬浮窗展示用）。 */
    fun interruptedExecutions(): List<InterruptedExecution> =
        synchronized(lock) { interruptedExecutions.values.toList() }

    /** 可恢复执行（[InterruptedExecution.isResumable]；恢复动作的输入清单）。 */
    fun resumableExecutions(): List<InterruptedExecution> =
        interruptedExecutions().filter { it.isResumable }

    /**
     * 取走可恢复执行（exactly-once 续跑分发）：[confirmResume] 后由持有者逐条取走重投，
     * 取走即出队——重复调用不重复分发（T4.1「覆盖恢复/重复投递」）。
     */
    fun drainResumable(): List<InterruptedExecution> = synchronized(lock) {
        val taken = interruptedExecutions.values.filter { it.isResumable }
        interruptedExecutions.entries.removeAll { it.value.isResumable }
        taken
    }

    /** 清空被切断执行记录（测试/审计归档用）。 */
    fun clearInterruptedExecutions() = synchronized(lock) { interruptedExecutions.clear() }

    companion object {
        /**
         * 三分法分类器（执行器断点处调用，统一口径避免各执行器自行脑补）：
         * - 未开始任何步骤 → [PausePhase.NOT_STARTED]；
         * - 最后动作已提交（派发 click/input 等）但断点前效果未验证 → [PausePhase.EFFECT_UNKNOWN]；
         * - 其余（有已完成步骤、无在途动作）→ [PausePhase.PARTIALLY_EXECUTED]。
         */
        fun classifyPhase(started: Boolean, lastActionCommitted: Boolean, effectVerified: Boolean): PausePhase =
            when {
                !started -> PausePhase.NOT_STARTED
                lastActionCommitted && !effectVerified -> PausePhase.EFFECT_UNKNOWN
                else -> PausePhase.PARTIALLY_EXECUTED
            }
    }
}
