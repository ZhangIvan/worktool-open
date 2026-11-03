/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.delivery

/** 出向发送出口（实现：WS 传输；测试：fake 通道）。 */
interface OutboundSink {
    /** 发送一帧；返回 false = 当前不可发送（未连接/已关闭），条目保持待发。 */
    fun send(frameText: String): Boolean
}

/**
 * AtLeastOnce 发送队列（任务卡 T2.2 ④：先落盘后副作用语义）。
 *
 * enqueue 顺序固定：
 * 1. journal.append(PENDING)——**副作用发生前必须已持久**；
 * 2. 尝试发送（失败/发送前崩溃都不丢：条目仍在 journal）。
 *
 * 对端去重 = eventId（重复 eventId 同 payloadDigest 幂等回执原确认，spec §5.4.3）。
 * 帧文本（含 eventId/载荷）在 append 时固化；**重放**（flushPending）时若注入
 * [refreshForResend] 则以「同 eventId + 新 nonce/ts/seq + 重算 HMAC」重建信封——
 * 原样重发固化文本会被冻结入站规则拒收（ts±30s → 4403；nonce 5min → 4402；
 * seq 严格递增 → 4405+903，CR T34-01 复核项）。首次投递（enqueue 直发）不重建
 * （帧刚构造即新鲜，重建只会白耗帧序）。
 */
class AtLeastOnceQueue(
    private val store: JournalStore,
    private val sink: OutboundSink,
    /** 重放帧刷新钩子（eventId + 固化帧文本 → 重建帧文本）；null = 原样重放。 */
    private val refreshForResend: ((eventId: String, frameText: String) -> String)? = null,
) {

    /** 当前未完成（非 DONE）条目数。 */
    fun pendingCount(): Int = store.entries().count { it.state != JournalState.DONE }

    /**
     * 入队（先落盘后发送）。返回落盘后的 journal 条目。
     * 无论发送成败，条目均已持久——发送失败后由 [flushPending]（重连/恢复时）重放。
     */
    fun enqueue(eventId: String, frameText: String, now: Long): JournalEntry {
        store.append(
            JournalEntry(
                eventId = eventId,
                frameText = frameText,
                createdAt = now,
                state = JournalState.PENDING,
                attempts = 0,
            ),
        ) // ① 先落盘（副作用前置条件）
        tryDeliver(eventId, refresh = false) // ② 后副作用（首投不重建：帧即新鲜）
        return store.entries().first { it.eventId == eventId }
    }

    /** 尝试投递单条（attempts 递增；sink 异常/不可发送时条目留待 flushPending 重放）。 */
    private fun tryDeliver(eventId: String, refresh: Boolean) {
        val entry = store.entries().firstOrNull { it.eventId == eventId } ?: return
        store.update(eventId, JournalState.INFLIGHT, entry.attempts + 1)
        val frameText = if (refresh) refreshForResend?.invoke(eventId, entry.frameText) ?: entry.frameText else entry.frameText
        try {
            sink.send(frameText)
        } catch (_: Exception) {
            // 传输异常不丢条目：journal 已持久，重连后 flushPending 重放
        }
    }

    /** 重放所有未完成条目（连接建立/崩溃恢复时调用；注入 [refreshForResend] 时按同 eventId 重建信封）。 */
    fun flushPending() {
        store.entries().filter { it.state != JournalState.DONE }.forEach { tryDeliver(it.eventId, refresh = true) }
    }

    /** 服务端确认（收到对应 eventId 的 ack/回执）：条目完成。 */
    fun onAck(eventId: String) {
        store.entries().firstOrNull { it.eventId == eventId }?.let {
            store.update(eventId, JournalState.DONE, it.attempts)
        }
    }
}

/** 入向命令门禁（E09 guard：执行门禁与去重持久化先于副作用）。 */
class InboundCommandGate(
    private val store: JournalStore,
    private val effect: (InboundCommand) -> Unit,
) {

    /** 入向指令描述（来自已验帧 socketType=2 载荷）。 */
    data class InboundCommand(
        val commandId: String,
        val type: Int,
        val epoch: Long,
    )

    sealed interface Decision {
        object Applied : Decision
        /** 已执行过（journal 去重命中）：不得重复副作用，回执原确认。 */
        object AlreadyApplied : Decision
        /** epoch < 已应用水位：拒旧（docs/02 §5.1 端侧拒旧）。 */
        data class RejectedOld(val epoch: Long, val appliedEpoch: Long) : Decision
    }

    /**
     * 门禁 + 副作用。顺序：
     * 1. epoch 拒旧（未持久，无副作用风险）；
     * 2. journal 记录启动（持久，先于副作用；重复 commandId → AlreadyApplied）；
     * 3. 执行副作用；
     * 4. 推进 appliedEpoch。
     */
    fun apply(command: InboundCommand): Decision {
        val watermark = store.appliedEpoch()
        if (command.epoch < watermark) {
            return Decision.RejectedOld(command.epoch, watermark)
        }
        if (!store.recordInboundStart(command.commandId, command.type, command.epoch)) {
            return Decision.AlreadyApplied
        }
        effect(command) // 副作用发生在持久化之后（崩溃恢复可由 journal 重放对账）
        if (command.epoch > watermark) {
            store.setAppliedEpoch(command.epoch)
        }
        return Decision.Applied
    }
}
