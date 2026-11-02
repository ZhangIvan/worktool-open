/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.report

import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.V1Envelope
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.WireMessage
import cn.worktool.open.feature.executor.fake.FakeReportSink
import cn.worktool.open.feature.executor.fixture.ProtocolFixtures
import cn.worktool.open.feature.executor.intent.MessageEvent
import cn.worktool.open.feature.executor.intent.MessageOrigin
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-01 新消息上报执行器场景（每执行器 ≥5：通知通道/列表通道/忽略面/
 * SR-06 脱敏/离线暂存重发/去重窗口/wire 往返）。
 *
 * 线形态依据 protocol/fixtures/wire-v1-envelope.report-101.json（spec §7
 * 冻结测试数据接口，测试直接消费该夹具做结构对照）。
 */
class MessageEventReporterTest {

    private val logs = mutableListOf<String>()
    private var now = 1_000L

    private fun reporter(sink: FakeReportSink) = MessageEventReporter(
        sink = sink,
        clock = { now },
        dedupWindowMillis = 10_000,
        log = { logs += it },
    )

    private fun notificationEvent(
        title: String,
        text: String,
        packageName: String = "com.tencent.wework",
    ) = AccessibilityEventSnapshot(
        eventType = WeworkEventRules.TYPE_NOTIFICATION_STATE_CHANGED,
        packageName = packageName,
        className = "android.app.Notification",
        texts = listOf(title, text),
        eventTimeMillis = now,
    )

    private fun conversationItemEvent(itemText: String) = AccessibilityEventSnapshot(
        eventType = WeworkEventRules.TYPE_WINDOW_CONTENT_CHANGED,
        packageName = "com.tencent.wework",
        className = "androidx.recyclerview.widget.RecyclerView",
        texts = listOf(itemText),
        eventTimeMillis = now,
    )

    // ---- ① 通知栏通道：事件 → 规范化 → type=101 上报（结构对照 101 夹具） ----

    @Test
    fun `通知事件-规范化上报101载荷形态对齐夹具`() {
        val sink = FakeReportSink()
        reporter(sink).onAccessibilityEvent(
            notificationEvent(title = "外部联系人甲", text = "您好，请问报价单何时发？"),
        )

        assertEquals("单事件单批次单载荷", 1, sink.attempts.size)
        val bean = sink.allBeans().single()
        assertEquals(MessageTypes.TYPE_RECEIVE_MESSAGE_LIST, bean.type)

        // 结构对照 protocol/fixtures/wire-v1-envelope.report-101.json（spec §7）
        val fixtureBean = fixtureReportBean()
        val sub = bean.subMessageList!!.single()
        val fixtureSub = fixtureBean.subMessageList!!.single()
        assertEquals("sender=0 他人（夹具口径）", fixtureSub.sender, sub.sender)
        val item = sub.itemMessageList!!.single()
        val fixtureItem = fixtureSub.itemMessageList!!.single()
        assertEquals("feature=2 消息内容（夹具口径）", fixtureItem.feature, item.feature)
        assertEquals("正文入 itemMessageList[0].text（夹具口径）", "您好，请问报价单何时发？", item.text)
        assertEquals("会话名入候选字段 targetRoom", "外部联系人甲", bean.extraString("targetRoom"))
    }

    // ---- ② 会话列表通道 ----

    @Test
    fun `会话列表事件-规范化上报同形态`() {
        val sink = FakeReportSink()
        reporter(sink).onAccessibilityEvent(
            conversationItemEvent("客户交付群：张三 发布了新公告"),
        )

        val bean = sink.allBeans().single()
        assertEquals(MessageTypes.TYPE_RECEIVE_MESSAGE_LIST, bean.type)
        assertEquals("外部群名", "客户交付群", bean.extraString("targetRoom"))
        assertEquals("最新一条消息", "张三 发布了新公告", bean.subMessageList!!.single().itemMessageList!!.single().text)
    }

    // ---- ③ 忽略面：非企微包 / 事件类型不符 / 文本残缺 ----

    @Test
    fun `忽略面-非企微包与非消息事件零上报`() {
        val sink = FakeReportSink()
        val r = reporter(sink)

        r.onAccessibilityEvent(notificationEvent("甲", "hi", packageName = "com.other.app"))
        r.onAccessibilityEvent(
            AccessibilityEventSnapshot(
                eventType = 2048,
                packageName = "com.tencent.wework",
                className = "android.widget.TextView",
                texts = listOf("随便什么"),
                eventTimeMillis = now,
            ),
        )
        r.onAccessibilityEvent(notificationEvent("只有标题没有正文", "")) // 正文空 → 解析不出

        assertEquals("全部忽略，零上报", 0, sink.attempts.size)
        assertEquals(0, logs.size)
    }

    // ---- ④ SR-06 脱敏：日志面零正文零手机号 ----

