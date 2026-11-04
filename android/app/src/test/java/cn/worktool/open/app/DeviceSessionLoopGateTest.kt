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
import cn.worktool.open.feature.executor.runtime.PauseManager
import cn.worktool.open.feature.executor.runtime.UiDriver
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.atomic.AtomicInteger

/**
 * U3/U4/采纳#12 执行回路新门测试（docs/16；JVM 级联——协议帧真实走
 * FrameVerifier/InboundCommandGate/CommandRunner 全链，传输 Fake）。
 *
 * - U3：暂停拦截指令经 onDeferred 上抛（SessionRuntime → PauseManager.enqueue 接线源）；
 * - U4：dispatch 帧 meta.ts 超 TTL → SKIPPED(过期) 回执（errorCode=101007 +
 *   metaJson.skipped=expired）不下发执行器；重复投递幂等回执；
 * - 采纳 #12：retryCountProvider 注入失败重试（0=首错即停；N 次重试后成功）。
 */
class DeviceSessionLoopGateTest {

    private val tokenHex = "5dbf5c6a4465c44060e763c9cff7711d7eae18c2ef75b96cab1790ef092d08a4"
    private val token = Hex.decode(tokenHex)
    private var now = 1_793_664_000_000L
    private val codec = EnvelopeCodec()

    // ---- 可编排执行器（203）：失败 N 次后成功 / 恒成功 / 计数 ----

    private class FlakyExecutor(
        val clicks: AtomicInteger,
        val failFirst: Int,
        /** 风险级可配（L0-only 重试护栏反例用；默认 L1=写指令形态） */
        private val risk: cn.worktool.open.feature.executor.risk.RiskLevel = cn.worktool.open.feature.executor.risk.RiskLevel.L1,
    ) : CommandExecutor {
        override val commandType: Int get() = MessageTypes.SEND_MESSAGE
        override fun riskLevel(intent: CommandIntent) = risk
        override fun buildPlan(intent: CommandIntent): ExecutionPlan = ExecutionPlan(
            commandType = commandType,
            riskLevel = risk,
            target = (intent as CommandIntent.SendText).targetRoom,
            paramsSummary = "flaky",
            steps = listOf(
                PlanStep("flaky-step", "执行") { _ ->
                    val n = clicks.incrementAndGet()
                    if (n <= failFirst) StepOutcome.failure(201001, "planned-fail-$n") else StepOutcome.success()
                },
            ),
        )
    }

    private class AlwaysPausedPauseManager : PauseManager {
        override fun isPaused(): Boolean = true
        override fun pauseSource(): String? = "runtime-window"
    }

    private class StubDriver : UiDriver {
        override fun currentRoot(): cn.worktool.open.core.accessibility.tree.UiNode? = null
        override fun click(node: cn.worktool.open.core.accessibility.tree.UiNode) = true
        override fun inputText(node: cn.worktool.open.core.accessibility.tree.UiNode, text: String) = true
        override fun scroll(node: cn.worktool.open.core.accessibility.tree.UiNode, direction: cn.worktool.open.core.accessibility.tree.ScrollDirection) = true
        override fun goBack() = true
        override fun sleep(ms: Long) = Unit
        override fun nowMillis(): Long = 0L
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

    /** 服务端侧帧构造器（与设备同 token；ts 由 clock 注入——TTL 场景给旧时间）。 */
    private fun serverBuilder(ts: Long) = FrameBuilder(
        codec = codec,
        deviceToken = { token },
        tokenVersion = { 1 },
        crypto = null,
        clock = { ts },
        sequencer = SendSequencer().apply { next() },
    )

    private fun helloAckText(builder: FrameBuilder): String {
        val digest = FrameAuth.payloadDigestOfList(null)
        val eventId = java.util.UUID.randomUUID().toString()
        val nonce = "7e3b98e90a47796c8e86633fddac8d8f"
        val ts = now
        val hmac = FrameAuth.hmacHex(token, "", eventId, 1, ts, nonce, digest)
        return Json.encodeToString(
            kotlinx.serialization.json.JsonElement.serializer(),
            kotlinx.serialization.json.buildJsonObject {
                put("type", "hello_ack")
                put("serverTime", ts)
                put("resumeFromSeq", 1)
                put("heartbeatIntervalSeconds", 15)
                put("sessionTimeoutSeconds", 45)
                put("meta", codec.metaElement(FrameMeta(2, ts, nonce, hmac, 1, eventId, "", 1)))
            },
        )
    }

