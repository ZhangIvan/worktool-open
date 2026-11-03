/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import java.util.Locale

/**
 * 设置引导页三卡状态机（BP-7/SP-3，纯 JVM 可测）。
 *
 * 三卡 = 无障碍服务 / 电池优化豁免 / MIUI 自启动（docs/14 BP-7：厂商坑无产品内
 * 引导，用户卡在系统设置层）。每卡两种达成途径：
 * - 系统可检测（无障碍：enabled_accessibility_services；电池豁免：
 *   PowerManager.isIgnoringBatteryOptimizations）→ [Detection.GREEN]；
 * - 系统检测不到（MIUI 自启动授权无公开 API）→ [Detection.PENDING] +
 *   manualConfirmed（用户按指引完成设置后手动确认，如实标注为「用户自述」）；
 * - 设备不适用（非 MIUI 设备的自启动卡）→ [Detection.NOT_APPLICABLE]（自动绿，
 *   卡面仍显示指引文案——「检测不到就只显示指引」口径）。
 *
 * 放行条件 = [allGreen]（三卡全绿 →「完成，去配对」按钮可用）。
 */
object SetupGuideCards {

    enum class CardKey { ACCESSIBILITY, BATTERY_OPTIMIZATION, MIUI_AUTOSTART }

    /** 单卡检测状态。 */
    enum class Detection {
        /** 系统检测未达成（或不可检测且用户未确认）——未绿。 */
        PENDING,

        /** 系统检测已达成——绿。 */
        GREEN,

        /** 设备不适用（如非 MIUI 的自启动卡）——绿，指引文案照显。 */
        NOT_APPLICABLE,
    }

    /** 单卡状态（detection + 用户手动确认位）。 */
    data class Card(val key: CardKey, val detection: Detection, val manualConfirmed: Boolean = false)

    fun isGreen(card: Card): Boolean = when (card.detection) {
        Detection.GREEN, Detection.NOT_APPLICABLE -> true
        Detection.PENDING -> card.manualConfirmed
    }

    /** 放行判定：三卡全绿。 */
    fun allGreen(cards: Collection<Card>): Boolean = cards.all { isGreen(it) }

    /** 未绿卡 key 列表（UI 提示「还差哪几步」；顺序按 CardKey 定义序）。 */
    fun notGreenKeys(cards: Collection<Card>): List<CardKey> =
        cards.filter { !isGreen(it) }.map { it.key }

    /** 用户手动确认（仅系统不可检测卡由 UI 暴露该入口）。 */
    fun confirmManually(card: Card): Card = card.copy(manualConfirmed = true)

    /** 初始状态：三卡全部 PENDING（检测刷新前）。 */
    fun initial(): Map<CardKey, Card> =
        CardKey.entries.associateWith { Card(it, Detection.PENDING) }
}

/**
 * MIUI 设备检测（SP-3 c 卡，纯 JVM 可测）：小米系（Xiaomi/Redmi/POCO）的
 * Build.MANUFACTURER 判定；自启动授权本身无公开检测 API → 卡面只显示指引 + 手动确认。
 */
object MiuiAutostartGuide {

    private val MIUI_MANUFACTURERS = setOf("xiaomi", "redmi", "poco")

    fun isMiuiDevice(manufacturer: String?): Boolean {
        val m = manufacturer?.trim()?.lowercase(Locale.US) ?: return false
        return m in MIUI_MANUFACTURERS
    }
}
