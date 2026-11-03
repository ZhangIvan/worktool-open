/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import okhttp3.OkHttpClient
import okhttp3.Request
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.TimeUnit
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLSession
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory

/**
 * WS 连接探针（docs/16 U5：配对前「测试连接」，纯 Kotlin 可测）。
 *
 * 三层探测（逐层推进、失败即停，对齐 APK mode=probe 的 URL/Code/Reason 结果框语义）：
 * ① TCP connect：裸 socket 连 host:port，测可达性与耗时；
 * ② TLS 握手：SSLSocket 握手 + 主机名校验（证书链问题在 reason 给出分类）；
 *    ——明文端点（ws://）跳过本层（tlsOk=null = 不适用）；
 * ③ HTTP 探活：GET `{httpScheme}://{host}:{port}/healthz`，记录状态码与耗时。
 *
 * 不携带任何凭据（不含配对码/设备令牌）；探测目标 = 用户输入的 server 基址本身
 * （wss/https 皆可）。三层实现全部经构造器注入（[Layers]），JVM 测试用 fake
 * 逐层编排；默认实现 = 真实 socket/OkHttp。
 */
class ConnectionProbe(
    private val layers: Layers = Layers.real(),
    private val timeoutMillis: Int = 5_000,
) {

    /** 三层注入面（JVM 测试 fake 编排用；默认 [Layers.real]）。 */
    interface Layers {
        /** TCP 连接；返回耗时 ms；失败抛异常（异常消息进 reason）。 */
        fun tcpConnect(host: String, port: Int, timeoutMillis: Int): Long

        /** TLS 握手 + 主机名校验；返回证书 CN 摘要；失败抛异常。明文端点不调用。 */
        fun tlsHandshake(host: String, port: Int, timeoutMillis: Int): String

        /** HTTP GET；返回状态码；失败/非 2xx-4xx 异常抛出（超时等）。 */
        fun httpGet(url: String, timeoutMillis: Int): Int

        companion object {
            fun real(client: OkHttpClient = OkHttpClient.Builder()
                .connectTimeout(5, TimeUnit.SECONDS)
                .readTimeout(5, TimeUnit.SECONDS)
                .build()): Layers = object : Layers {
                override fun tcpConnect(host: String, port: Int, timeoutMillis: Int): Long {
                    val t0 = System.currentTimeMillis()
                    Socket().use { s ->
                        s.connect(InetSocketAddress(host, port), timeoutMillis)
                    }
                    return System.currentTimeMillis() - t0
                }

                override fun tlsHandshake(host: String, port: Int, timeoutMillis: Int): String {
                    val factory = SSLSocketFactory.getDefault() as SSLSocketFactory
                    val raw = factory.createSocket()
                    val socket = raw as SSLSocket
                    try {
                        socket.soTimeout = timeoutMillis
                        socket.connect(InetSocketAddress(host, port), timeoutMillis)
                        socket.startHandshake()
                        val session: SSLSession = socket.session
                        // 主机名校验（HttpsURLConnection 默认验证器：不通过即抛异常）
                        if (!HttpsURLConnection.getDefaultHostnameVerifier().verify(host, session)) {
                            error("TLS 主机名校验失败（证书与主机名不匹配）")
                        }
                        val cert = session.peerCertificates.firstOrNull()
                        return "TLS OK · 证书=${cert?.type ?: "?"}"
                    } finally {
                        runCatching { socket.close() }
                    }
                }

                override fun httpGet(url: String, timeoutMillis: Int): Int {
                    val request = Request.Builder().url(url).get().build()
                    client.newBuilder()
                        .callTimeout(timeoutMillis.toLong(), TimeUnit.MILLISECONDS)
                        .build()
                        .newCall(request).execute().use { response ->
                            // 消费极短响应体即关闭（healthz 探活，不读载荷内容）
                            return response.code
                        }
                }
            }
        }
    }

    /** 探测结果（UI 结果对话框逐层渲染 ✓/✗ + 原因）。 */
    data class ProbeResult(
        val url: String,
        /** TCP 层耗时 ms；null = 未到达该层。 */
        val tcpMs: Long?,
        /** TLS 层：true=握手+校验通过 / false=失败 / null=不适用（明文端点）或未到达。 */
        val tlsOk: Boolean?,
        /** HTTP /healthz 状态码；null = 未到达/失败。 */
        val httpCode: Int?,
        /** 首个失败层：tcp / tls / http / parse；null = 全部通过。 */
        val failedLayer: String?,
        /** 失败原因（异常消息）；null = 无。 */
        val reason: String?,
        /** HTTP 层耗时 ms；null = 未到达。 */
        val httpMs: Long? = null,
    ) {
        val allOk: Boolean get() = failedLayer == null && httpCode != null && httpCode in 200..399
    }

    /** 探测地址解析（wss/https → host/port/是否 TLS）。 */
    data class ParsedUrl(val scheme: String, val host: String, val port: Int, val tls: Boolean) {
        val httpBase: String get() = "${if (tls) "https" else "http"}://$host:$port"
    }

    /** 解析用户输入基址；null = 格式不可解析（host 缺失/端口非法）。 */
    fun parse(baseUrl: String): ParsedUrl? {
        val raw = baseUrl.trim()
        val schemeEnd = raw.indexOf("://")
        if (schemeEnd <= 0) return null
        val scheme = raw.substring(0, schemeEnd).lowercase()
        if (scheme != "wss" && scheme != "ws" && scheme != "https" && scheme != "http") return null
        val tls = scheme == "wss" || scheme == "https"
        val authority = raw.substring(schemeEnd + 3).substringBefore('/').substringBefore('?')
        if (authority.isEmpty()) return null
        // IPv6 字面量：[::1]:8463 形态
        val (host, portStr) = if (authority.startsWith("[")) {
            val close = authority.indexOf(']')
            if (close < 0) return null
            authority.substring(0, close + 1) to authority.substring(close + 1).removePrefix(":")
        } else {
            authority.substringBefore(':') to authority.substringAfter(':', "")
        }
        if (host.isEmpty()) return null
        val port = when {
            portStr.isEmpty() -> if (tls) 443 else 80
            else -> portStr.toIntOrNull()?.takeIf { it in 1..65535 } ?: return null
        }
        return ParsedUrl(scheme, host, port, tls)
    }

    /**
     * 执行三层探测（逐层推进、失败即停）。地址不可解析直接返回 parse 失败结果。
     */
    fun probe(baseUrl: String): ProbeResult {
        val parsed = parse(baseUrl)
            ?: return ProbeResult(baseUrl, null, null, null, "parse", "地址不可解析（须 wss://host[:port] 形态）")
        val host = parsed.host.removePrefix("[").removeSuffix("]")
        return try {
            val tcpMs = layers.tcpConnect(host, parsed.port, timeoutMillis)
            try {
                var tlsOk: Boolean? = null
                if (parsed.tls) {
                    layers.tlsHandshake(host, parsed.port, timeoutMillis)
                    tlsOk = true
                }
                try {
                    val t0 = System.currentTimeMillis()
                    val code = layers.httpGet("${parsed.httpBase}/healthz", timeoutMillis)
                    ProbeResult(
                        url = baseUrl,
                        tcpMs = tcpMs,
                        tlsOk = tlsOk,
                        httpCode = code,
                        failedLayer = if (code in 200..399) null else "http",
                        reason = if (code in 200..399) null else "healthz 状态码 $code（非 2xx/3xx）",
                        httpMs = System.currentTimeMillis() - t0,
                    )
                } catch (e: Exception) {
                    ProbeResult(baseUrl, tcpMs, tlsOk, null, "http", "HTTP 探活失败：${e.message}")
                }
            } catch (e: Exception) {
                ProbeResult(baseUrl, tcpMs, false, null, "tls", "TLS 握手/校验失败：${e.message}")
            }
        } catch (e: Exception) {
            ProbeResult(baseUrl, null, null, null, "tcp", "TCP 连接失败：${e.message}")
        }
    }
}
