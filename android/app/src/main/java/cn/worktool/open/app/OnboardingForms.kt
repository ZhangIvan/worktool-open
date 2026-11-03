/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.DeviceCredentialStore
import cn.worktool.open.core.protocol.transport.EndpointPolicy

/**
 * 配对引导流表单校验（产品冲刺 A 车道，纯 JVM 可测）。
 *
 * server 地址口径：`wss://host[:port]`（或 `https://`——REST 配对与 WS 会话共用同一
 * baseUrl，[cn.worktool.open.core.protocol.pair.PairingClient] 直接拼 `/api/v2/pair`）。
 * 明文 `ws://`/`http://` 仅 loopback 原型例外（SR-09）；最终安全裁决复用
 * [EndpointPolicy.check]（core:protocol 车道所有者），本类只做表单层前置校验，
 * 给出面向用户的错误文案。
 */
object OnboardingForms {

    /** server 地址表单校验结果。 */
    sealed interface ServerAddress {
        /** 合法：[normalized] = 去尾部空白/斜杠后的基址。 */
        data class Valid(val normalized: String) : ServerAddress

        /** 非法：[reasonCode] 供 UI 映射文案。 */
        data class Invalid(val reasonCode: String) : ServerAddress
    }

    // reasonCode 稳定枚举（UI 文案映射用，不外发）
    const val REASON_EMPTY = "empty"
    const val REASON_SCHEME = "scheme"
    const val REASON_HOST = "host"
    const val REASON_PORT = "port"
    const val REASON_USERINFO = "userinfo"
    const val REASON_FRAGMENT = "fragment"

    /**
     * 校验并规范化 server 地址输入。
     * 规则：scheme ∈ {wss, https}；host 必填；端口可选（1~65535）；不得带
     * userinfo/fragment；query 拒绝。允许尾部 `/`（去除）。裸 IP / 明文不在本层
     * 拒绝——由 [securityCheck]（EndpointPolicy）给出精确规则名。
     */
    fun validateServerAddress(input: String): ServerAddress {
        val raw = input.trim()
        if (raw.isEmpty()) return ServerAddress.Invalid(REASON_EMPTY)
        val schemeEnd = raw.indexOf("://")
        if (schemeEnd <= 0) return ServerAddress.Invalid(REASON_SCHEME)
        val scheme = raw.substring(0, schemeEnd).lowercase()
        if (scheme != "wss" && scheme != "https") return ServerAddress.Invalid(REASON_SCHEME)
        if (raw.contains('@')) return ServerAddress.Invalid(REASON_USERINFO)
        if (raw.contains('#')) return ServerAddress.Invalid(REASON_FRAGMENT)
        if (raw.contains('?')) return ServerAddress.Invalid(REASON_FRAGMENT)
        val authority = raw.substring(schemeEnd + 3).trimEnd('/')
        val hostPort = authority.substringBefore('/')
        if (hostPort.isEmpty()) return ServerAddress.Invalid(REASON_HOST)
        val host: String
        var port: Int? = null
        if (hostPort.startsWith("[")) { // IPv6 字面量
            val close = hostPort.indexOf(']')
            if (close < 0) return ServerAddress.Invalid(REASON_HOST)
            host = hostPort.substring(0, close + 1)
            val rest = hostPort.substring(close + 1)
            if (rest.startsWith(":")) port = rest.substring(1).toIntOrNull()
            else if (rest.isNotEmpty()) return ServerAddress.Invalid(REASON_HOST)
        } else {
            host = hostPort.substringBefore(':')
            if (host.isEmpty()) return ServerAddress.Invalid(REASON_HOST)
            if (hostPort.contains(':')) port = hostPort.substringAfter(':').toIntOrNull()
        }
        if (port != null && port !in 1..65535) return ServerAddress.Invalid(REASON_PORT)
        return ServerAddress.Valid("$scheme://$authority")
    }

    /** 6 位配对码（spec §5.1：一次性数字码）。 */
    sealed interface PairingCode {
        data class Valid(val code: String) : PairingCode
        data class Invalid(val reasonCode: String) : PairingCode
    }

    fun validatePairingCode(input: String): PairingCode {
        val raw = input.trim()
        if (!raw.matches(Regex("^\\d{6}$"))) return PairingCode.Invalid("format")
        return PairingCode.Valid(raw)
    }

    /**
     * 安全策略终审（EndpointPolicy 委托）：表单合法但被 SR-09 拒（裸 IP / 非 loopback
     * 明文）时返回违规规则名，UI 给出精确提示。产品变体不注册 lab 裸 IP 例外。
     */
    fun securityCheck(normalizedBaseUrl: String): String? =
        when (val check = EndpointPolicy.check(normalizedBaseUrl)) {
            EndpointPolicy.Check.Ok -> null
            is EndpointPolicy.Check.Violated -> check.rule
        }

    /** 启动路由（⑥ 接线）：已配对 = 有效凭据 + 可用 server 地址；否则进引导流。 */
    enum class LaunchRoute { MAIN, ONBOARDING }

    fun launchRoute(store: DeviceCredentialStore, serverAddress: String?): LaunchRoute {
        val creds = store.load() ?: return LaunchRoute.ONBOARDING
        if (creds.deviceTokenHex.isBlank() || creds.deviceId.isBlank()) return LaunchRoute.ONBOARDING
        val addr = serverAddress?.trim().orEmpty()
        if (validateServerAddress(addr) !is ServerAddress.Valid) return LaunchRoute.ONBOARDING
        return LaunchRoute.MAIN
    }
}