    @Test
    fun `脱敏-日志只含长度元数据不含正文与手机号`() {
        val sink = FakeReportSink()
        val secret = "联系方式 13812345678，报价单见附件"
        reporter(sink).onAccessibilityEvent(notificationEvent("客户乙", secret))

        assertEquals("上报载荷照常含正文（FR-01 目的）", secret, sink.allBeans().single().subMessageList!!.single().itemMessageList!!.single().text)
        val logText = logs.joinToString("\n")
        assertFalse("日志不回显正文", logText.contains(secret))
        assertFalse("日志不回显手机号", logText.contains("13812345678"))
        assertTrue("日志保留长度元数据", logText.contains("len=${secret.length}"))
    }

    // ---- ⑤ 离线暂存重发（FR-01 验收：离线暂存重发） ----

    @Test
    fun `离线暂存-sink不可投递时入暂存恢复后重投`() {
        val sink = FakeReportSink()
        val r = reporter(sink)

        sink.accept = false
        val offline = r.report(MessageEvent(MessageOrigin.NOTIFICATION, targetRoom = "群A", content = "离线期消息", eventTimeMillis = now))
        assertFalse("首轮投递失败", offline)
        assertEquals(1, r.pendingCount())

        now += 5_000
        sink.accept = true
        val second = r.report(MessageEvent(MessageOrigin.NOTIFICATION, targetRoom = "群B", content = "恢复后消息", eventTimeMillis = now))
        assertTrue(second)

        assertEquals("暂存 1 条已随恢复重投", 0, r.pendingCount())
        assertEquals(
            "暂存条目先于新事件保序投出（只看接受轮次）",
            listOf("离线期消息", "恢复后消息"),
            sink.acceptedBeans().map { it.subMessageList!!.single().itemMessageList!!.single().text!! },
        )
    }

    @Test
    fun `暂存上限-FIFO淘汰最旧并记日志`() {
        val sink = FakeReportSink()
        val r = MessageEventReporter(
            sink = sink,
            clock = { now },
            dedupWindowMillis = 0, // 关去重，纯测上限
            maxPending = 2,
            log = { logs += it },
        )
        sink.accept = false
        r.report(MessageEvent(MessageOrigin.NOTIFICATION, targetRoom = "群", content = "m1", eventTimeMillis = now))
        r.report(MessageEvent(MessageOrigin.NOTIFICATION, targetRoom = "群", content = "m2", eventTimeMillis = now))
        r.report(MessageEvent(MessageOrigin.NOTIFICATION, targetRoom = "群", content = "m3", eventTimeMillis = now))

        assertEquals(2, r.pendingCount())
        now += 60_000
        sink.accept = true
        assertEquals("淘汰最旧，保序重投 m2/m3", 2, r.flushPending())
        assertEquals(
            listOf("m2", "m3"),
            sink.acceptedBeans().map { it.subMessageList!!.single().itemMessageList!!.single().text!! },
        )
        assertTrue("淘汰留痕（脱敏形态）", logs.any { it.contains("report-pending-evict") && it.contains("len=2") })
    }

    // ---- ⑥ 双通道去重窗口 ----

    @Test
    fun `去重窗口-双通道同消息只报一次窗口外可再报`() {
        val sink = FakeReportSink()
        val r = reporter(sink)

        r.onAccessibilityEvent(notificationEvent("群A", "同一条消息"))
        r.onAccessibilityEvent(conversationItemEvent("群A: 同一条消息")) // 双通道重复到达

        assertEquals("窗口内同指纹只报一次", 1, sink.acceptedBeans().size)

        now += 10_001 // 越过窗口（同内容新事件 = 新消息，允许再报）
        r.onAccessibilityEvent(notificationEvent("群A", "同一条消息"))
        assertEquals(2, sink.acceptedBeans().size)
    }

    // ---- ⑦ wire 往返：经 core:protocol 信封编码解码等价 ----

    @Test
    fun `wire往返-101载荷装入socketType2信封编码解码等价`() {
        val sink = FakeReportSink()
        reporter(sink).onAccessibilityEvent(
            notificationEvent(title = "外部联系人甲", text = "您好，请问报价单何时发？"),
        )
        val bean = sink.allBeans().single()

        val codec = EnvelopeCodec()
        val envelope = V1Envelope(socketType = MessageTypes.SOCKET_MESSAGE_LIST, list = listOf(bean), listPresent = true)
        val decoded = codec.decode(codec.encodeV1(envelope)) as WireMessage.V1

        assertEquals(MessageTypes.SOCKET_MESSAGE_LIST, decoded.envelope.socketType)
        val back = decoded.envelope.list.single()
        assertEquals(bean.type, back.type)
        assertEquals(bean.roomType, back.roomType)
        assertEquals(bean.textType, back.textType)
        assertEquals(
            bean.subMessageList!!.single().itemMessageList!!.single().text,
            back.subMessageList!!.single().itemMessageList!!.single().text,
        )
        assertEquals(bean.extraString("targetRoom"), back.extraString("targetRoom"))
    }

    // ---- 工具：消费 protocol 权威夹具（spec §7） ----

    private fun fixtureReportBean(): WeworkMessageBean {
        val wire = EnvelopeCodec().decode(ProtocolFixtures.text("wire-v1-envelope.report-101.json"))
        return (wire as WireMessage.V1).envelope.list.single()
    }
}
