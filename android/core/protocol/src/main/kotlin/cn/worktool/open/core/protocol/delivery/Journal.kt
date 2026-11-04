/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.delivery

/**
 * 持久 journal 接口（先落盘后副作用语义的唯一持久层抽象，docs/02 §5.1/§5.2）。
 *
 * 生产实现 = SQLite（core:protocol-android 的 SqliteJournalStore，单库单写）；
 * JVM 单测 = [InMemoryJournalStore]（同接口等价语义，崩溃模拟用 [InMemoryJournalStore.snapshot]）。
 *
 * 端侧语义（protocol/state-transitions.csv E09 guard / spec §5.4.5）：
 * - 出向：AtLeastOnce 队列条目先 append（PENDING）再发送；崩溃恢复重放未完成条目；
 * - 入向：执行门禁与去重记录先于副作用；`appliedEpoch` 拒旧（docs/02 §5.1）。
 */
interface JournalStore {

    /** 追加出向条目（同 eventId 重复 append = 幂等覆盖保留原状态）。 */
    fun append(entry: JournalEntry)

    /** 更新出向条目状态/尝试计数（不存在则忽略）。 */
    fun update(eventId: String, state: JournalState, attempts: Int)

    /** 全量出向条目（崩溃恢复重放输入）。 */
    fun entries(): List<JournalEntry>

    /** 记录入向命令启动（执行门禁）：返回 false = 该 commandId 已记录（去重，不得重复副作用）。 */
    fun recordInboundStart(commandId: String, type: Int, epoch: Long): Boolean

    /** 端侧已应用的最小命令 epoch（hello.lastEpoch 来源，服务端不得下发更小 epoch）。 */
    fun appliedEpoch(): Long

    fun setAppliedEpoch(epoch: Long)

    /**
     * 清理出向已完成（DONE）条目，仅保留最近 [keep] 条（docs/16 性能审计 H1）：
     * journal 此前永不清理 → flushPending 全量重放历史帧（最旧优先）、新帧永远排队尾，
     * 跨重连滞留 5.5 分钟。DONE 化后由队列在 enqueue/flushPending 后调用本方法做
     * 防回退窗口裁剪（重放幂等由服务端 eventId 去重兜底，spec §5.4.3）。
     * 缺省 no-op：未覆写的实现不清理（接口向后兼容，行为同历史版本）。
     */
    fun pruneDone(keep: Int) {}
}

/** 出向条目状态机：PENDING → INFLIGHT →（服务端确认）DONE。 */
enum class JournalState { PENDING, INFLIGHT, DONE }

/** 出向 journal 条目（eventId 承载服务端幂等去重，spec §5.4.3）。 */
data class JournalEntry(
    val eventId: String,
    val frameText: String,
    val createdAt: Long,
    val state: JournalState = JournalState.PENDING,
    val attempts: Int = 0,
)

/** JVM 单测用内存实现（线程安全；snapshot 支持崩溃→恢复模拟）。 */
class InMemoryJournalStore : JournalStore {

    private val lock = Any()
    private val outbound = LinkedHashMap<String, JournalEntry>()
    private val inbound = HashMap<String, Long>()
    private var epoch = 0L

    override fun append(entry: JournalEntry) {
        synchronized(lock) {
            outbound.putIfAbsent(entry.eventId, entry)
        }
    }

    override fun update(eventId: String, state: JournalState, attempts: Int) {
        synchronized(lock) {
            outbound[eventId]?.let { outbound[eventId] = it.copy(state = state, attempts = attempts) }
        }
    }

    override fun entries(): List<JournalEntry> = synchronized(lock) { outbound.values.toList() }

    override fun recordInboundStart(commandId: String, type: Int, epoch: Long): Boolean = synchronized(lock) {
        if (inbound.containsKey(commandId)) {
            false
        } else {
            inbound[commandId] = epoch
            true
        }
    }

    override fun appliedEpoch(): Long = synchronized(lock) { epoch }

    override fun setAppliedEpoch(epoch: Long) {
        synchronized(lock) { this.epoch = epoch }
    }

    override fun pruneDone(keep: Int) = synchronized(lock) {
        // 插入序 = created_at 序（LinkedHashMap）→ 保留最近 keep 条 DONE，其余删除。
        if (keep > 0) {
            outbound.values.filter { it.state == JournalState.DONE }
                .dropLast(keep)
                .forEach { outbound.remove(it.eventId) }
        }
    }

    /** 深拷贝快照（模拟崩溃后重启重读 SQLite）。 */
    fun snapshot(): InMemoryJournalStore {
        val copy = InMemoryJournalStore()
        synchronized(lock) {
            copy.outbound.putAll(outbound.mapValues { it.value.copy() })
            copy.inbound.putAll(inbound)
            copy.epoch = epoch
        }
        return copy
    }
}
