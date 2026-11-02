/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

import java.net.URI

/**
 * 端点安全策略（SR-01/04/09，门禁 G9）：
 * - 默认拒绝明文（ws:// / http://）——仅 loopback 原型例外（docs/02 §6.1）；
 * - 默认拒绝裸 IP 主机（上游教训：明文+裸 IP；自签部署需显式开启 [allowRawIpHost]）；
 * - 无 trust-all 证书逻辑（OkHttp 默认系统校验，SR-01）。
 */
object EndpointPolicy {

    sealed interface Check {
        object Ok : Check
        data class Violated(val rule: String, val detail: String) : Check
    }

    fun check(
        rawUrl: String,
        allowInsecureLoopback: Boolean = true,
        allowRawIpHost: Boolean = false,
    ): Check {
        val uri = try {
            URI(rawUrl)
        } catch (_: Exception) {
            return Check.Violated("malformed_url", rawUrl)
        }
        val scheme = uri.scheme?.lowercase()
            ?: return Check.Violated("missing_scheme", rawUrl)
        if (scheme !in setOf("ws", "wss", "http", "https")) {
            return Check.Violated("scheme_not_allowed", scheme)
        }
        val host = uri.host
            ?: return Check.Violated("missing_host", rawUrl)
        val isLoopback = isLoopback(host)
        val isPlain = scheme == "ws" || scheme == "http"
        if (isPlain && !isLoopback) {
            return Check.Violated(
                "plain_text_not_loopback",
                "$scheme://$host —— SR-09：默认禁明文（仅 loopback 原型例外）",
            )
        }
        if (isPlain && !allowInsecureLoopback) {
            return Check.Violated("insecure_disabled", "$scheme://$host")
        }
        if (isRawIpHost(host) && !isLoopback && !allowRawIpHost) {
            return Check.Violated(
                "bare_ip_host",
                "$host —— SR-09：默认禁裸 IP（显式配置可开启）",
            )
        }
        return Check.Ok
    }

    private fun isLoopback(host: String): Boolean {
        val h = host.lowercase()
        if (h == "localhost" || h == "::1" || h == "[::1]") return true
        if (h == "127.0.0.1") return true
        // 127.0.0.0/8 全段 loopback
        if (h.matches(Regex("^127\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}$"))) return true
        return false
    }

    private fun isRawIpHost(host: String): Boolean =
        host.matches(Regex("^[0-9.]+$")) || host.startsWith("[") || host.contains(':')
}
