/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import java.security.MessageDigest

/**
 * 本地已执行事件台账（重连合并与去重，docs/02 §5.2 / spec §5.4.5）。
 *
 * 语义：
 * - 端侧执行本地规则**先记账（含 eventHash）后副作用**（对齐协议层 AtLeastOnce 队列
 *   「先落盘后发送」，docs/02 §5.1）；
 * - 同一 (ruleId, eventHash) 重复到来 = 同一事件重复观测 → 跳过执行（端侧幂等）；
 * - 重连后 [pendingReports] 产出未上报事件，由引擎以 `source=local` 载荷上报；
 *   服务端按 (ruleId, eventHash) 同键去重（spec §5.4.5），故 AtLeastOnce 重复上报安全。
 */
object EventHashes {

    /** SHA-256 hex（小写，64 字符）。 */
    fun sha256Hex(canonical: String): String =
        MessageDigest.getInstance("SHA-256")
            .digest(canonical.toByteArray(Charsets.UTF_8))
            .joinToString("") { "%02x".format(it) }

    /**
     * 事件标识 canonical 串：规则 + 指令类型 + 目标 + 发送人 + 文本 + 事件时间。
     * 同一事件（同一观测）跨重放产生相同 hash；不同发生（不同时间戳/内容）不同 hash。
     * null 字段以空串占位；列表以 U+001F（单元分隔符）拼接避免与普通文本歧义。
     */
    fun canonicalOf(
        ruleId: String,
        commandType: Int,
        targetRoom: String?,
        senderNames: List<String>,
        text: String,
        eventTimeMillis: Long,
    ): String = listOf(
        ruleId,
        commandType.toString(),
        targetRoom ?: "",
        senderNames.joinToString(SEP_UNIT),
        text,
        eventTimeMillis.toString(),
    ).joinToString(SEP_FIELD)

    /** 字段分隔符（U+0002 STX，不选 '|' 避免与文本歧义）。 */
    private const val SEP_FIELD: String = "\u0002"

    /** 列表单元分隔符（U+001F）。 */
    private const val SEP_UNIT: String = "\u001F"

    fun compute(
        ruleId: String,
        commandType: Int,
        targetRoom: String?,
        senderNames: List<String>,
        text: String,
        eventTimeMillis: Long,
    ): String = sha256Hex(canonicalOf(ruleId, commandType, targetRoom, senderNames, text, eventTimeMillis))
}

/** 本地已执行事件（上报载荷的端侧事实源）。 */
data class LocalExecEvent(
    val ruleId: String,
    val eventHash: String,
    val commandType: Int,
    val roomType: Int? = null,
    val targetRoom: String? = null,
    /** 动作摘要（发送内容/打招呼；source=local 上报载荷必要字段）。 */
    val content: String? = null,
    val executedAtMillis: Long,
    val reported: Boolean = false,
)

/**
 * 台账持久层接口。JVM 测试/原型 = [InMemoryLocalEventStore]；生产 = 端侧 SQLite
 * （随 app 设备侧落地接线，与 core:protocol-android 的 SqliteJournalStore 同库策略，
 * T3.3 JVM 范围内不强制）。
 */
interface LocalEventStore {

    /** 插入；false = (ruleId, eventHash) 已存在（重复观测）。 */
    fun insert(event: LocalExecEvent): Boolean

    fun all(): List<LocalExecEvent>

    fun markReported(ruleId: String, eventHash: String)
}

/** 内存实现（线程安全；保持插入顺序）。 */
class InMemoryLocalEventStore : LocalEventStore {
    private val lock = Any()
    private val events = LinkedHashMap<Pair<String, String>, LocalExecEvent>()

    override fun insert(event: LocalExecEvent): Boolean = synchronized(lock) {
        events.putIfAbsent(event.ruleId to event.eventHash, event) == null
    }

    override fun all(): List<LocalExecEvent> = synchronized(lock) { events.values.toList() }

    override fun markReported(ruleId: String, eventHash: String) {
        synchronized(lock) {
            events[ruleId to eventHash]?.let { events[ruleId to eventHash] = it.copy(reported = true) }
        }
    }
}

/** 台账门面：去重记账 + 待上报视图。 */
class LocalEventLedger(private val store: LocalEventStore) {

    /**
     * 记账（执行前置条件）。返回 false = (ruleId, eventHash) 重复，调用方不得再执行副作用。
     */
    fun recordExecuted(event: LocalExecEvent): Boolean = store.insert(event)

    /** 未上报事件（重连合并输入，按发生时间升序）。 */
    fun pendingReports(): List<LocalExecEvent> = store.all().filter { !it.reported }.sortedBy { it.executedAtMillis }

    /** 上报成功（AtLeastOnce 队列已持久化）后标记。 */
    fun markReported(event: LocalExecEvent) = store.markReported(event.ruleId, event.eventHash)

    fun all(): List<LocalExecEvent> = store.all()
}
