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
 *   [isPaused]（[ActionExecutor.execute] 契约，Ports.kt）。
 * - 全部状态迁移经 [audit] 回调留痕（SR-12：接管事件全量审计）。
 */
class PauseManager(
    private val clock: () -> Long,
    private val audit: (AuditEvent) -> Unit = {},
) {

    enum class State { RUNNING, PAUSED, RESUME_PENDING }

    /** 暂停期间只入队不执行的服务端指令。 */
    data class DeferredCommand(
        val commandId: String,
        val commandType: Int,
        val targetRoom: String? = null,
        val content: String? = null,
        val receivedAtMillis: Long,
    )

    /** 审计事件（SR-12；payload 仅元数据，不含消息正文）。 */
    data class AuditEvent(
        val type: String, // paused / resume_requested / resume_confirmed / resume_cancelled / command_deferred
        val at: Long,
        val actor: String? = null,
        val reason: String? = null,
        val commandCount: Int = 0,
    )

    /** 显式确认缺失（从 PAUSED 直接确认恢复等非法迁移）。 */
    class PauseConfirmationRequired(message: String) : IllegalStateException(message)

    private val lock = Any()
    private var stateValue: State = State.RUNNING
    private var pauseSourceValue: String? = null
    private val deferredCommands = ArrayDeque<DeferredCommand>()
    private val auditLog = ArrayList<AuditEvent>()

    val state: State get() = synchronized(lock) { stateValue }
    val isPaused: Boolean get() = synchronized(lock) { stateValue != State.RUNNING }

    /**
     * 暂停来源标识（最近一次 pause 的 actor；未暂停/无 actor 返回 null）。
     * T3.1 执行器侧只读暂停门（feature:executor runtime.PauseManager）经 app 适配器
     * 消费本方法（FR-52 三处一致性核对 + 执行报告留痕）。
     */
    fun pauseSource(): String? = synchronized(lock) { if (stateValue == State.RUNNING) null else pauseSourceValue }

    /** 一键暂停（RUNNING/RESUME_PENDING → PAUSED；重复暂停幂等）。 */
    fun pause(reason: String? = null, actor: String? = null, now: Long = clock()) {
        synchronized(lock) {
            stateValue = State.PAUSED
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
            check(stateValue != State.RUNNING) { "RUNNING 态无入队语义：在线指令应直接执行" }
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
            check(stateValue == State.PAUSED) { "仅 PAUSED 态可发起恢复（当前 $stateValue）" }
            stateValue = State.RESUME_PENDING
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
            if (stateValue == State.PAUSED) {
                throw PauseConfirmationRequired("恢复须先 requestResume 显式确认（FR-52），当前 PAUSED")
            }
            check(stateValue == State.RESUME_PENDING) { "仅 RESUME_PENDING 态可确认恢复（当前 $stateValue）" }
            stateValue = State.RUNNING
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
            check(stateValue == State.RESUME_PENDING) { "仅 RESUME_PENDING 态可撤回（当前 $stateValue）" }
            stateValue = State.PAUSED
            val e = AuditEvent("resume_cancelled", now)
            auditLog.add(e)
            audit(e)
        }
    }

    /** 暂存指令快照（悬浮窗/控制台展示，FR-50/52 暂停态一致性）。 */
    fun deferred(): List<DeferredCommand> = synchronized(lock) { deferredCommands.toList() }

    /** 审计留痕快照（SR-12）。 */
    fun auditTrail(): List<AuditEvent> = synchronized(lock) { auditLog.toList() }
}
