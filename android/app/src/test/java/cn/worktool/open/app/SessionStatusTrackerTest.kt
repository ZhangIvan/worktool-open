/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** 状态卡数据源：SessionStatusTracker 观测 + RobotStatusComputer 在线判定（≤15s 口径）。 */
class SessionStatusTrackerTest {

    private val interval = 15_000L

    @Test
    fun `未启动会话 - not_started且无心跳`() {
        val s = RobotStatusComputer.compute(
            sessionStarted = false, connectionOpen = false,
            lastHeartbeatAtMillis = null, nowMillis = 0, heartbeatIntervalMillis = interval,
        )
        assertFalse(s.online)
        assertEquals(RobotStatusComputer.STATE_NOT_STARTED, s.stateCode)
        assertNull(s.heartbeatAgeMillis)
    }

    @Test
    fun `心跳14秒 - 在线`() {
        val s = RobotStatusComputer.compute(
            sessionStarted = true, connectionOpen = true,
            lastHeartbeatAtMillis = 100_000, nowMillis = 114_000, heartbeatIntervalMillis = interval,
        )
        assertTrue(s.online)
        assertEquals(RobotStatusComputer.STATE_ONLINE, s.stateCode)
        assertEquals(14_000L, s.heartbeatAgeMillis)
    }

    @Test
    fun `心跳16秒 - 判离线（超15秒口径）`() {
        val s = RobotStatusComputer.compute(
            sessionStarted = true, connectionOpen = true,
            lastHeartbeatAtMillis = 100_000, nowMillis = 116_000, heartbeatIntervalMillis = interval,
        )
        assertFalse(s.online)
        assertEquals(RobotStatusComputer.STATE_OFFLINE, s.stateCode)
    }

    @Test
    fun `连接已断 - 即使心跳新鲜也判离线`() {
        val s = RobotStatusComputer.compute(
            sessionStarted = true, connectionOpen = false,
            lastHeartbeatAtMillis = 100_000, nowMillis = 101_000, heartbeatIntervalMillis = interval,
        )
        assertFalse(s.online)
    }

    @Test
    fun `tracker - 心跳写出与断连的观测链`() {
        val t = SessionStatusTracker()
        t.onSessionStarted()
        t.onConnected(atMillis = 1_000)
        t.onFrameSent(atMillis = 2_000)
        t.onFrameReceived(atMillis = 2_500)
        var snap = t.snapshot()
        assertTrue(snap.sessionStarted && snap.connectionOpen)
        assertEquals(2_000L, snap.lastHeartbeatAtMillis)
        assertEquals(2_500L, snap.lastInboundAtMillis)

        t.onDisconnected(atMillis = 9_000)
        snap = t.snapshot()
        assertFalse(snap.connectionOpen)
        assertEquals(9_000L, snap.disconnectedAtMillis)

        t.onSessionStopped()
        assertFalse(t.snapshot().sessionStarted)
    }

    @Test
    fun `tracker - 断连后重连心跳恢复在线`() {
        val t = SessionStatusTracker()
        t.onSessionStarted()
        t.onConnected(1_000)
        t.onFrameSent(1_000)
        t.onDisconnected(30_000)
        t.onConnected(60_000)
        t.onFrameSent(60_000)
        val s = RobotStatusComputer.compute(
            sessionStarted = t.snapshot().sessionStarted,
            connectionOpen = t.snapshot().connectionOpen,
            lastHeartbeatAtMillis = t.snapshot().lastHeartbeatAtMillis,
            nowMillis = 65_000, heartbeatIntervalMillis = interval,
        )
        assertTrue(s.online)
    }

    @Test
    fun `心跳距世人话文案 - 分档渲染`() {
        assertEquals("—", RobotStatusComputer.heartbeatAgeText(null))
        assertEquals("3s 前", RobotStatusComputer.heartbeatAgeText(3_400))
        assertEquals("2m 前", RobotStatusComputer.heartbeatAgeText(120_000))
        assertEquals("1h 前", RobotStatusComputer.heartbeatAgeText(3_600_000))
    }
}
