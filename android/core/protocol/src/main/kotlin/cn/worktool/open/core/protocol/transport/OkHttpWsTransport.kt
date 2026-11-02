/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import java.util.concurrent.TimeUnit

/**
 * OkHttp WS 传输实现（车道唯一网络依赖）。
 *
 * 安全：默认 OkHttpClient（系统证书校验，无 trust-all——SR-01）；
 * `pingIntervalMillis` 用于链路保活探测（v2 失联判定由上层 45s 不活跃窗口关闭）。
 */
class OkHttpWsTransport(
    private val baseUrlClient: OkHttpClient = defaultClient(),
    private val pingIntervalMillis: Long = 0,
) : WsTransport {

    private var webSocket: WebSocket? = null

    override fun connect(url: String, listener: TransportListener) {
        val policy = EndpointPolicy.check(url)
        if (policy is EndpointPolicy.Check.Violated) {
            listener.onFailure(IllegalStateException("端点策略拒绝: ${policy.rule} ${policy.detail}"))
            return
        }
        val client = if (pingIntervalMillis > 0) {
            baseUrlClient.newBuilder()
                .pingInterval(pingIntervalMillis, TimeUnit.MILLISECONDS)
                .build()
        } else {
            baseUrlClient
        }
        val request = Request.Builder().url(url).build()
        webSocket = client.newWebSocket(
            request,
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) = listener.onOpen()

                override fun onMessage(webSocket: WebSocket, text: String) = listener.onMessage(text)

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) =
                    listener.onFailure(t)

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) =
                    listener.onClosed(code, reason)
            },
        )
    }

    override fun send(text: String): Boolean = webSocket?.send(text) ?: false

    override fun close(code: Int, reason: String) {
        webSocket?.close(code, reason)
    }

    class Factory(
        private val client: OkHttpClient = defaultClient(),
        private val pingIntervalMillis: Long = 0,
    ) : WsTransport.Factory {
        override fun create(): WsTransport = OkHttpWsTransport(client, pingIntervalMillis)
    }

    companion object {
        fun defaultClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS) // WS 长连接
            .build()
    }
}