    private fun commandFrame(builder: FrameBuilder, commandId: String, payloadTs: Long? = null): String = builder.build(
        socketType = MessageTypes.SOCKET_MESSAGE_LIST,
        commandId = commandId,
        apiSend = 1,
        list = listOf(
            WeworkMessageBean(
                type = MessageTypes.SEND_MESSAGE,
                roomType = 1,
                extras = buildMap {
                    put("targetRoom", JsonPrimitive("e2e群"))
                    put("receivedContent", JsonPrimitive("x"))
                    put("epoch", JsonPrimitive(1))
                    // U4：载荷 ts = 指令原创建时间（server flushQueued 注入点；帧 meta.ts
                    // 恒新鲜——FrameVerifier ±30s 冻结规则，TTL 判定源不可用帧时间戳）
                    payloadTs?.let { put("ts", JsonPrimitive(it)) }
                },
            ),
        ),
    ).text

    /**
     * 服务端回执帧（socketType=3，re-write 形态：同 commandId + 结果载荷、无 epoch、
     * 新 eventId——gateway rememberReceipt/writeReceipt 的线缆形状）。
     */
    private fun receiptFrame(builder: FrameBuilder, commandId: String): String = builder.build(
        socketType = MessageTypes.SOCKET_RAW_CONFIRM,
        commandId = commandId,
        apiSend = 1,
        list = listOf(
            WeworkMessageBean(
                type = MessageTypes.SEND_MESSAGE,
                errorCode = 0,
                metaJson = """{"dispatchId":"$commandId"}""",
            ),
        ),
    ).text

    private fun newLoop(
        transport: FakeTransport,
        journal: InMemoryJournalStore,
        registry: ExecutorRegistry,
        pause: PauseManager,
        events: MutableList<String>,
        onDeferred: ((String, Int, Long) -> Unit)? = null,
        ttlMillis: (() -> Long?)? = null,
        retryCount: (() -> Int)? = null,
    ): DeviceSessionLoop {
        val credentials = DeviceCredentials(
            deviceId = "device-gate",
            deviceTokenHex = tokenHex,
            tokenVersion = 1,
            wsPath = "/api/v2/device-sessions/gate",
        )
        return DeviceSessionLoop(
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
            onDeferred = onDeferred,
            expiredTtlMillis = ttlMillis,
            retryCountProvider = retryCount,
        )
    }

    // ---- U4：TTL 过期 → SKIPPED 回执，不下发执行器 ----

