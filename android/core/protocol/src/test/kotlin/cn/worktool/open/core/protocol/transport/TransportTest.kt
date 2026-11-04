/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 退避策略（spec §3：v1 5s→600s / v2 5s→120s 可配）。 */
class BackoffPolicyTest {

    @Test
    fun `v1 退避 5s 起步封顶 600s`() {
        val b = BackoffPolicy.v1()
        assertEquals(5_000L, b.nextDelayMillis())
        assertEquals(10_000L, b.nextDelayMillis())
        repeat(20) { b.nextDelayMillis() }
        assertEquals(600_000L, b.nextDelayMillis()) // 已封顶
        assertEquals(600_000L, b.nextDelayMillis())
    }

    @Test
    fun `v2 退避 5s 起步封顶 120s`() {
        val b = BackoffPolicy.v2()
        assertEquals(5_000L, b.nextDelayMillis())
        assertEquals(10_000L, b.nextDelayMillis())
        assertEquals(20_000L, b.nextDelayMillis())
        assertEquals(40_000L, b.nextDelayMillis())
        assertEquals(80_000L, b.nextDelayMillis())
        assertEquals(120_000L, b.nextDelayMillis())
        assertEquals(120_000L, b.nextDelayMillis())
    }

    @Test
    fun `v2 上限可配`() {
        val b = BackoffPolicy.v2(maxMillis = 30_000)
        assertEquals(5_000L, b.nextDelayMillis())
        assertEquals(10_000L, b.nextDelayMillis())
        assertEquals(20_000L, b.nextDelayMillis())
        assertEquals(30_000L, b.nextDelayMillis())
    }

    @Test
    fun `成功连接后复位`() {
        val b = BackoffPolicy.v2()
        repeat(5) { b.nextDelayMillis() }
        b.reset()
        assertEquals(5_000L, b.nextDelayMillis())
    }

    @Test
    fun `抖动注入`() {
        val b = BackoffPolicy(initialMillis = 1_000, maxMillis = 10_000, jitter = { it / 2 })
        assertEquals(500L, b.nextDelayMillis())
    }
}

/** 端点安全策略（SR-01/04/09，G9）。 */
class EndpointPolicyTest {

    @Test
    fun `TLS 端点放行`() {
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("wss://example.com/api"))
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("https://example.com/api/v2/pair"))
    }

    @Test
    fun `明文仅限 loopback 原型例外`() {
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("ws://localhost:8080/webserver/wework/r1"))
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("ws://127.0.0.1:8080/x"))
        val violated = EndpointPolicy.check("ws://example.com:8080/x")
        assertTrue((violated as EndpointPolicy.Check.Violated).rule == "plain_text_not_loopback")
        assertEquals(EndpointPolicy.Check.Violated::class.java, EndpointPolicy.check("http://example.com/x").javaClass)
    }

    @Test
    fun `loopback 明文例外可整体关闭`() {
        val violated = EndpointPolicy.check("ws://127.0.0.1:8080/x", allowInsecureLoopback = false)
        assertTrue((violated as EndpointPolicy.Check.Violated).rule == "insecure_disabled")
    }

    @Test
    fun `裸 IP 主机默认拒绝-显式可开`() {
        val violated = EndpointPolicy.check("wss://192.0.2.53/api")
        assertTrue((violated as EndpointPolicy.Check.Violated).rule == "bare_ip_host")
        assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("wss://192.0.2.53/api", allowRawIpHost = true))
    }

    @Test
    fun `畸形 URL 与 scheme 拒绝`() {
        assertEquals(EndpointPolicy.Check.Violated::class.java, EndpointPolicy.check("not a url").javaClass)
        assertEquals(EndpointPolicy.Check.Violated::class.java, EndpointPolicy.check("ftp://example.com").javaClass)
        assertEquals(EndpointPolicy.Check.Violated::class.java, EndpointPolicy.check("wss:///nohost").javaClass)
    }

    // —— lab 变体例外（W-M3R 真机装配）：产品零行为差异 + fail-closed 语义 ——

    @Test
    fun `lab 裸 IP 例外-默认空注册表零行为差异`() {
        EndpointPolicy.allowLabRawIpHosts(emptySet()) // 复位（测试隔离）
        val violated = EndpointPolicy.check("https://192.0.2.53:8463/api/v2/pair")
        assertTrue(violated is EndpointPolicy.Check.Violated && violated.rule == "bare_ip_host")
    }

    @Test
    fun `lab 裸 IP 例外-注册后仅放行 TLS 且仅该主机`() {
        EndpointPolicy.allowLabRawIpHosts(setOf("192.0.2.53"))
        try {
            assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("https://192.0.2.53:8463/api/v2/pair"))
            assertEquals(EndpointPolicy.Check.Ok, EndpointPolicy.check("wss://192.0.2.53:8463/api/v2/device-sessions/s1"))
            assertEquals(setOf("192.0.2.53"), EndpointPolicy.labRawIpHostsSnapshot())
            // 未注册的其他裸 IP 依旧拒绝（fail-closed）
            val other = EndpointPolicy.check("https://192.0.2.54:8463/api/v2/pair")
            assertTrue(other is EndpointPolicy.Check.Violated && other.rule == "bare_ip_host")
            // 明文不可经 lab 例外豁免（SR-09 前置分支）
            val plain = EndpointPolicy.check("ws://192.0.2.53:8463/x")
            assertTrue(plain is EndpointPolicy.Check.Violated && plain.rule == "plain_text_not_loopback")
        } finally {
            EndpointPolicy.allowLabRawIpHosts(emptySet())
        }
    }
}

