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
 * 完成语义（docs/16 性能审计 H1）：**发送成功即 DONE**，flushPending 只重放未 DONE
 * 条目，DONE 条目在下次 enqueue/flush 后清理（保留 [doneRetention] 条防回退窗口）——
 * journal 永不完成则重放全量历史帧（最旧优先），新帧永远排队尾，跨重连滞留 5.5 分钟。
 * 重放幂等由服务端 eventId 去重兜底（spec §5.4.3），发送成功即 DONE 不损 at-least-once。
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
    /**
     * DONE 条目保留窗口（docs/16 性能审计 H1）：清理已完成条目时保留最近 N 条
     * 防回退窗口（服务端 eventId 去重兜底重放幂等，spec §5.4.3）。
     */
    private val doneRetention: Int = DONE_RETENTION_DEFAULT,
    /** 重放帧刷新钩子（eventId + 固化帧文本 → 重建帧文本）；null = 原样重放。
     *  置尾保持既有尾 lambda 调用形态（AtLeastOnceQueue(store, sink) { ... }）。 */
    private val refreshForResend: ((eventId: String, frameText: String) -> String)? = null,
) {

    /** 当前未完成（非 DONE）条目数。 */
    fun pendingCount(): Int = store.entries().count { it.state != JournalState.DONE }

    /**
     * 入队（先落盘后发送）。返回落盘后的 journal 条目。
     * 无论发送成败，条目均已持久——发送失败后由 [flushPending]（重连/恢复时）重放；
     * 发送成功即标 DONE（H1：at-least-once 语义由服务端 eventId 去重兜底，重放本就
     * 幂等——journal 不再依赖 ack 完成路径）。
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
        // 读回落盘条目（同 eventId 重复 append 幂等覆盖，须保留原状态/attempts）
        // 直接传入 [tryDeliver]——去掉此前每次投递的 entries() 全表扫（H1）。
        val persisted = store.entries().firstOrNull { it.eventId == eventId }
        if (persisted != null) {
            tryDeliver(persisted, refresh = false) // ② 后副作用（首投不重建：帧即新鲜）
        }
        pruneDoneRetainingWindow() // ③ 下次 enqueue 顺带清理上一轮 DONE（保留回退窗口）
        return store.entries().first { it.eventId == eventId }
    }

    /**
     * 尝试投递单条（attempts 递增；sink 异常/不可发送时条目留待 flushPending 重放）。
     * H1 两处改动：入参携带条目（调用方已持有，不再 entries() 全表扫）；发送成功
     * （[OutboundSink.send] 返回 true）即标 DONE——重放幂等由服务端去重兜底。
     */
    private fun tryDeliver(entry: JournalEntry, refresh: Boolean) {
        store.update(entry.eventId, JournalState.INFLIGHT, entry.attempts + 1)
        val frameText = if (refresh) refreshForResend?.invoke(entry.eventId, entry.frameText) ?: entry.frameText else entry.frameText
        val sent = try {
            sink.send(frameText)
        } catch (_: Exception) {
            false // 传输异常不丢条目：journal 已持久，重连后 flushPending 重放
        }
        if (sent) {
            store.update(entry.eventId, JournalState.DONE, entry.attempts + 1) // H1：发送成功即完成
        }
    }

    /** 重放所有未完成条目（连接建立/崩溃恢复时调用；注入 [refreshForResend] 时按同 eventId 重建信封）。 */
    fun flushPending() {
        store.entries().filter { it.state != JournalState.DONE }.forEach { tryDeliver(it, refresh = true) }
        pruneDoneRetainingWindow() // H1：flush 后清理本轮发送成功的 DONE（保留回退窗口）
    }

    /** 服务端确认（收到对应 eventId 的 ack/回执）：条目完成。 */
    fun onAck(eventId: String) {
        store.entries().firstOrNull { it.eventId == eventId }?.let {
            store.update(eventId, JournalState.DONE, it.attempts)
        }
    }

    /** 清理已完成条目，保留最近 [doneRetention] 条（防回退窗口；≤0 = 不清理）。 */
    private fun pruneDoneRetainingWindow() {
        if (doneRetention > 0) store.pruneDone(doneRetention)
    }

    private companion object {
        /** DONE 回退窗口缺省宽度（docs/16 H1 采纳口径 N≥32）。 */
        const val DONE_RETENTION_DEFAULT = 32
    }
}

