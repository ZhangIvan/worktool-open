/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * U1 运行时段判定核心测试（docs/16 U1；纯 JVM）。
 * 覆盖：未启用恒在窗 / 常规窗口边界（含起点不含终点）/ 跨零点窗口两侧 /
 * 同值全天 / 默认 8:00–22:00 / 迁移跟踪 ENTER-EXIT-NONE / reset。
 */
class RuntimeWindowPolicyTest {

    private val config = { enabled: Boolean, s: Int, e: Int -> RuntimeWindowConfig(enabled, s, e) }

    @Test
    fun `未启用-恒在窗-不拦截`() {
        val c = config(false, 8 * 60, 22 * 60)
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 0))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 23 * 60 + 59))
    }

    @Test
    fun `常规窗口-含起点不含终点`() {
        val c = config(true, 8 * 60, 22 * 60)
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 8 * 60)) // 起点即生效
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 21 * 60 + 59))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 22 * 60)) // 终点到点退出
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 7 * 60 + 59))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 23 * 60))
    }

    @Test
    fun `跨零点窗口-两侧判定`() {
        val c = config(true, 22 * 60, 6 * 60) // 22:00–06:00
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 22 * 60))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 23 * 60 + 59))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 0))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 5 * 60 + 59))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 6 * 60))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 12 * 60))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 21 * 60 + 59))
    }

    @Test
    fun `起止同值-按全天处理-不锁死`() {
        val c = config(true, 9 * 60, 9 * 60)
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 0))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 9 * 60))
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 23 * 60 + 59))
    }

    @Test
    fun `默认配置-8点至22点`() {
        assertEquals(8 * 60, RuntimeWindowPolicy.DEFAULT_START_MINUTE)
        assertEquals(22 * 60, RuntimeWindowPolicy.DEFAULT_END_MINUTE)
        val c = RuntimeWindowConfig(enabled = true)
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 12 * 60))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 7 * 60))
    }

    @Test
    fun `迁移跟踪-出窗入窗只在边界产事件`() {
        val t = RuntimeWindowTracker()
        assertEquals(RuntimeWindowTracker.Transition.NONE, t.tick(WindowVerdict.IN_WINDOW)) // 稳态
        assertEquals(RuntimeWindowTracker.Transition.EXIT, t.tick(WindowVerdict.OUT_OF_WINDOW)) // 入→出
        assertEquals(RuntimeWindowTracker.Transition.NONE, t.tick(WindowVerdict.OUT_OF_WINDOW)) // 稳态
        assertEquals(RuntimeWindowTracker.Transition.ENTER, t.tick(WindowVerdict.IN_WINDOW)) // 出→入
        assertEquals(WindowVerdict.IN_WINDOW, t.current())
    }

    @Test
    fun `迁移跟踪-reset 重置基线不产事件`() {
        val t = RuntimeWindowTracker(WindowVerdict.OUT_OF_WINDOW)
        t.reset(WindowVerdict.OUT_OF_WINDOW)
        assertEquals(RuntimeWindowTracker.Transition.NONE, t.tick(WindowVerdict.OUT_OF_WINDOW))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, t.current())
    }

    @Test
    fun `非法分钟值-钳制到 0-1439 防越界`() {
        val c = config(true, 8 * 60 + 5000, -60) // 钳制成 1439 / 0 → 跨零点全窗
        // start=1439, end=0 → 跨零点：[1439,1440) ∪ [0,0) = 仅 1439 一分钟在窗
        assertEquals(WindowVerdict.IN_WINDOW, RuntimeWindowPolicy.verdict(c, 1439))
        assertEquals(WindowVerdict.OUT_OF_WINDOW, RuntimeWindowPolicy.verdict(c, 0))
    }
}