/** 手动推进的虚拟调度器。 */
class ManualScheduler : Scheduler {
    private val queue = java.util.PriorityQueue<Pair<Long, Int>>(compareBy { it.first })
    private val tasks = HashMap<Int, () -> Unit>()
    private val cancelled = HashSet<Int>()
    private var seq = 0
    var nowVirtual = 0L

    override fun schedule(delayMillis: Long, task: () -> Unit): Cancellable {
        val id = ++seq
        tasks[id] = task
        queue.add(nowVirtual + delayMillis to id)
        return object : Cancellable {
            override fun cancel() {
                cancelled.add(id)
            }
        }
    }

    /** 推进虚拟时钟，执行到期任务。 */
    fun advance(millis: Long) {
        val target = nowVirtual + millis
        while (true) {
            val head = queue.peek() ?: break
            if (head.first > target) break
            queue.poll()
            nowVirtual = head.first
            val task = tasks.remove(head.second)
            if (task != null && head.second !in cancelled) task()
        }
        nowVirtual = target
    }
}

/** 可编排的 fake WS 传输。 */
class FakeTransport : WsTransport {
    var listener: TransportListener? = null
    var open = false
    val sent = mutableListOf<String>()
    var connectFailuresBeforeOpen = 0

    override fun connect(url: String, listener: TransportListener) {
        this.listener = listener
        if (connectFailuresBeforeOpen > 0) {
            connectFailuresBeforeOpen--
            listener.onFailure(IllegalStateException("connect refused"))
        } else {
            open = true
            listener.onOpen()
        }
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

    class Factory(private val transport: FakeTransport) : WsTransport.Factory {
        override fun create(): WsTransport = transport
    }
}

/** ReconnectingWsClient 状态机测试（v1/v2 两种配置）。 */
class ReconnectingWsClientTest {

    private class Recording {
        val events = mutableListOf<String>()
    }

    @Test
    fun `v1 形态-onOpen 发 td-心跳 5s-断线指数退避重连`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val rec = Recording()
        val clockNow = java.util.concurrent.atomic.AtomicLong(1_000L)
        var url = "ws://localhost:8080/webserver/wework/robot-1"

        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { url },
            greeting = { """{"td":${clockNow.get()}}""" },
            onServerMessage = { rec.events.add("msg:$it") },
            heartbeatFrame = { """{"type":11}""" },
            heartbeatIntervalMillis = 5_000,
            inactivityTimeoutMillis = null,
            backoff = BackoffPolicy.v1(),
            scheduler = scheduler,
            clock = { clockNow.get() },
            onEvent = { rec.events.add(it.javaClass.simpleName) },
        )
        client.start()

        // onOpen → 立即发 td
        assertEquals("""{"td":1000}""", transport.sent.first())

        // 心跳 5s 周期链
        scheduler.advance(5_000)
        assertEquals("""{"type":11}""", transport.sent[1])
        scheduler.advance(5_000)
        assertEquals("""{"type":11}""", transport.sent[2])

        // 服务端消息
        transport.serverMessage("""{"socketType":0}""")
        assertTrue(rec.events.any { it == "msg:{\"socketType\":0}" })

        // 断线 → 5s 后重连（退避第 1 档）
        transport.close(1006, "peer gone")
        assertEquals("Disconnected", rec.events.last { it.startsWith("Disconnect") })
        scheduler.advance(5_000)
        assertTrue(transport.open) // 已重连
        assertEquals("""{"td":1000}""", transport.sent.last()) // 新连接首帧仍是 td

