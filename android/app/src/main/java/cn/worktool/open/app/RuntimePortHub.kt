/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.ports.CommandQueuePort
import cn.worktool.open.feature.executor.ports.ConsoleNoticePort
import cn.worktool.open.feature.executor.ports.ReceiveLoopPort
import cn.worktool.open.feature.executor.ports.SessionControlPort
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.ports.TargetOutcome
import kotlinx.serialization.json.JsonObject

/**
 * 会话回路自有端口集（W-M6 fullBatch 装配）：把「回路天然知道答案」的端口
 * （批量成败/结构化推送/接管暂停/接收循环开关/控制台提示/待执行队列）绑到
 * [DeviceSessionLoop] 与 task-engine [PauseManager] 上——不造平行状态。
 *
 * 装配序（解环）：hub 先构造（loop 引用可空）→ 注入 ExecutorBindings →
 * DeviceSessionLoop 启动即 [attach]；attach 前到达的调用按未连接语义
 * 诚实失败（false/null），不假装成功。
 *
 * 语义映射（登记）：
 * - 201 STOP_AND_GO_HOME → [PauseManager.pause]（接管暂停，FR-52 同一状态机）；
 * - 202 LOOP_RECEIVE_NEW_MESSAGE → 暂停/恢复以「receive_loop」为 actor——
 *   v1 语义=消息处理暂停（回路 WS 保持；完全断流归 SP-2 前台服务裁量）；
 * - 304/305/306 待执行队列 → PauseManager.deferred()（onDeferred 入队的同源数据，
 *   U3 队列视图共用）；messageId=commandId 口径。
 */
class RuntimePortHub(
    private val pauseManager: PauseManager,
    private val clock: () -> Long,
    /** UI 侧提示出口（Toast/悬浮窗；null=仅事件日志）。 */
    private val notifier: ((errorCode: Int, errorReason: String, stopMain: Boolean) -> Unit)? = null,
    private val onEvent: ((String) -> Unit)? = null,
) : SessionControlPort, ReceiveLoopPort, ConsoleNoticePort, CommandQueuePort,
    StructuredResultSink, BatchOutcomeSink {

    /** 回路引用（attach 前为 null → 结构化推送诚实失败）。 */
    @Volatile
    internal var loop: DeviceSessionLoop? = null

    private fun attachLoop(loop: DeviceSessionLoop) {
        this.loop = loop
    }

    internal fun attach(loop: DeviceSessionLoop) = attachLoop(loop)

    // ---- BatchOutcomeSink：当前指令的逐格成败（回路单线程执行 → 单槽即足） ----

    @Volatile
    private var pendingBatch: List<TargetOutcome>? = null

    override fun onBatchOutcome(commandType: Int, outcomes: List<TargetOutcome>) {
        pendingBatch = outcomes
        onEvent?.invoke("批量结果 type=$commandType → 成功 ${outcomes.count { it.succeeded }}/${outcomes.size}")
    }

    /** 回路 reportResult 时取走并清空（成功/失败名单组装；无则空）。 */
    internal fun takeBatchOutcomes(): List<TargetOutcome> {
        val out = pendingBatch ?: emptyList()
        pendingBatch = null
        return out
    }

    // ---- StructuredResultSink：socketType=2 结构化上行（511/512/513/515 等） ----

    override fun onStructuredResult(commandType: Int, payload: JsonObject) {
        val l = loop
        if (l == null) {
            onEvent?.invoke("结构化推送 type=$commandType 丢弃（回路未连接）")
            return
        }
        l.sendStructuredReport(commandType, payload)
        onEvent?.invoke("结构化推送 type=$commandType 已上行（socketType=2）")
    }

    // ---- SessionControlPort（201）：接管暂停 ----

    override fun requestTakeoverPause(source: String): Boolean {
        if (pauseManager.isPaused) return false // 已有更高优先级状态
        pauseManager.pause(reason = "stop_and_go_home", actor = source, now = clock())
        onEvent?.invoke("201 接管暂停已请求（source=$source）")
        return true
    }

    // ---- ReceiveLoopPort（202）：接收循环开关 = 暂停态切换 ----

    @Volatile
    private var receiveLoopEnabled: Boolean = true

    override fun setReceiveLoopEnabled(enabled: Boolean): Boolean {
        if (enabled) {
            if (!receiveLoopEnabled) {
                if (pauseManager.pauseSource() == RECEIVE_LOOP_ACTOR) {
                    pauseManager.requestResume(actor = RECEIVE_LOOP_ACTOR, now = clock())
                    receiveLoopEnabled = true
                    onEvent?.invoke("202 新消息接收循环 → 开（恢复处理）")
                } else {
                    return false // 暂停非本开关所致，避免越权恢复
                }
            }
        } else {
            if (!pauseManager.isPaused) {
                pauseManager.pause(reason = "receive_loop_off", actor = RECEIVE_LOOP_ACTOR, now = clock())
            }
            receiveLoopEnabled = false
            onEvent?.invoke("202 新消息接收循环 → 关（暂停处理）")
        }
        return true
    }

    override fun isReceiveLoopEnabled(): Boolean = receiveLoopEnabled

    // ---- ConsoleNoticePort（102）：控制台提示 ----

    override fun showErrorNotice(errorCode: Int, errorReason: String) {
        onEvent?.invoke("102 控制台提示：[$errorCode] $errorReason")
        notifier?.invoke(errorCode, errorReason, false)
    }

    override fun requestStopMainFunction(errorCode: Int, errorReason: String) {
        onEvent?.invoke("102 二次熔断：[$errorCode] $errorReason → 停止主功能")
        notifier?.invoke(errorCode, errorReason, true)
    }

    // ---- CommandQueuePort（304/305/306）：PauseManager deferred 同源 ----

    override fun clearPending(): List<String> {
        val ids = pauseManager.deferred().map { it.commandId }.distinct()
        pauseManager.discardDeferred(reason = "304 clear", actor = "command_queue", now = clock())
        onEvent?.invoke("304 清空待执行队列：${ids.size} 条")
        return ids
    }

    override fun removePending(messageIds: List<String>): Pair<List<String>, List<String>> {
        val current = pauseManager.deferred()
        val keep = current.filter { it.commandId !in messageIds }
        val removedIds = current.map { it.commandId }.filter { it in messageIds }
        if (keep.size != current.size) {
            pauseManager.discardDeferred(reason = "305 remove", actor = "command_queue", now = clock())
            keep.forEach { pauseManager.enqueue(it, now = clock()) }
        }
        return removedIds to messageIds.filter { it !in removedIds }
    }

    override fun pendingSnapshot(withDetails: Boolean): Pair<List<String>, List<String>> {
        val deferred = pauseManager.deferred()
        return deferred.map { it.commandId } to
            if (withDetails) {
                deferred.map { "type=${it.commandType} target=${it.targetRoom ?: '-'}" }
            } else {
                emptyList()
            }
    }

    private companion object {
        const val RECEIVE_LOOP_ACTOR = "receive_loop"
    }
}
