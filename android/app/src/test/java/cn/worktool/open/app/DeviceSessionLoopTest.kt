/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.EnvelopeCodec
import cn.worktool.open.core.protocol.FrameMeta
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.crypto.FrameAuth
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.delivery.InMemoryJournalStore
import cn.worktool.open.core.protocol.session.FrameBuilder
import cn.worktool.open.core.protocol.session.SendSequencer
import cn.worktool.open.core.protocol.transport.Cancellable
import cn.worktool.open.core.protocol.transport.Scheduler
import cn.worktool.open.core.protocol.transport.TransportListener
import cn.worktool.open.core.protocol.transport.WsTransport
import cn.worktool.open.feature.executor.exec.CommandExecutor
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager
import cn.worktool.open.feature.executor.runtime.PauseManager
import cn.worktool.open.feature.executor.runtime.UiDriver
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.atomic.AtomicInteger

/**
 * T3.4 执行回路 JVM 级联测试（DoD：执行回路 JVM 级联测试绿）：
 *
 * 会话 onFrame（FrameVerifier 验签）→ hello_ack 建会话 → 指令下发帧（含 epoch）→
 * InboundCommandGate（journal 先落盘+去重+拒旧）→ IntentNormalizer → ExecutorRegistry →
 * CommandRunner（UiDriver）→ RAW_CONFIRM（socketType=3）经 AtLeastOnce 队列回报；
 * 含控制帧（903/904）与幂等回执路径。传输 = 本测试内 Fake（协议层语义真实走全链）。
 */
class DeviceSessionLoopTest {

    private val tokenHex = "5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4"
    private val token = Hex.decode(tokenHex)
    private var now = 1_793_664_000_000L

    private val codec = EnvelopeCodec()

    /** 计数执行器（203）：无界面成功路径——验证回路接线，UI 语义归 feature:executor 车道测试。 */
    private class CountingSendTextExecutor(val clicks: AtomicInteger) : CommandExecutor {
        override val commandType: Int get() = MessageTypes.SEND_MESSAGE
        override fun riskLevel(intent: CommandIntent) = cn.worktool.open.feature.executor.risk.RiskLevel.L1
        override fun buildPlan(intent: CommandIntent): ExecutionPlan {
            val send = intent as CommandIntent.SendText
            return ExecutionPlan(
                commandType = commandType,
                riskLevel = cn.worktool.open.feature.executor.risk.RiskLevel.L1,
                target = send.targetRoom,
                paramsSummary = "content=${send.content.take(8)}…",
                steps = listOf(
                    PlanStep("noop-execute", "执行（计数）") { _ ->
                        clicks.incrementAndGet()
                        StepOutcome.success()
                    },
                ),
            )
        }
    }

    private class StubDriver : UiDriver {
        var now = 0L
        override fun currentRoot(): cn.worktool.open.core.accessibility.tree.UiNode? = null
        override fun click(node: cn.worktool.open.core.accessibility.tree.UiNode) = true
        override fun inputText(node: cn.worktool.open.core.accessibility.tree.UiNode, text: String) = true
        override fun scroll(node: cn.worktool.open.core.accessibility.tree.UiNode, direction: cn.worktool.open.core.accessibility.tree.ScrollDirection) = true
        override fun goBack() = true
        override fun sleep(ms: Long) { now += ms }
        override fun nowMillis(): Long = now
    }

    private class ImmediateScheduler : Scheduler {
        override fun schedule(delayMillis: Long, task: () -> Unit): Cancellable = object : Cancellable {
            override fun cancel() = Unit
        }
    }

    private class FakeTransport : WsTransport {
        var listener: TransportListener? = null
        var open = false
        val sent = mutableListOf<String>()
        override fun connect(url: String, listener: TransportListener) {
            this.listener = listener
            open = true
            listener.onOpen()
        }
        override fun send(text: String): Boolean {
            if (!open) return false
            sent.add(text)
            return true
        }
        override fun close(code: Int, reason: String) {
            if (open) {
                open = false
                listener?.onClosed(code, reason)
            }
        }
        fun serverMessage(text: String) = listener?.onMessage(text)
        class Factory(private val t: FakeTransport) : WsTransport.Factory {
            override fun create(): WsTransport = t
        }
    }

