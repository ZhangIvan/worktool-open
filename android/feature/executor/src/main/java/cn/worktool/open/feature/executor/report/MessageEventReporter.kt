/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.report

import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.core.protocol.ItemMessageBean
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.SubMessageBean
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.feature.executor.intent.MessageEvent
import cn.worktool.open.feature.executor.intent.PayloadFields
import kotlinx.serialization.json.JsonPrimitive

/**
 * 新消息上报执行器（FR-01，type=101 端→云；docs/05 P0 首批行「101/102」）。
 *
 * 职责链：无障碍事件快照 → [AccessibilityMessageParser] 规范化 [MessageEvent]
 * → 映射 `WeworkMessageBean(type=101)` → 经 [MessageReportSink]（生产=
 * core:protocol `ProtocolClient.V2Session.sendReport`）上报。
 *
 * 内建机制：
 * - **消抖去重**（[dedupWindowMillis]）：同一消息经通知栏+会话列表双通道到达时，
 *   窗口内同指纹只报一次（指纹 = 会话+正文+发送人）；
 * - **离线暂存重发**（FR-01 验收「离线暂存重发」的内存面）：sink 不可投递时入
 *   [pending]（上限 [maxPending]，FIFO 淘汰），[flushPending] 重投——**持久化**
 *   暂存归协议层 AtLeastOnce journal（端口实现方负责，见 [MessageReportSink]）；
 * - **SR-06 脱敏**：全部日志行经 [SensitiveRedactor]（正文只出长度元数据）。
 *
 * 真机链路（无障碍服务事件订阅接线）= W-M3R 任务卡（dodDeferred）；本类交付
 * JVM 纯状态机（FakeSink 可全场景复现）。
 */
class MessageEventReporter(
    private val sink: MessageReportSink,
    private val clock: () -> Long = { 0L },
    /** 同指纹去重窗口毫秒。 */
    private val dedupWindowMillis: Long = DEFAULT_DEDUP_WINDOW_MS,
    /** 内存暂存上限（超出 FIFO 淘汰最旧并记日志）。 */
    private val maxPending: Int = DEFAULT_MAX_PENDING,
    /** 日志出口（生产接 logcat；SR-06 由 [SensitiveRedactor] 保证）。 */
    private val log: (String) -> Unit = {},
    private val parser: AccessibilityMessageParser = AccessibilityMessageParser(clock = clock),
) {

    private data class Pending(val bean: WeworkMessageBean, val event: MessageEvent)

    /** 指纹 → 最近上报时间（去重窗口）。 */
    private val recentHashes = LinkedHashMap<String, Long>()

    private val pending = ArrayDeque<Pending>()

    /** 无障碍事件入口（服务订阅回调接线；解析不出 = 忽略）。 */
    fun onAccessibilityEvent(event: AccessibilityEventSnapshot) {
        val parsed = parser.parse(event) ?: return
        report(parsed)
    }

    /** 上报单个事件；false = 本轮不可投递（已入暂存）。 */
    fun report(event: MessageEvent): Boolean {
        val hash = event.eventHash()
        if (isDuplicate(hash)) {
            log("report-dedup skip ${SensitiveRedactor.eventLogLine("101", event.targetRoom, event.content, event.senderNames.size)}")
            return true
        }
        // 保序：有暂存时先重投（仍不可投递则新事件随暂存，不乱序）
        if (pending.isNotEmpty()) flushPending()
        val bean = event.toReportBean()
        val delivered = sink.sendReport(listOf(bean))
        if (delivered) {
            markReported(hash)
            log("report-ok ${SensitiveRedactor.eventLogLine("101", event.targetRoom, event.content, event.senderNames.size)}")
            return true
        }
        pending.addLast(Pending(bean, event))
        while (pending.size > maxPending) {
            val evicted = pending.removeFirst()
            log("report-pending-evict ${SensitiveRedactor.eventLogLine("101", evicted.event.targetRoom, evicted.event.content, evicted.event.senderNames.size)}")
        }
        log("report-pending ${SensitiveRedactor.eventLogLine("101", event.targetRoom, event.content, event.senderNames.size)}")
        return false
    }

    /** 重投全部暂存（恢复连接后调用）；返回本轮送达条数。 */
    fun flushPending(): Int {
        var delivered = 0
        while (pending.isNotEmpty()) {
            val head = pending.first()
            if (!sink.sendReport(listOf(head.bean))) break // 仍不可投递：保序停止
            pending.removeFirst()
            markReported(head.event.eventHash())
            delivered++
            log("report-flush-ok ${SensitiveRedactor.eventLogLine("101", head.event.targetRoom, head.event.content, head.event.senderNames.size)}")
        }
        return delivered
    }

    /** 当前暂存条数（观测/测试）。 */
    fun pendingCount(): Int = pending.size

    private fun isDuplicate(hash: String): Boolean {
        val seenAt = recentHashes[hash] ?: return false
        val now = clock()
        if (now - seenAt > dedupWindowMillis) {
            recentHashes.remove(hash)
            return false
        }
        return true
    }

    private fun markReported(hash: String) {
        recentHashes[hash] = clock()
        // 窗口外条目顺带清理（防无界增长；保序遍历删除最旧）
        val now = clock()
        val expired = recentHashes.entries.filter { now - it.value > dedupWindowMillis }
        expired.forEach { recentHashes.remove(it.key) }
    }

    companion object {
        /** 默认去重窗口（双通道重复到达的时间容差；真机校准点）。 */
        const val DEFAULT_DEDUP_WINDOW_MS: Long = 10_000

        /** 默认内存暂存上限。 */
        const val DEFAULT_MAX_PENDING: Int = 200
    }
}

/**
 * 事件 → 上报载荷（type=101）。
 *
 * 线形态依据 protocol/fixtures/wire-v1-envelope.report-101.json（spec §7 冻结
 * 测试数据接口）：正文入 `subMessageList[0].itemMessageList[0]`（feature=2
 * 消息内容，spec §4.3），发送人入 `nameList`；会话名为候选字段 `targetRoom`
 * （spec §4.4 待 T3.2 冻结，取值与指令侧 [PayloadFields.TARGET_ROOM] 同名）。
 */
internal fun MessageEvent.toReportBean(): WeworkMessageBean = WeworkMessageBean(
    type = MessageTypes.TYPE_RECEIVE_MESSAGE_LIST,
    roomType = roomType,
    textType = textType,
    subMessageList = listOf(
        SubMessageBean(
            sender = OTHER_SENDER,
            textType = textType,
            itemMessageList = listOf(ItemMessageBean(feature = FEATURE_MESSAGE_CONTENT, text = content)),
            nameList = senderNames.ifEmpty { emptyList() },
        ),
    ),
    extras = mapOf(PayloadFields.TARGET_ROOM to JsonPrimitive(targetRoom)),
)

/** spec §4.3 SubMessageBean.sender：0=他人（入站消息固定他人）。 */
private const val OTHER_SENDER = 0

/** spec §4.3 ItemMessageBean.feature：2=消息内容。 */
private const val FEATURE_MESSAGE_CONTENT = 2
