/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-55 自动降亮度 JVM 纯测试（不引 Robolectric——Settings 写入器经
 * [BrightnessSettingsWriter] 端口注入 Fake，docs/03 T3.3 同款手法）。
 *
 * 口径锚点（docs/01 FR-55 默认值勘误，2026-11-03 APK 实证）：600s 无交互 / 约 20% 亮度 /
 * 恢复写回系统亮度设置（original_brightness/original_mode）。
 */
class BrightnessManagerTest {

    /** Fake Settings 写入器：记录全部写调用（顺序敏感），读写值/成败可编程。 */
    private class FakeWriter(
        var brightness: Int? = 200,
        var mode: Int? = BrightnessManager.BRIGHTNESS_MODE_MANUAL,
        var writeBrightnessOk: Boolean = true,
        var writeModeOk: Boolean = true,
    ) : BrightnessSettingsWriter {
        val calls = ArrayList<String>()
        override fun readBrightness(): Int? = brightness
        override fun readBrightnessMode(): Int? = mode
        override fun writeBrightness(value: Int): Boolean {
            calls.add("brightness=$value")
            if (writeBrightnessOk) brightness = value
            return writeBrightnessOk
        }
        override fun writeBrightnessMode(value: Int): Boolean {
            calls.add("mode=$value")
            if (writeModeOk) mode = value
            return writeModeOk
        }
    }

    /** Fake 配置存储：默认值即勘误口径（600s/20%），持久化位内存化。 */
    private class FakeConfig(
        override var enabled: Boolean = true,
        override var delaySeconds: Int = BrightnessManager.DEFAULT_DELAY_SECONDS,
        override var dimPercent: Int = BrightnessManager.DEFAULT_DIM_PERCENT,
        override var windowStartHour: Int = -1,
        override var windowEndHour: Int = -1,
        override var applied: Boolean = false,
        override var originalBrightness: Int? = null,
        override var originalMode: Int? = null,
    ) : BrightnessConfigStore

    private var now = 0L
    private var hour = 12
    private val writer = FakeWriter()
    private val config = FakeConfig()
    private val logs = ArrayList<String>()
    private val manager = BrightnessManager(
        writer = writer,
        config = config,
        clock = { now },
        hourOfDay = { hour },
        log = { logs.add(it) },
    )

    @Test
    fun `默认值勘误 - 600s 阈值与 20% 降级百分比`() {
        assertEquals(600_000L, manager.delayMillis())
        assertEquals(600, BrightnessManager.DEFAULT_DELAY_SECONDS)
        assertEquals(20, BrightnessManager.DEFAULT_DIM_PERCENT)
        assertEquals(40, manager.dimTargetFor(200)) // 200 × 20% = 40
        assertEquals(1, manager.dimTargetFor(3)) // 下限钳 1（约 20% 语义，不熄屏）
    }

    @Test
    fun `未达无交互阈值不降亮度`() {
        now = 599_999 // 阈值 600s 差 1ms
        assertFalse(manager.tick())
        assertFalse(manager.isDimmed)
        assertTrue(writer.calls.isEmpty())
        now = 600_000
        assertTrue(manager.tick())
        assertTrue(manager.isDimmed)
    }

    @Test
    fun `600s 无交互降至约20百分比并留档 original_brightness 与 original_mode`() {
        now = 600_001
        assertTrue(manager.tick())
        assertEquals(listOf("brightness=40"), writer.calls) // 200 → 20%
        assertEquals(40, writer.brightness)
        assertTrue(config.applied)
        assertEquals(200, config.originalBrightness)
        assertEquals(BrightnessManager.BRIGHTNESS_MODE_MANUAL, config.originalMode)
        assertTrue(manager.isDimmed)
    }

    @Test
    fun `自动亮度模式先切手动 - 恢复时写回 original_mode`() {
        writer.mode = BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC
        now = 600_000
        assertTrue(manager.dim())
        assertEquals(listOf("mode=0", "brightness=40"), writer.calls) // 先手动后降亮度
        assertEquals(BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC, config.originalMode)

        assertTrue(manager.onUserInteraction()) // 交互即恢复
        assertEquals(listOf("mode=0", "brightness=40", "brightness=200", "mode=1"), writer.calls)
        assertEquals(BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC, writer.mode)
        assertFalse(config.applied)
        assertNull(config.originalBrightness)
        assertNull(config.originalMode)
        assertFalse(manager.isDimmed)
    }

    @Test
    fun `已降态重复 tick 幂等 - 零重复写`() {
        now = 600_000
        assertTrue(manager.dim())
        assertEquals(1, writer.calls.size)
        now += 1_800_000 // 继续无交互 30 分钟
        assertFalse(manager.tick())
        assertEquals(1, writer.calls.size) // 幂等：无新写调用
    }

    @Test
    fun `交互即恢复并写回系统亮度 - 清持久化位且重新计时`() {
        now = 600_000
        manager.dim()
        now = 1_200_000
        assertTrue(manager.onUserInteraction())
        assertEquals("brightness=200", writer.calls.last()) // 写回原亮度
        assertFalse(config.applied)
        assertFalse(manager.isDimmed)
        // 恢复后重新计时：立即 tick 不再降（idle 被重置）
        assertFalse(manager.tick())
        assertEquals(2, writer.calls.size)
    }

    @Test
    fun `恢复幂等 - 未降态 restore 与交互零写调用`() {
        assertFalse(manager.restore())
        assertFalse(manager.onUserInteraction())
        assertTrue(writer.calls.isEmpty())
        assertTrue(config.applied.not())
    }

    @Test
    fun `崩溃残留恢复 - recoverOnStartup 用留档写回并清位`() {
        // 模拟降亮度态崩溃重启：applied=true + 留档在，运行时 isDimmed=false
        config.applied = true
        config.originalBrightness = 200
        config.originalMode = BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC
        assertTrue(manager.recoverOnStartup())
        assertEquals(listOf("brightness=200", "mode=1"), writer.calls)
        assertFalse(config.applied)
        assertFalse(manager.isDimmed)
        assertFalse(manager.recoverOnStartup()) // 二次恢复幂等
    }

    @Test
    fun `时段窗口 - 窗口外不降与跨午夜窗口内降`() {
        config.windowStartHour = 22
        config.windowEndHour = 7 // 夜间 22:00~07:00（跨午夜）
        hour = 12
        now = 600_000
        assertFalse(manager.dim()) // 窗口外
        assertTrue(writer.calls.isEmpty())

        hour = 23 // 窗口内（深夜值守场景）
        assertTrue(manager.dim())
        assertEquals(1, writer.calls.size)

        // 同日窗口 [start,end)
        manager.restore()
        config.windowStartHour = 8
        config.windowEndHour = 20
        assertFalse(manager.inWindow(21))
        assertTrue(manager.inWindow(8))
        assertTrue(manager.inWindow(19))
    }

    @Test
    fun `开关关闭不降与写亮度失败回滚不置 applied 位`() {
        // ① enabled=false
        config.enabled = false
        now = 600_000
        assertFalse(manager.dim())
        assertTrue(writer.calls.isEmpty())
        config.enabled = true

        // ② 自动模式下写亮度失败 → 回滚模式写入，零持久化位
        writer.mode = BrightnessManager.BRIGHTNESS_MODE_AUTOMATIC
        writer.writeBrightnessOk = false
        assertFalse(manager.dim())
        assertEquals(listOf("mode=0", "brightness=40", "mode=1"), writer.calls) // 末笔=回滚
        assertFalse(config.applied)
        assertNull(config.originalBrightness)
        assertFalse(manager.isDimmed)
    }
}