    /** 服务端侧帧构造器（与设备同 token 签名——双向逐帧鉴权）。seq 预推进 1：
     *  hello_ack 占服务端出向 seq=1，首个指令帧自 seq=2 起（spec §5.4.2 v1.2.0）。 */
    private val serverBuilder = FrameBuilder(
        codec = codec,
        deviceToken = { token },
        tokenVersion = { 1 },
        crypto = null,
        clock = { now },
        sequencer = SendSequencer().apply { next() },
    )

    private fun helloAckText(): String {
        val digest = FrameAuth.payloadDigestOfList(null)
        val eventId = java.util.UUID.randomUUID().toString()
        val nonce = "7e3b98e90a47796c8e86633fddac8d8f"
        val hmac = FrameAuth.hmacHex(token, "", eventId, 1, now, nonce, digest)
        return Json.encodeToString(
            kotlinx.serialization.json.JsonElement.serializer(),
            buildJsonObject {
                put("type", "hello_ack")
                put("serverTime", now)
                put("resumeFromSeq", 1)
                put("heartbeatIntervalSeconds", 15)
                put("sessionTimeoutSeconds", 45)
                put("meta", codec.metaElement(FrameMeta(2, now, nonce, hmac, 1, eventId, "", 1)))
            },
        )
    }

    private fun commandFrame(commandId: String, epoch: Long): String =
        serverBuilder.build(
            socketType = MessageTypes.SOCKET_MESSAGE_LIST,
            commandId = commandId,
            apiSend = 1,
            list = listOf(
                WeworkMessageBean(
                    type = MessageTypes.SEND_MESSAGE,
                    roomType = 1,
                    extras = mapOf(
                        "targetRoom" to JsonPrimitive("外部群E2E测试群"),
                        "receivedContent" to JsonPrimitive("级联测试消息"),
                        "epoch" to JsonPrimitive(epoch),
                    ),
                ),
            ),
        ).text

    private fun controlFrame(vararg beans: WeworkMessageBean): String =
        serverBuilder.build(
            socketType = MessageTypes.SOCKET_MESSAGE_LIST,
            apiSend = 0,
            list = beans.toList(),
        ).text

    private fun newLoop(
        transport: FakeTransport,
        journal: InMemoryJournalStore,
        clicks: AtomicInteger,
        pause: PauseManager = NeverPausedPauseManager(),
        events: MutableList<String> = mutableListOf(),
        dryRunGate: (() -> Boolean)? = null,
    ): DeviceSessionLoop {
        val credentials = DeviceCredentials(
            deviceId = "device-9f2c7b41",
            deviceTokenHex = tokenHex,
            tokenVersion = 1,
            wsPath = "/api/v2/device-sessions/5f0f6e58-7a9b-4c1d-8e2f-3a4b5c6d7e80",
        )
        val registry = ExecutorRegistry.of(CountingSendTextExecutor(clicks))
        val loop = DeviceSessionLoop(
            credentials = credentials,
            baseUrl = "wss://example.com",
            sessionCrypto = null,
            journal = journal,
            scheduler = ImmediateScheduler(),
            clock = { now },
            registry = registry,
            driverProvider = { StubDriver() },
            pauseManager = pause,
            transportFactory = FakeTransport.Factory(transport),
            onEvent = { events.add(it) },
            dryRunGate = dryRunGate,
        )
        return loop
    }

    // ---- ① 全链：hello_ack → 指令(epoch) → 执行 → AtLeastOnce → RAW_CONFIRM ----