    @Test
    fun `U4-过期指令-SKIPPED 回执不下发执行器`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val events = mutableListOf<String>()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = events,
            ttlMillis = { 2 * 3_600_000L },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))

        // 帧签名新鲜（过 FrameVerifier ±30s），载荷 ts = 指令原创建时间（3 小时前，
        // 服务端积压 3h 后 flushQueued 重建帧下发的形态）→ 超 2h TTL → 跳过
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000001", payloadTs = now - 3 * 3_600_000L))

        assertEquals("过期指令不得执行", 0, clicks.get())
        val confirm = transport.sent.last { it.contains("\"socketType\":3") }
        val obj = Json.parseToJsonElement(confirm).jsonObject
        val bean = codec.beans(obj["list"]!!).single()
        assertEquals(101007, bean.errorCode)
        assertEquals("指令已过期跳过执行", bean.errorReason)
        assertEquals(
            "expired",
            Json.parseToJsonElement(bean.metaJson!!).jsonObject["skipped"]!!.jsonPrimitive.content,
        )
        assertTrue(events.any { it.contains("指令已过期跳过执行") })
        loop.stop()
    }

    @Test
    fun `U4-新鲜指令-TTL 内照常执行`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
            ttlMillis = { 2 * 3_600_000L },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000002", payloadTs = now - 60_000L))
        assertEquals(1, clicks.get())
        loop.stop()
    }

    @Test
    fun `U4-载荷无 ts-退化为帧时间戳（新鲜恒执行-如实登记的边界）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val events = mutableListOf<String>()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = events,
            ttlMillis = { 2 * 3_600_000L },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        // 无载荷 ts：receivedAt 退化 frame.meta.ts（±30s 验证窗内恒新鲜 → 门不触发）
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000003"))
        assertEquals(1, clicks.get())
        loop.stop()
    }

    @Test
    fun `U4-TTL 未启用-过期指令照常执行（默认行为不变）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
            ttlMillis = null,
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000004", payloadTs = now - 9 * 3_600_000L))
        assertEquals(1, clicks.get())
        loop.stop()
    }

    @Test
    fun `U4-重复投递过期指令-幂等回执不复活`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
            ttlMillis = { 2 * 3_600_000L },
        )
        loop.start()
        // 同一 builder 承载两帧（seq 2→3 严格递增，冻结入站规则 §5.2.4）
        val builder = serverBuilder(now)
        transport.serverMessage(helloAckText(builder))
        transport.serverMessage(commandFrame(builder, "11111111-aaaa-4bbb-8ccc-000000000005", payloadTs = now - 3 * 3_600_000L))
        val sentOnce = transport.sent.count { it.contains("\"socketType\":3") }
        // 服务端重投同 commandId（journal 去重）→ 幂等回执，仍不执行
        transport.serverMessage(commandFrame(builder, "11111111-aaaa-4bbb-8ccc-000000000005", payloadTs = now - 3 * 3_600_000L))
        assertEquals(0, clicks.get())
        assertEquals("幂等回执（重投一帧）", sentOnce + 1, transport.sent.count { it.contains("\"socketType\":3") })
        loop.stop()
    }

    // ---- U3：暂停拦截 → onDeferred 上抛 ----

    @Test
    fun `U3-暂停拦截指令-onDeferred 上抛（待执行队列数据源）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val deferred = mutableListOf<Triple<String, Int, Long>>()
        val events = mutableListOf<String>()
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = AlwaysPausedPauseManager(),
            events = events,
            onDeferred = { id, type, at -> deferred.add(Triple(id, type, at)) },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000006", payloadTs = now - 1_000L))

        assertEquals("暂停期不得执行", 0, clicks.get())
        assertEquals(listOf("11111111-aaaa-4bbb-8ccc-000000000006"), deferred.map { it.first })
        assertEquals(MessageTypes.SEND_MESSAGE, deferred.single().second)
        assertTrue(events.any { it.contains("已入待执行队列") })
        loop.stop()
    }

    @Test
    fun `U3-未注入 onDeferred-暂停拦截静默（历史行为兼容）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = AlwaysPausedPauseManager(),
            events = mutableListOf(),
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000007"))
        assertEquals(0, clicks.get()) // 不崩、不执行
        loop.stop()
    }

    // ---- 采纳 #12：失败重试可配 ----

    @Test
    fun `重试-默认0-首错即停（历史语义保持）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 99, risk = cn.worktool.open.feature.executor.risk.RiskLevel.L0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
            retryCount = { 0 },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000008"))
        assertEquals("0 重试 = 只执行 1 次", 1, clicks.get())
        loop.stop()
    }

    @Test
    fun `重试护栏-L1写指令失败不自动重试（第三方二-2反例）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 2)) // 默认 L1
        val events = mutableListOf<String>()
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = events,
            retryCount = { 5 }, // 即便配置 5 次重试，L1 也不重跑（防重复副作用）
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-00000000000a"))
        assertEquals("L1 失败 = 只执行 1 次（不重试）", 1, clicks.get())
        loop.stop()
    }

    @Test
    fun `重试-2次-第3次尝试成功`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 2, risk = cn.worktool.open.feature.executor.risk.RiskLevel.L0))
        val events = mutableListOf<String>()
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = events,
            retryCount = { 2 },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-000000000009"))
        assertEquals("2 次重试 = 共 3 次尝试", 3, clicks.get())
        assertTrue(events.any { it.contains("1/2 次重试") })
        // 终态回执成功（最后尝试成功）
        val confirm = transport.sent.last { it.contains("\"socketType\":3") }
        val bean = codec.beans(Json.parseToJsonElement(confirm).jsonObject["list"]!!).single()
        assertEquals(0, bean.errorCode)
        loop.stop()
    }

    @Test
    fun `重试-耗尽仍失败-终态失败回执`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 99, risk = cn.worktool.open.feature.executor.risk.RiskLevel.L0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
            retryCount = { 1 },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-00000000000a"))
        assertEquals(2, clicks.get()) // 1 次初始 + 1 次重试
        val confirm = transport.sent.last { it.contains("\"socketType\":3") }
        val bean = codec.beans(Json.parseToJsonElement(confirm).jsonObject["list"]!!).single()
        assertEquals(201001, bean.errorCode)
        loop.stop()
    }

    @Test
    fun `重试-暂停期指令不重试（PAUSED 优先）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = AlwaysPausedPauseManager(),
            events = mutableListOf(),
            retryCount = { 5 },
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        transport.serverMessage(commandFrame(serverBuilder(now), "11111111-aaaa-4bbb-8ccc-00000000000b"))
        assertEquals("暂停拦截非 FAILED，不进重试循环", 0, clicks.get())
        loop.stop()
    }

    // ---- 会话 21 回归：服务端回执帧（socketType=3）不入门禁（拒旧误伤/重 ack 乒乓） ----

    @Test
    fun `回执帧-不入门禁不执行不重发回执-消拒旧误伤`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val events = mutableListOf<String>()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = events,
        )
        loop.start()
        val builder = serverBuilder(now)
        transport.serverMessage(helloAckText(builder))
        // 指令照常执行（载荷带 epoch=1002，水位推进至 1002）
        transport.serverMessage(commandFrame(builder, "11111111-aaaa-4bbb-8ccc-00000000000c"))
        assertEquals(1, clicks.get())
        val confirmsBefore = transport.sent.count { it.contains("\"socketType\":3") }
        // 服务端回执重写帧（同 commandId、无 epoch、新 eventId、seq 接续）——历史实现
        // 入门禁：epoch=0 < 水位 →「拒旧」误伤日志；若改为去重命中又触发重 ack（与
        // 服务端回执重写互为因果 = 乒乓循环）。正确语义：回执帧单向信息，静默消费。
        transport.serverMessage(receiptFrame(builder, "11111111-aaaa-4bbb-8ccc-00000000000c"))
        assertEquals("回执帧不得触发执行", 1, clicks.get())
        assertEquals("回执帧不得重发结果回执（防乒乓）", confirmsBefore, transport.sent.count { it.contains("\"socketType\":3") })
        assertTrue("回执帧不得产生拒旧误伤日志", events.none { it.contains("拒旧") })
        loop.stop()
    }

    // ---- 会话 21 回归：出站 RAW_CONFIRM 载荷回填会话 epoch（spec §5.7.2 对偶面） ----

    @Test
    fun `RAW_CONFIRM-载荷回填会话 epoch-取已应用水位`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        // 指令载荷 epoch=1002 → 门禁接受后水位=1002 → 回执载荷须回填该会话 epoch
        val builder = serverBuilder(now)
        transport.serverMessage(
            builder.build(
                socketType = MessageTypes.SOCKET_MESSAGE_LIST,
                commandId = "11111111-aaaa-4bbb-8ccc-00000000000d",
                apiSend = 1,
                list = listOf(
                    WeworkMessageBean(
                        type = MessageTypes.SEND_MESSAGE,
                        roomType = 1,
                        extras = buildMap {
                            put("targetRoom", JsonPrimitive("e2e群"))
                            put("receivedContent", JsonPrimitive("x"))
                            put("epoch", JsonPrimitive(1002))
                        },
                    ),
                ),
            ).text,
        )
        assertEquals(1, clicks.get())
        val confirm = transport.sent.last { it.contains("\"socketType\":3") }
        val bean = codec.beans(Json.parseToJsonElement(confirm).jsonObject["list"]!!).single()
        assertEquals(
            "回执载荷须携带会话 epoch（服务端对账/审计证据面）",
            "1002",
            (bean.extras["epoch"] as? JsonPrimitive)?.content,
        )
        loop.stop()
    }

    @Test
    fun `RAW_CONFIRM-无已应用 epoch-回填 0（904 epoch≥0 同语义）`() {
        val transport = FakeTransport()
        val clicks = AtomicInteger()
        val registry = ExecutorRegistry.of(FlakyExecutor(clicks, failFirst = 0))
        val loop = newLoop(
            transport, InMemoryJournalStore(), registry,
            pause = cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager(),
            events = mutableListOf(),
        )
        loop.start()
        transport.serverMessage(helloAckText(serverBuilder(now)))
        // 缺 epoch 指令（=0，不拒旧仅去重）→ 执行 → 回执 epoch=0（无已应用水位）
        transport.serverMessage(
            serverBuilder(now).build(
                socketType = MessageTypes.SOCKET_MESSAGE_LIST,
                commandId = "11111111-aaaa-4bbb-8ccc-00000000000e",
                apiSend = 1,
                list = listOf(
                    WeworkMessageBean(
                        type = MessageTypes.SEND_MESSAGE,
                        roomType = 1,
                        extras = buildMap {
                            put("targetRoom", JsonPrimitive("e2e群"))
                            put("receivedContent", JsonPrimitive("x"))
                        },
                    ),
                ),
            ).text,
        )
        assertEquals(1, clicks.get())
        val confirm = transport.sent.last { it.contains("\"socketType\":3") }
        val bean = codec.beans(Json.parseToJsonElement(confirm).jsonObject["list"]!!).single()
        assertEquals("0", (bean.extras["epoch"] as? JsonPrimitive)?.content)
        loop.stop()
    }
}
