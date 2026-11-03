/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * 重连计数透出测试（docs/16 优化-采纳 #14「离线态重连中(第N次)」文案数据源；
 * 纯 JVM，复用 TransportTest 的 ManualScheduler/FakeTransport）。
 */
class ReconnectCounterTest {

    private fun newClient(
        transport: FakeTransport,
        scheduler: ManualScheduler,
    ) = ReconnectingWsClient(
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

    @Test
    fun `初始与已连接-计数为 0`() {
        val client = newClient(FakeTransport(), ManualScheduler())
        client.start()
        assertEquals(0, client.reconnectAttempts())
        client.stop()
    }

    @Test
    fun `断连后计数递增-第 N 次重连`() {
        val transport = FakeTransport()
        val scheduler = ManualScheduler()
        val client = newClient(transport, scheduler)
        client.start()

        transport.close(1006, "peer gone")
        assertEquals("第一次断连 → 第 1 次重连", 1, client.reconnectAttempts())

        scheduler.advance(1_000) // 重连成功
        assertEquals("onOpen 成功 → 计数清零", 0, client.reconnectAttempts())
        client.stop()
    }

    @Test
    fun `连续失败-计数持续递增直到成功`() {
        val transport = FakeTransport()
        transport.connectFailuresBeforeOpen = 3
        val scheduler = ManualScheduler()
        val client = newClient(transport, scheduler)
        client.start()
        assertEquals(1, client.reconnectAttempts()) // 首连失败

        scheduler.advance(1_000) // 第 2 次尝试失败
        assertEquals(2, client.reconnectAttempts())
        scheduler.advance(2_000) // 第 3 次尝试失败
        assertEquals(3, client.reconnectAttempts())
        scheduler.advance(4_000) // 第 4 次成功
        assertEquals("成功后清零", 0, client.reconnectAttempts())
        client.stop()
    }

    @Test
    fun `stop 后计数保持快照（不伪造清零）`() {
        val transport = FakeTransport()
        transport.connectFailuresBeforeOpen = 99
        val scheduler = ManualScheduler()
        val client = newClient(transport, scheduler)
        client.start()
        assertEquals(1, client.reconnectAttempts())
        client.stop()
        assertEquals("stop 不清零（离线快照如实）", 1, client.reconnectAttempts())
    }
}