/** 入向命令门禁（E09 guard：执行门禁与去重持久化先于副作用）。 */
class InboundCommandGate(
    private val store: JournalStore,
    /**
     * 时钟（TTL 判定用；缺省系统时钟——生产由 DeviceSessionLoop 注入会话时钟）。
     */
    private val clock: () -> Long = System::currentTimeMillis,
    /**
     * 过期跳过门（docs/16 U4，TTL 积压门）：提供者返回 >0 = 启用（每次 apply 实时
     * 取值——AppSettings 变更免重建生效）。指令接收时间距今超 TTL → 不下发副作用，
     * 记 [Decision.Expired]（调用方回执 SKIPPED(过期)）。缺省恒 null = 不启用
     * （历史行为不变）。
     */
    private val expiredTtlMillis: () -> Long? = { null },
    /** 过期命中回调（调用方发 SKIPPED 回执/事件日志；在 journal 去重记录之后调用）。 */
    private val onExpired: ((command: InboundCommand, ageMillis: Long) -> Unit)? = null,
    /** 副作用入口（执行指令；须在 journal 去重记录之后才被调用）。 */
    private val effect: (InboundCommand) -> Unit,
) {

    /**
     * 入向指令描述（来自已验帧 socketType=2 载荷）。
     *
     * [receivedAtMillis] = 指令的**服务端下发时间戳**（dispatch 帧 meta.ts，毫秒）；
     * -1 = 未携带（TTL 门无法判定 → 不拦截，保持既有行为）。回放（journal 重放/
     * 903 重发）路径同字段判定，防重放旧指令复活。
     */
    data class InboundCommand(
        val commandId: String,
        val type: Int,
        val epoch: Long,
        val receivedAtMillis: Long = -1L,
    )

    sealed interface Decision {
        object Applied : Decision
        /** 已执行过（journal 去重命中）：不得重复副作用，回执原确认。 */
        object AlreadyApplied : Decision
        /** epoch < 已应用水位：拒旧（docs/02 §5.1 端侧拒旧）。 */
        data class RejectedOld(val epoch: Long, val appliedEpoch: Long) : Decision

        /**
         * 过期跳过（U4 TTL 门）：积压超 TTL，**不下发副作用**；已在 journal 去重
         * 记录（重复投递/回放同 commandId → AlreadyApplied 幂等回执，防旧指令复活）。
         */
        data class Expired(val ageMillis: Long, val ttlMillis: Long) : Decision
    }

    /**
     * 门禁 + 副作用。顺序：
     * 1. epoch 拒旧（未持久，无副作用风险）；
     * 2. TTL 过期跳过（U4：持久去重记录先于判定返回，副作用不下发）；
     * 3. journal 记录启动（持久，先于副作用；重复 commandId → AlreadyApplied）；
     * 4. 执行副作用；
     * 5. 推进 appliedEpoch。
     */
    fun apply(command: InboundCommand): Decision {
        val watermark = store.appliedEpoch()
        if (command.epoch < watermark) {
            return Decision.RejectedOld(command.epoch, watermark)
        }
        if (!store.recordInboundStart(command.commandId, command.type, command.epoch)) {
            return Decision.AlreadyApplied
        }
        val ttl = expiredTtlMillis()
        if (ttl != null && ttl > 0 && command.receivedAtMillis >= 0) {
            val age = clock() - command.receivedAtMillis
            if (age > ttl) {
                onExpired?.invoke(command, age)
                return Decision.Expired(age, ttl)
            }
        }
        effect(command) // 副作用发生在持久化之后（崩溃恢复可由 journal 重放对账）
        if (command.epoch > watermark) {
            store.setAppliedEpoch(command.epoch)
        }
        return Decision.Applied
    }
}
