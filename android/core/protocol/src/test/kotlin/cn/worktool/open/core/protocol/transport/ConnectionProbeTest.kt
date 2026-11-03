/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * U5 连接探针测试（docs/16 U5；纯 JVM，fake 三层编排）。
 * 覆盖：地址解析（wss 默认端口 / 自定义端口 / ws 明文 / 非法输入 / IPv6）/
 * 三层全通 / TCP 失败短路 / TLS 失败短路 / HTTP 非 2xx 标失败层 / 明文端点跳过 TLS。
 */
class ConnectionProbeTest {

    // ---- 地址解析 ----

    @Test
    fun `解析-wss 默认 443-带 TLS`() {
        val p = ConnectionProbe().parse("wss://example.com")
        assertNotNull(p)
        assertEquals(443, p!!.port)
        assertTrue(p.tls)
        assertEquals("https://example.com:443", p.httpBase)
    }

    @Test
    fun `解析-自定义端口与路径剥离`() {
        val p = ConnectionProbe().parse("wss://example.com:8463/api/v2/x?y=1")
        assertNotNull(p)
        assertEquals(8463, p!!.port)
        assertEquals("example.com", p.host)
    }

    @Test
    fun `解析-ws 明文默认 80-无 TLS`() {
        val p = ConnectionProbe().parse("ws://10.0.0.5:9000")
        assertNotNull(p)
        assertEquals(9000, p!!.port)
        assertFalse(p.tls)
        assertEquals("http://10.0.0.5:9000", p.httpBase)
    }

    @Test
    fun `解析-非法输入返回 null`() {
        val probe = ConnectionProbe()
        assertNull(probe.parse("example.com")) // 无 scheme
        assertNull(probe.parse("ftp://example.com")) // 非白名单 scheme
        assertNull(probe.parse("wss://")) // 无 host
        assertNull(probe.parse("wss://host:notaport")) // 端口非法
    }

    @Test
    fun `解析-IPv6 字面量`() {
        val p = ConnectionProbe().parse("wss://[::1]:8463")
        assertNotNull(p)
        assertEquals("[::1]", p!!.host)
        assertEquals(8463, p.port)
    }

    // ---- 三层编排（fake Layers）----

    private class FakeLayers(
        val tcp: (String, Int) -> Long = { _, _ -> 12L },
        val tls: (String, Int) -> String = { h, _ -> "TLS OK · $h" },
        val http: (String) -> Int = { 200 },
    ) : ConnectionProbe.Layers {
        override fun tcpConnect(host: String, port: Int, timeoutMillis: Int) = tcp(host, port)
        override fun tlsHandshake(host: String, port: Int, timeoutMillis: Int) = tls(host, port)
        override fun httpGet(url: String, timeoutMillis: Int) = http(url)
    }

    @Test
    fun `三层全通-allOk 且无失败层`() {
        var healthUrl = ""
        val probe = ConnectionProbe(FakeLayers(http = { healthUrl = it; 200 }))
        val r = probe.probe("wss://example.com")
        assertEquals("https://example.com:443/healthz", healthUrl)
        assertEquals(12L, r.tcpMs)
        assertEquals(true, r.tlsOk)
        assertEquals(200, r.httpCode)
        assertNull(r.failedLayer)
        assertTrue(r.allOk)
    }

    @Test
    fun `TCP 失败-短路至 tcp 层-不触 TLS 与 HTTP`() {
        var tlsCalled = false
        var httpCalled = false
        val probe = ConnectionProbe(
            FakeLayers(
                tcp = { _, _ -> error("connect timed out") },
                tls = { _, _ -> tlsCalled = true; "x" },
                http = { httpCalled = true; 200 },
            ),
        )
        val r = probe.probe("wss://example.com")
        assertEquals("tcp", r.failedLayer)
        assertTrue(r.reason!!.contains("connect timed out"))
        assertNull(r.tlsOk)
        assertNull(r.httpCode)
        assertFalse(tlsCalled)
        assertFalse(httpCalled)
        assertFalse(r.allOk)
    }

    @Test
    fun `TLS 握手失败-短路至 tls 层`() {
        var httpCalled = false
        val probe = ConnectionProbe(
            FakeLayers(
                tls = { _, _ -> error("certificate mismatch") },
                http = { httpCalled = true; 200 },
            ),
        )
        val r = probe.probe("wss://example.com")
        assertEquals("tls", r.failedLayer)
        assertEquals(false, r.tlsOk)
        assertTrue(r.reason!!.contains("certificate mismatch"))
        assertNull(r.httpCode)
        assertFalse(httpCalled)
    }

    @Test
    fun `HTTP healthz 非 2xx-标失败层 http`() {
        val probe = ConnectionProbe(FakeLayers(http = { 503 }))
        val r = probe.probe("wss://example.com")
        assertEquals("http", r.failedLayer)
        assertEquals(503, r.httpCode)
        assertTrue(r.reason!!.contains("503"))
        assertFalse(r.allOk)
    }

    @Test
    fun `明文端点-跳过 TLS-tlsOk 为 null`() {
        val probe = ConnectionProbe(FakeLayers())
        val r = probe.probe("ws://example.com:8080")
        assertNull(r.tlsOk) // 不适用（非 TLS 端点不握手）
        assertEquals(200, r.httpCode)
        assertTrue(r.allOk)
    }

    @Test
    fun `地址不可解析-parse 失败结果`() {
        val probe = ConnectionProbe(FakeLayers())
        val r = probe.probe("not-a-url")
        assertEquals("parse", r.failedLayer)
        assertFalse(r.allOk)
    }
}
