/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.InMemoryDeviceCredentialStore
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** 配对引导流表单校验 + 启动路由（产品冲刺 A 车道）。 */
class OnboardingFormsTest {

    // ---- server 地址（wss://host:port 口径）----

    @Test
    fun `server 地址 - wss带端口合法且规范化去尾斜杠`() {
        val r = OnboardingForms.validateServerAddress("wss://robot.example.com:8443/")
        assertTrue(r is OnboardingForms.ServerAddress.Valid)
        assertEquals("wss://robot.example.com:8443", (r as OnboardingForms.ServerAddress.Valid).normalized)
    }

    @Test
    fun `server 地址 - https无端口合法`() {
        val r = OnboardingForms.validateServerAddress("https://robot.example.com")
        assertTrue(r is OnboardingForms.ServerAddress.Valid)
    }

    @Test
    fun `server 地址 - 无scheme拒绝`() {
        assertEquals(
            OnboardingForms.REASON_SCHEME,
            (OnboardingForms.validateServerAddress("robot.example.com") as OnboardingForms.ServerAddress.Invalid).reasonCode,
        )
    }

    @Test
    fun `server 地址 - 明文ws产品口拒绝（SR-09表单层）`() {
        // ws:// 只在 loopback 原型例外——产品表单层 scheme 白名单只收 wss/https
        assertEquals(
            OnboardingForms.REASON_SCHEME,
            (OnboardingForms.validateServerAddress("ws://robot.example.com") as OnboardingForms.ServerAddress.Invalid).reasonCode,
        )
    }

    @Test
    fun `server 地址 - 端口越界拒绝`() {
        assertEquals(
            OnboardingForms.REASON_PORT,
            (OnboardingForms.validateServerAddress("wss://host.example.com:99999") as OnboardingForms.ServerAddress.Invalid).reasonCode,
        )
    }

    @Test
    fun `server 地址 - userinfo拒绝`() {
        assertEquals(
            OnboardingForms.REASON_USERINFO,
            (OnboardingForms.validateServerAddress("wss://user:pass@host.example.com") as OnboardingForms.ServerAddress.Invalid).reasonCode,
        )
    }

    @Test
    fun `server 地址 - 安全终审 - 裸IP被EndpointPolicy拒绝`() {
        // 表单层合法（wss+host），但 SR-09 禁裸 IP —— 安全终审给出精确规则名
        assertEquals("bare_ip_host", OnboardingForms.securityCheck("wss://192.0.2.10:443"))
    }

    @Test
    fun `server 地址 - 安全终审 - 域名TLS通过`() {
        assertEquals(null, OnboardingForms.securityCheck("wss://robot.example.com:8443"))
    }

    // ---- 6 位配对码 ----

    @Test
    fun `配对码 - 6位数字通过`() {
        val r = OnboardingForms.validatePairingCode("123456")
        assertTrue(r is OnboardingForms.PairingCode.Valid)
        assertEquals("123456", (r as OnboardingForms.PairingCode.Valid).code)
    }

    @Test
    fun `配对码 - 5位与含字母均拒绝`() {
        assertTrue(OnboardingForms.validatePairingCode("12345") is OnboardingForms.PairingCode.Invalid)
        assertTrue(OnboardingForms.validatePairingCode("12a456") is OnboardingForms.PairingCode.Invalid)
        assertTrue(OnboardingForms.validatePairingCode("1234567") is OnboardingForms.PairingCode.Invalid)
    }

    // ---- 启动路由（⑥ 接线判定）----

    private fun creds() = DeviceCredentials(
        deviceId = "d1", deviceTokenHex = "00", tokenVersion = 1,
    )

    @Test
    fun `路由 - 无凭据进引导流`() {
        assertEquals(
            OnboardingForms.LaunchRoute.ONBOARDING,
            OnboardingForms.launchRoute(InMemoryDeviceCredentialStore(), "wss://robot.example.com"),
        )
    }

    @Test
    fun `路由 - 凭据缺server地址进引导流`() {
        val store = InMemoryDeviceCredentialStore().apply { save(creds()) }
        assertEquals(OnboardingForms.LaunchRoute.ONBOARDING, OnboardingForms.launchRoute(store, null))
        assertEquals(OnboardingForms.LaunchRoute.ONBOARDING, OnboardingForms.launchRoute(store, "not-a-url"))
    }

    @Test
    fun `路由 - 凭据且地址合法进主界面`() {
        val store = InMemoryDeviceCredentialStore().apply { save(creds()) }
        assertEquals(
            OnboardingForms.LaunchRoute.MAIN,
            OnboardingForms.launchRoute(store, "wss://robot.example.com:8443"),
        )
    }
}