        client.stop()
    }

    @Test
    fun `连接失败-指数退避后成功`() {
        val transport = FakeTransport()
        transport.connectFailuresBeforeOpen = 2 // 两次失败，第三次成功
        val scheduler = ManualScheduler()
        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { "ws://localhost:8080/x" },
            greeting = null,
            onServerMessage = { },
            heartbeatFrame = null,
            heartbeatIntervalMillis = 15_000,
            backoff = BackoffPolicy(initialMillis = 1_000, maxMillis = 8_000),
            scheduler = scheduler,
            clock = { 0L },
        )
        client.start()
        assertTrue(!transport.open)
        scheduler.advance(1_000) // 第一次重试失败
        scheduler.advance(2_000) // 第二次重试成功
        assertTrue(transport.open)
        client.stop()
    }

    // H2（2026-11-04 性能审计）：心跳发送成功=链路活性证据（TCP 写成功=内核级
    // 存活；死链由写失败路径与服务端读 deadline 关闭双路径检测）。服务端对心跳
    // 无回包（spec 语义）不再触发看门狗——消灭每分钟重连循环。
    @Test
    fun `v2 形态-心跳持续成功则看门狗不断连（发送即活性）`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val events = mutableListOf<String>()
        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { "wss://example.com/api/v2/device-sessions/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d" },
            greeting = { """{"type":"hello"}""" },
            onServerMessage = { },
            heartbeatFrame = { "hb" },
            heartbeatIntervalMillis = 15_000,
            inactivityTimeoutMillis = 45_000,
            backoff = BackoffPolicy(initialMillis = 5_000, maxMillis = 120_000),
            scheduler = scheduler,
            clock = { scheduler.nowVirtual },
            onEvent = { events.add(it.javaClass.simpleName) },
        )
        client.start()
        assertEquals("""{"type":"hello"}""", transport.sent.first())
        scheduler.advance(40_000)
        transport.serverMessage("pong") // 服务端消息同样刷新活性
        scheduler.advance(120_000) // 4 分钟无服务端帧——心跳一直成功，不得断连
        assertTrue("心跳成功=链路活性，不得断连", transport.open)
        assertTrue(events.none { it.startsWith("InactivityTimeout") })
        client.stop()
    }

    @Test
    fun `v2 形态-无心跳帧时看门狗仍按不活跃关闭（覆盖看门狗本体）`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val events = mutableListOf<String>()
        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { "wss://example.com/api/v2/device-sessions/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d" },
            greeting = { """{"type":"hello"}""" },
            onServerMessage = { },
            heartbeatFrame = null, // 无心跳帧 → lastSendOk 不刷新 → 看门狗按旧语义触发
            heartbeatIntervalMillis = 15_000,
            inactivityTimeoutMillis = 45_000,
            backoff = BackoffPolicy(initialMillis = 5_000, maxMillis = 120_000),
            scheduler = scheduler,
            clock = { scheduler.nowVirtual },
            onEvent = { events.add(it.javaClass.simpleName) },
        )
        client.start()
        // 15s 链式检查 + idle>45 严格大于 → 触发发生在 60s 拍
        scheduler.advance(65_000)
        assertTrue("无任何活性证据时必须不活跃断连", events.any { it.startsWith("InactivityTimeout") })
        assertTrue("必须调度重连", events.any { it.startsWith("ReconnectScheduled") })
        client.stop()
    }

    @Test
    fun `心跳发送失败即重连-v1 语义`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { "ws://localhost:8080/x" },
            greeting = null,
            onServerMessage = { },
            heartbeatFrame = { "hb" },
            heartbeatIntervalMillis = 5_000,
            backoff = BackoffPolicy(initialMillis = 1_000, maxMillis = 60_000),
            scheduler = scheduler,
            clock = { 0L },
        )
        client.start()
        transport.open = false // 模拟发送通道失效（未回调 onClose）
        scheduler.advance(5_000)
        assertTrue(!transport.open)
        scheduler.advance(1_000)
        assertTrue(transport.open) // 心跳失败 → 退避重连
        client.stop()
    }

    @Test
    fun `stop 后不再重连`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val client = ReconnectingWsClient(
            transportFactory = FakeTransport.Factory(transport),
            urlProvider = { "ws://localhost:8080/x" },
            greeting = null,
            onServerMessage = { },
            heartbeatFrame = null,
            heartbeatIntervalMillis = 5_000,
            backoff = BackoffPolicy(initialMillis = 1_000, maxMillis = 60_000),
            scheduler = scheduler,
            clock = { 0L },
        )
        client.start()
        transport.close(1006, "x")
        client.stop()
        scheduler.advance(60_000)
        assertTrue(!transport.open)
    }
}