    @Test
    fun `执行回路全链-hello_ack 建会话-指令执行-RAW_CONFIRM 回报`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks)
        loop.start()

        // hello_ack（独立控制帧，T3.4 冻结形态）→ 会话建立
        transport.serverMessage(helloAckText())
        assertEquals(1L, loop.confirmedSeq())

        // 指令下发（seq=2，载荷含 epoch=1）
        val commandId = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f"
        transport.serverMessage(commandFrame(commandId, epoch = 1))
        assertEquals(1, clicks.get()) // 经 Normalizer→Registry→Runner 真实执行

        // 出向：hello（greeting）+ RAW_CONFIRM（socketType=3，AtLeastOnce 先落盘）
        val confirm = transport.sent.drop(1).single()
        val confirmObj = Json.parseToJsonElement(confirm).jsonObject
        assertEquals(3, confirmObj["socketType"]!!.jsonPrimitive.content.toInt())
        assertEquals(commandId, confirmObj["meta"]!!.jsonObject["commandId"]!!.jsonPrimitive.content)
        val bean = codec.beans(confirmObj["list"]!!).single()
        assertEquals(MessageTypes.SEND_MESSAGE, bean.type)
        assertEquals(0, bean.errorCode)
        assertEquals(commandId, Json.parseToJsonElement(bean.metaJson!!).jsonObject["dispatchId"]!!.jsonPrimitive.content)
        assertTrue("执行结果须先落盘（AtLeastOnce）", journal.entries().isNotEmpty())
        assertEquals(2L, loop.confirmedSeq())
        loop.stop()
    }

    // ---- ② 重复下发：commandId 去重 → 幂等回执 = 同 eventId 重建帧（不重复执行） ----

    @Test
    fun `重复下发幂等-去重后同eventId重建回执不重复执行`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks)
        loop.start()
        transport.serverMessage(helloAckText())
        val commandId = "d2c3b4a5-f6e7-4d8c-9a0b-1d2e3f4a5b6c"
        transport.serverMessage(commandFrame(commandId, epoch = 1))
        assertEquals(1, clicks.get())
        val sentAfterFirst = transport.sent.size

        // 同 commandId 新 eventId 重发（服务端补投场景）→ gate 去重 → 幂等回执重建帧。
        // 冻结入站规则（§5.2.4，Go gateway v2.go）：nonce 5min 查重 / ts ±30s /
        // seq 严格 == InSeq+1（幂等重发帧同样消费帧序）——原样重发旧帧文本必被
        // 4402/4403/4405+903 拒收；python 规范客户端重放形态 = 同 eventId + 新鲜信封。
        transport.serverMessage(commandFrame(commandId, epoch = 2))
        assertEquals("去重后不得重复执行", 1, clicks.get())
        assertEquals("幂等回执 = 重建一帧", sentAfterFirst + 1, transport.sent.size)

        val original = Json.parseToJsonElement(
            transport.sent.first { it.contains("\"socketType\":3") },
        ).jsonObject
        val reAck = Json.parseToJsonElement(transport.sent.last()).jsonObject
        val origMeta = original["meta"]!!.jsonObject
        val reAckMeta = reAck["meta"]!!.jsonObject

        // 同 eventId（§5.4.3 幂等去重键）+ 载荷逐值一致（→ 同 payloadDigest）
        assertEquals("回执 eventId 须与原确认一致", origMeta["eventId"]!!, reAckMeta["eventId"]!!)
        assertEquals("载荷逐值一致（同 payloadDigest 前提）", original["list"]!!, reAck["list"]!!)
        assertEquals(commandId, reAckMeta["commandId"]!!.jsonPrimitive.content)
        // 新鲜信封值：nonce 不复用、seq 取下一帧序（hello=1 → 原确认=2 → 重建=3）
        assertTrue(
            "nonce 须新鲜（复用即 nonce 重放 4402）",
            origMeta["nonce"]!!.jsonPrimitive.content != reAckMeta["nonce"]!!.jsonPrimitive.content,
        )
        assertEquals("原确认 seq（hello=1 后首帧）", 2L, origMeta["seq"]!!.jsonPrimitive.content.toLong())
        assertEquals("重建帧 seq 须为下一帧序（旧 seq = 4405+903 关流）", 3L, reAckMeta["seq"]!!.jsonPrimitive.content.toLong())
        assertTrue(origMeta["ts"]!!.jsonPrimitive.content.toLong() <= reAckMeta["ts"]!!.jsonPrimitive.content.toLong())

        // 重算 HMAC（C-9 输入式）：对新信封值逐字节复算必过——「原样重发」做不到这一点
        val reAckSeq = reAckMeta["seq"]!!.jsonPrimitive.content.toLong()
        val reAckTs = reAckMeta["ts"]!!.jsonPrimitive.content.toLong()
        val digest = FrameAuth.payloadDigestOfList(reAck["list"])
        assertTrue(
            "重建帧 HMAC 须按新 seq/ts/nonce 重算",
            FrameAuth.verifyHmac(
                token,
                reAckMeta["hmac"]!!.jsonPrimitive.content,
                commandId,
                reAckMeta["eventId"]!!.jsonPrimitive.content,
                reAckSeq, reAckTs,
                reAckMeta["nonce"]!!.jsonPrimitive.content,
                digest,
            ),
        )
        loop.stop()
    }

    // ---- ③ epoch 拒旧 ----

    @Test
    fun `epoch 拒旧-旧 epoch 补投不执行`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks)
        loop.start()
        transport.serverMessage(helloAckText())
        transport.serverMessage(commandFrame("e3d4c5b6-a7f8-4e9d-8b0c-2d3e4f5a6b7d", epoch = 5))
        assertEquals(1, clicks.get())
        // 旧 epoch（< 已应用水位 5）→ 拒旧
        transport.serverMessage(commandFrame("f4e5d6c7-b8a9-4f0e-8c1d-3e4f5a6b7c8e", epoch = 3))
        assertEquals("旧 epoch 不得执行", 1, clicks.get())
        loop.stop()
    }

    // ---- ④ 904 补报请求 → journal_replay 回报 ----

    @Test
    fun `904 请求触发执行台账补报`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks)
        loop.start()
        transport.serverMessage(helloAckText())
        val commandId = "0a1b2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d"
        transport.serverMessage(commandFrame(commandId, epoch = 1))
        val sentBefore = transport.sent.size

        // 服务端 904 补报请求（Go sendJournalRequest 现产形态）
        transport.serverMessage(
            controlFrame(WeworkMessageBean(type = 904, extras = mapOf("request" to JsonPrimitive("journal_replay")))),
        )
        val replay = transport.sent.drop(sentBefore).single()
        val replayObj = Json.parseToJsonElement(replay).jsonObject
        assertEquals(2, replayObj["socketType"]!!.jsonPrimitive.content.toInt())
        val payload = codec.beans(replayObj["list"]!!).single()
        assertEquals(904, payload.type)
        val entries = (payload.extras["entries"] as kotlinx.serialization.json.JsonArray)
        assertEquals("补报条目数 = 已执行台账", 1, entries.size)
        val entry = entries.first().jsonObject
        assertEquals(commandId, entry["commandId"]!!.jsonPrimitive.content)
        assertEquals("success", entry["outcome"]!!.jsonPrimitive.content)
        assertEquals("local", entry["source"]!!.jsonPrimitive.content)
        assertEquals(1L, entry["epoch"]!!.jsonPrimitive.content.toLong())
        loop.stop()
    }

    // ---- ⑤ 903：仅留痕（重连重放归传输层状态机）----

    @Test
    fun `903 重同步请求留痕不中断`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val events = mutableListOf<String>()
        val loop = newLoop(transport, journal, clicks, events = events)
        loop.start()
        transport.serverMessage(helloAckText())
        transport.serverMessage(controlFrame(WeworkMessageBean(type = 903)))
        assertTrue(events.any { it.contains("903") })
        assertEquals(0, clicks.get())
        loop.stop()
    }

    // ---- ⑥ FR-52 暂停：不发终态回执 ----

    private class AlwaysPaused : PauseManager {
        override fun isPaused() = true
        override fun pauseSource(): String? = "test-takeover"
    }

    @Test
    fun `暂停拦截-不发终态回执`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks, pause = AlwaysPaused())
        loop.start()
        transport.serverMessage(helloAckText())
        val commandId = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5e"
        transport.serverMessage(commandFrame(commandId, epoch = 1))
        assertEquals("暂停期不得执行", 0, clicks.get())
        assertTrue(
            "暂停指令不得产生 RAW_CONFIRM",
            transport.sent.none { it.contains("\"socketType\":3") },
        )
        loop.stop()
    }

    // ---- ⑦ FR-54 端侧干跑门（主界面快捷操作开关）：零副作用 + 回执同构 + 审计标记 ----

    @Test
    fun `端侧干跑-指令零执行-回执同构携带deviceDryRun标记`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val events = mutableListOf<String>()
        val loop = newLoop(transport, journal, clicks, events = events, dryRunGate = { true })
        loop.start()
        transport.serverMessage(helloAckText())
        val commandId = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
        transport.serverMessage(commandFrame(commandId, epoch = 1))

        assertEquals("干跑门开：真实执行面不得触碰", 0, clicks.get())

        // 回执：socketType=3、errorCode=0、metaJson.deviceDryRun=true（审计标记）
        val confirm = transport.sent.drop(1).single()
        val confirmObj = Json.parseToJsonElement(confirm).jsonObject
        assertEquals(3, confirmObj["socketType"]!!.jsonPrimitive.content.toInt())
        val bean = codec.beans(confirmObj["list"]!!).single()
        assertEquals(MessageTypes.SEND_MESSAGE, bean.type)
        assertEquals(0, bean.errorCode)
        val metaJson = Json.parseToJsonElement(bean.metaJson!!).jsonObject
        assertEquals(commandId, metaJson["dispatchId"]!!.jsonPrimitive.content)
        assertEquals(true, metaJson["deviceDryRun"]!!.jsonPrimitive.content.toBoolean())

        // 事件面：干跑留痕（含将要做什么 = 计划步骤）
        assertTrue(events.any { it.contains("端侧干跑") && it.contains("零副作用") })

        // 台账：outcome=device_dry_run（904 补报可见干跑事实）
        loop.sendJournalReplay()
        val replayObj = Json.parseToJsonElement(transport.sent.last()).jsonObject
        val entry = (codec.beans(replayObj["list"]!!).single().extras["entries"]
            as kotlinx.serialization.json.JsonArray).first().jsonObject
        assertEquals("device_dry_run", entry["outcome"]!!.jsonPrimitive.content)
        loop.stop()
    }

    @Test
    fun `端侧干跑-门关时正常执行-门为null同`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        val loop = newLoop(transport, journal, clicks, dryRunGate = { false })
        loop.start()
        transport.serverMessage(helloAckText())
        transport.serverMessage(commandFrame("3c4d5e6f-7a8b-4c9d-0e1f-2a3b4c5d6e7f", epoch = 1))
        assertEquals("干跑门关：正常真实执行", 1, clicks.get())
        val confirm = transport.sent.drop(1).single()
        val bean = codec.beans(Json.parseToJsonElement(confirm).jsonObject["list"]!!).single()
        assertTrue(
            "非干跑回执不得携带 deviceDryRun 标记",
            Json.parseToJsonElement(bean.metaJson!!).jsonObject["deviceDryRun"] == null,
        )
        loop.stop()
    }

    @Test
    fun `端侧干跑-暂停态仍出干跑回执-干跑属安全操作不经暂停门`() {
        val transport = FakeTransport()
        val journal = InMemoryJournalStore()
        val clicks = AtomicInteger()
        // AlwaysPaused 下真实执行被暂停拦截（见⑥）；干跑无副作用 = 安全操作，
        // 语义与 feature:executor CommandRunner.dryRun 一致（不受暂停门拦截）
        val loop = newLoop(
            transport, journal, clicks,
            pause = AlwaysPaused(),
            dryRunGate = { true },
        )
        loop.start()
        transport.serverMessage(helloAckText())
        transport.serverMessage(commandFrame("4d5e6f7a-8b9c-4d0e-1f2a-3b4c5d6e7f8a", epoch = 1))
        assertEquals(0, clicks.get())
        // 暂停 + 干跑门开：须产出干跑 RAW_CONFIRM（errorCode=0 + deviceDryRun 标记）。
        // metaJson 在帧内为 JSON 字符串值（引号转义）——须解码后断言，不做子串匹配。
        val confirmObj = transport.sent
            .map { Json.parseToJsonElement(it).jsonObject }
            .first { it["socketType"]?.jsonPrimitive?.content == "3" }
        val bean = codec.beans(confirmObj["list"]!!).single()
        assertEquals(0, bean.errorCode)
        assertEquals(
            true,
            Json.parseToJsonElement(bean.metaJson!!).jsonObject["deviceDryRun"]!!
                .jsonPrimitive.content.toBoolean(),
        )
        loop.stop()
    }
}
