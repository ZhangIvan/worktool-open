/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** BP-7/SP-3 设置引导页三卡状态机（放行判定 / 手动确认 / 不适用自动绿 / MIUI 设备检测）。 */
class SetupGuideCardsTest {

    private fun card(
        key: SetupGuideCards.CardKey,
        detection: SetupGuideCards.Detection,
        manual: Boolean = false,
    ) = SetupGuideCards.Card(key, detection, manual)

    // ---- 单卡绿判定 ----

    @Test
    fun `系统检测GREEN-绿`() {
        assertTrue(
            SetupGuideCards.isGreen(
                card(SetupGuideCards.CardKey.ACCESSIBILITY, SetupGuideCards.Detection.GREEN),
            ),
        )
    }

    @Test
    fun `PENDING未确认-不绿`() {
        assertFalse(
            SetupGuideCards.isGreen(
                card(SetupGuideCards.CardKey.BATTERY_OPTIMIZATION, SetupGuideCards.Detection.PENDING),
            ),
        )
    }

    @Test
    fun `PENDING用户手动确认-绿`() {
        assertTrue(
            SetupGuideCards.isGreen(
                card(SetupGuideCards.CardKey.MIUI_AUTOSTART, SetupGuideCards.Detection.PENDING, manual = true),
            ),
        )
    }

    @Test
    fun `NOT_APPLICABLE不适用-自动绿`() {
        assertTrue(
            SetupGuideCards.isGreen(
                card(SetupGuideCards.CardKey.MIUI_AUTOSTART, SetupGuideCards.Detection.NOT_APPLICABLE),
            ),
        )
    }

    // ---- 三卡放行（allGreen / notGreenKeys）----

    @Test
    fun `三卡全绿-放行`() {
        val all = SetupGuideCards.CardKey.entries.map {
            card(it, SetupGuideCards.Detection.GREEN)
        }
        assertTrue(SetupGuideCards.allGreen(all))
        assertTrue(SetupGuideCards.notGreenKeys(all).isEmpty())
    }

    @Test
    fun `一卡未绿-不放行且notGreenKeys指出该卡`() {
        val all = SetupGuideCards.CardKey.entries.map {
            card(it, if (it == SetupGuideCards.CardKey.BATTERY_OPTIMIZATION) SetupGuideCards.Detection.PENDING else SetupGuideCards.Detection.GREEN)
        }
        assertFalse(SetupGuideCards.allGreen(all))
        assertEquals(listOf(SetupGuideCards.CardKey.BATTERY_OPTIMIZATION), SetupGuideCards.notGreenKeys(all))
    }

    @Test
    fun `初始态三卡全PENDING-不放行`() {
        assertFalse(SetupGuideCards.allGreen(SetupGuideCards.initial().values))
    }

    // ---- 手动确认状态转移（检测刷新不清掉用户确认）----

    @Test
    fun `confirmManually只翻转确认位-不改检测态`() {
        val pending = card(SetupGuideCards.CardKey.MIUI_AUTOSTART, SetupGuideCards.Detection.PENDING)
        val confirmed = SetupGuideCards.confirmManually(pending)
        assertEquals(SetupGuideCards.Detection.PENDING, confirmed.detection)
        assertTrue(confirmed.manualConfirmed)
        assertFalse(pending.manualConfirmed) // 不可变：原卡不变
    }

    @Test
    fun `手动确认后检测刷新仍绿-确认位保留`() {
        // SetupGuideActivity.refreshDetections 语义：新 Card 保留 previous.manualConfirmed
        val confirmed = SetupGuideCards.confirmManually(
            card(SetupGuideCards.CardKey.MIUI_AUTOSTART, SetupGuideCards.Detection.PENDING, manual = true),
        )
        val refreshed = SetupGuideCards.Card(
            SetupGuideCards.CardKey.MIUI_AUTOSTART,
            SetupGuideCards.Detection.PENDING,
            manualConfirmed = confirmed.manualConfirmed,
        )
        assertTrue(SetupGuideCards.isGreen(refreshed))
    }

    // ---- MIUI 设备检测（卡 c 口径）----

    @Test
    fun `MIUI制造商检测-xiaomi_redmi_poco`() {
        assertTrue(MiuiAutostartGuide.isMiuiDevice("Xiaomi"))
        assertTrue(MiuiAutostartGuide.isMiuiDevice("REDMI"))
        assertTrue(MiuiAutostartGuide.isMiuiDevice("poco"))
    }

    @Test
    fun `非MIUI制造商-不适用自动绿`() {
        assertFalse(MiuiAutostartGuide.isMiuiDevice("HUAWEI"))
        assertFalse(MiuiAutostartGuide.isMiuiDevice("Google"))
        assertFalse(MiuiAutostartGuide.isMiuiDevice(null))
        assertFalse(MiuiAutostartGuide.isMiuiDevice("  "))
    }
}
