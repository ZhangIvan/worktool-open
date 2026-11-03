/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.app.RobotStatusComputer.RttBand
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * 采纳项 #8 RTT 彩条测试（docs/16 优化-采纳；纯 JVM）。
 * 覆盖：分档阈值边界（<100 绿 / 998 黄 / 999 红 / null 灰 / 负值灰）/
 * 出向→入向配对采样 / 多帧连发不重复计时 / 重连后旧样本不配对。
 */
class RttSamplingTest {

    @Test
    fun `分档-阈值边界`() {
        assertEquals(RttBand.GREEN, RobotStatusComputer.rttBand(0L))
        assertEquals(RttBand.GREEN, RobotStatusComputer.rttBand(99L))
        assertEquals(RttBand.YELLOW, RobotStatusComputer.rttBand(100L))
        assertEquals(RttBand.YELLOW, RobotStatusComputer.rttBand(998L))
        assertEquals(RttBand.RED, RobotStatusComputer.rttBand(999L))
        assertEquals(RttBand.RED, RobotStatusComputer.rttBand(5_000L))
    }

    @Test
    fun `分档-无样本与负值`() {
        assertEquals(RttBand.UNKNOWN, RobotStatusComputer.rttBand(null))
        assertEquals(RttBand.UNKNOWN, RobotStatusComputer.rttBand(-1L))
    }

    @Test
    fun `采样-出向入向配对差值`() {
        val t = SessionStatusTracker()
        t.onFrameSent(1_000L)
        t.onFrameReceived(1_180L)
        assertEquals(180L, t.snapshot().lastRttMillis)
    }

    @Test
    fun `采样-多帧连发只配对首个出向帧`() {
        val t = SessionStatusTracker()
        t.onFrameSent(1_000L)
        t.onFrameSent(1_050L) // 连发第二帧不重置计时锚点
        t.onFrameReceived(1_200L)
        assertEquals(200L, t.snapshot().lastRttMillis)
        t.onFrameReceived(1_400L) // 已配对完成的入向不再产出 RTT
        assertEquals(200L, t.snapshot().lastRttMillis)
    }

    @Test
    fun `采样-重连后旧出向时间戳不跨连接配对`() {
        val t = SessionStatusTracker()
        t.onFrameSent(1_000L)
        t.onDisconnected(1_100L)
        t.onConnected(2_000L) // 新连接清锚点
        t.onFrameReceived(2_500L)
        assertNull(t.snapshot().lastRttMillis)
    }

    @Test
    fun `采样-未发送先接收不产出 RTT`() {
        val t = SessionStatusTracker()
        t.onFrameReceived(1_000L)
        assertNull(t.snapshot().lastRttMillis)
    }
}
