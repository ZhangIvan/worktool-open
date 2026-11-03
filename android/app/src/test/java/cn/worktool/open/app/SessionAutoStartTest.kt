/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.DeviceCredentials
import org.junit.Assert.assertEquals
import org.junit.Test

/** BP-1/SP-1 会话自启判定（配对状态判定 / 自启幂等）。 */
class SessionAutoStartTest {

    private fun creds(
        deviceId: String = "wt-abc123",
        tokenHex: String = "aabb",
        sessionId: String? = "s-1",
        wsPath: String? = "/api/v2/device-sessions/ws/xyz",
    ) = DeviceCredentials(
        deviceId = deviceId,
        deviceTokenHex = tokenHex,
        tokenVersion = 1,
        sessionId = sessionId,
        wsPath = wsPath,
    )

    // ---- 配对状态判定（凭据缺失/残缺）----

    @Test
    fun `无凭据-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_CREDENTIALS,
            SessionAutoStart.decide(isRunning = false, credentials = null, serverAddress = "wss://s.example.com"),
        )
    }

    @Test
    fun `凭据残缺-token空-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_CREDENTIALS,
            SessionAutoStart.decide(isRunning = false, credentials = creds(tokenHex = "  "), serverAddress = "wss://s.example.com"),
        )
    }

    @Test
    fun `凭据残缺-deviceId空-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_CREDENTIALS,
            SessionAutoStart.decide(isRunning = false, credentials = creds(deviceId = ""), serverAddress = "wss://s.example.com"),
        )
    }

    // ---- server 地址判定（与 OnboardingForms 同一口径）----

    @Test
    fun `server地址缺失-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_SERVER,
            SessionAutoStart.decide(isRunning = false, credentials = creds(), serverAddress = null),
        )
    }

    @Test
    fun `server地址非法格式-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_SERVER,
            SessionAutoStart.decide(isRunning = false, credentials = creds(), serverAddress = "robot.example.com"),
        )
    }

    // ---- 启动判定 ----

    @Test
    fun `已配对且未在跑-启动`() {
        assertEquals(
            SessionAutoStart.Decision.START,
            SessionAutoStart.decide(isRunning = false, credentials = creds(), serverAddress = "wss://s.example.com"),
        )
    }

    @Test
    fun `已配对且未在跑-仅sessionId无wsPath也可启动（重建入口兜底）`() {
        // BP-2 语义：wsPath 过期后凭据仍有 sessionId → 经 /api/v2/device-sessions 重建
        assertEquals(
            SessionAutoStart.Decision.START,
            SessionAutoStart.decide(
                isRunning = false,
                credentials = creds(wsPath = null, sessionId = "s-9"),
                serverAddress = "wss://s.example.com",
            ),
        )
    }

    @Test
    fun `wsPath与sessionId双空-凭据不可用-不启动`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_UNUSABLE_CREDENTIALS,
            SessionAutoStart.decide(
                isRunning = false,
                credentials = creds(wsPath = "", sessionId = null),
                serverAddress = "wss://s.example.com",
            ),
        )
    }

    // ---- 自启幂等（isRunning 判定）----

    @Test
    fun `已在跑-幂等不重启`() {
        assertEquals(
            SessionAutoStart.Decision.KEEP_RUNNING,
            SessionAutoStart.decide(isRunning = true, credentials = creds(), serverAddress = "wss://s.example.com"),
        )
    }

    @Test
    fun `幂等优先于凭据可用性-在跑即不重启`() {
        // 已在跑的会话即使凭据形状异常也不重启（防 onStart 重拨 WS 抖动）
        assertEquals(
            SessionAutoStart.Decision.KEEP_RUNNING,
            SessionAutoStart.decide(
                isRunning = true,
                credentials = creds(wsPath = "", sessionId = null),
                serverAddress = "wss://s.example.com",
            ),
        )
    }

    @Test
    fun `阻断优先于幂等-server非法即使在跑也判SKIP`() {
        assertEquals(
            SessionAutoStart.Decision.SKIP_NO_SERVER,
            SessionAutoStart.decide(isRunning = true, credentials = creds(), serverAddress = "ftp://x"),
        )
    }
}
