/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * FR-55 自动降亮度（闭源版 delta：长时间值守省电/防烧屏，恢复可逆）。
 *
 * 默认值勘误口径（docs/01 FR-55 行，2026-11-03 APK 实证 k2/h.java:25-38）：
 * **600s 无交互**降至**约 20% 亮度**；交互即恢复并**写回系统亮度设置**
 * （original_brightness/original_mode，与 APK 独立 SP `auto_brightness` 同键位）。
 *
 * 结构（纯 JVM 可测，docs/03 T3.3 同款——不引入 Robolectric，Android 面走端口）：
 * - [IdleDetector]：无交互计时（`onInteraction` 重置计时 + 交互即恢复）；
 * - [BrightnessSettingsWriter]：Settings.System 亮度/亮度模式写入器端口——app 装配层
 *   提供两条生产路径：API<30 `Settings.System.putInt` 与 API≥30 `putIntForUser`
 *   （USER_CURRENT，亮度 Android 9 起按用户隔离）；测试注入 Fake；
 * - [BrightnessConfigStore]：阈值/时段可配置 + `applied/originalBrightness/originalMode`
 *   持久化（崩溃残留恢复 = APK MyApplication.java:122-137「启动恢复」同口径）。
 *
 * 幂等契约：
 * - 降亮度：已降态再 `tick`/`dim` 不重复写系统设置；
 * - 恢复：未降态 `restore` 不产生任何写调用（false 直接返回）。
 *
 * 生产接线（app，本车道交付组件、装配另行接线）：周期轮询 `tick()`（如每分钟）+
 * 交互源（触屏/键事件）回调 `onUserInteraction()`；启动时先 `recoverOnStartup()`。
 */
class BrightnessManager(
    private val writer: BrightnessSettingsWriter,
    private val config: BrightnessConfigStore,
    private val clock: () -> Long = { 0L },
    /** 当前小时（0..23，本地时区）——时段窗口判定；测试注入固定值。 */
    private val hourOfDay: () -> Int = { 0 },
    private val idle: IdleDetector = IdleDetector(clock),
    private val log: (String) -> Unit = {},
) {

    /** 运行时降亮度态（与 config.applied 持久化位独立：崩溃后 isDimmed=false 而 applied=true）。 */
    var isDimmed: Boolean = false
        private set

    /** 无交互计时（FR-55 首件：交互重置 + 阈值判定）。 */
    class IdleDetector(private val clock: () -> Long) {
        private var lastInteractionAt = clock()

        /** 交互事件入口：重置计时；由 [BrightnessManager.onUserInteraction] 消费。 */
        fun onInteraction() {
            lastInteractionAt = clock()
        }

        /** 当前已空闲毫秒数。 */
        fun idleMillis(): Long = clock() - lastInteractionAt

        /** 显式重置（恢复亮度后重新计时）。 */
        fun reset() {
            lastInteractionAt = clock()
        }
    }

    /** 周期轮询入口（如每分钟调度一次）：阈值/时段/开关全满足则降亮度。 */
    fun tick(): Boolean = dim()

    /**
     * 交互事件入口：重置无交互计时；若当前处于降亮度态则**立即恢复**并写回系统亮度
     * （docs/01 FR-55「交互即恢复并写回系统亮度设置」）。返回是否触发了恢复。
     */
    fun onUserInteraction(): Boolean {
        idle.onInteraction()
        return if (isDimmed) restore() else false
    }

    /**
     * 降亮度。幂等：已降态（[isDimmed]）直接 false，不重复写系统设置。
     * 全前置：enabled + 时段窗口内 + 无交互时长 ≥ 阈值 + 读得到当前亮度。
     *
     * 写入序列（original_brightness/original_mode 口径，k2/h.java:25-38）：
     * 1. 捕获当前亮度与亮度模式（自动模式先切手动，否则系统会覆盖手设值）；
     * 2. 写入降级目标 = max(1, 原亮度 × dimPercent%)；
     * 3. 全部落盘成功才置 applied/originals（中途失败回滚已写项，不置位）。
     */
    fun dim(): Boolean {
        if (isDimmed) return false // 幂等：已降不再操作
        if (!config.enabled) return false
        if (!inWindow(hourOfDay())) {
            log("brightness-dim skip 窗口外(${config.windowStartHour}..${config.windowEndHour})")
            return false
        }
        if (idle.idleMillis() < delayMillis()) {
            return false
        }
        val original = writer.readBrightness() ?: run {
            log("brightness-dim fail 读亮度失败")
            return false
        }
        val originalMode = writer.readBrightnessMode()
        // 自动亮度模式下手设值会被系统覆盖 → 先切手动（originalMode 留档恢复）
        if (originalMode != null && originalMode == BRIGHTNESS_MODE_AUTOMATIC) {
            if (!writer.writeBrightnessMode(BRIGHTNESS_MODE_MANUAL)) {
                log("brightness-dim fail 切手动模式失败")
                return false
            }
        }
        val target = dimTargetFor(original)
        if (!writer.writeBrightness(target)) {
            // 回滚模式，不落任何持久化位
            if (originalMode != null && originalMode == BRIGHTNESS_MODE_AUTOMATIC) {
                writer.writeBrightnessMode(originalMode)
            }
            log("brightness-dim fail 写亮度失败（已回滚）")
            return false
        }
        config.originalBrightness = original
        config.originalMode = originalMode
        config.applied = true
        isDimmed = true
        log("brightness-dim ok original=$original target=$target mode=$originalMode")
        return true
    }

    /**
     * 恢复亮度并写回系统设置。幂等：未降态直接 false（零写调用）。
     * 恢复序列 = 先写回 originalBrightness（自动模式接管前落地）再写回 originalMode
     * → 清持久化位。仅当降亮度时改写过模式（原为自动）才写回模式——手动模式零冗余写。
     */
    fun restore(): Boolean {
        if (!isDimmed) return false // 幂等：未降不操作
        val original = config.originalBrightness
        if (original == null) {
            // 持久化位在而留档丢失（不应发生）：清残留位，失败暴露
            log("brightness-restore fail 留档丢失，清残留 applied 位")
            config.applied = false
            isDimmed = false
            return false
        }
        val brightnessRestored = writer.writeBrightness(original)
        val modeRestored = if (config.originalMode == BRIGHTNESS_MODE_AUTOMATIC) {
            writer.writeBrightnessMode(BRIGHTNESS_MODE_AUTOMATIC)
        } else true
        if (!brightnessRestored || !modeRestored) {
            log("brightness-restore fail 写回失败(brightness=$brightnessRestored,mode=$modeRestored)")
            return false // 持久化位保留：下次恢复/启动恢复仍可重试
        }
        config.applied = false
        config.originalBrightness = null
        config.originalMode = null
        isDimmed = false
        idle.reset() // 恢复后重新计时
        log("brightness-restore ok brightness=$original")
        return true
    }

    /**
     * 启动恢复（APK MyApplication.java:122-137 同口径）：崩溃发生在降亮度态时
     * `applied=true` 残留持久化位 → 用留档 original_brightness/original_mode 写回。
     * 返回是否执行了恢复；留档缺失则清残留位（fail-loud 日志，不盲写系统设置）。
     */
    fun recoverOnStartup(): Boolean {
        if (!config.applied || isDimmed) return false
        if (config.originalBrightness == null) {
            log("brightness-recover 清残留 applied 位（留档缺失）")
            config.applied = false
            return false
        }
        isDimmed = true // 复用 restore 的写回+清位序列
        val ok = restore()
        log("brightness-recover ${if (ok) "ok" else "fail"}")
        return ok
    }

    /** 当前无交互时长是否已达阈值（阈值=delaySeconds，勘误默认 600s）。 */
    fun idleThresholdReached(): Boolean = idle.idleMillis() >= delayMillis()

    /** 阈值毫秒（config 读数钳位 1..86400 秒，APK 同范围）。 */
    fun delayMillis(): Long = config.delaySeconds.coerceIn(MIN_DELAY_SECONDS, MAX_DELAY_SECONDS) * 1000L

    /** 降级目标亮度：max(1, 原亮度 × dimPercent%)（dimPercent 钳位 1..99）。 */
    fun dimTargetFor(original: Int): Int =
        maxOf(1, original * config.dimPercent.coerceIn(MIN_DIM_PERCENT, MAX_DIM_PERCENT) / 100)

    /**
     * 时段窗口判定（阈值/时段可配置的「时段」半边）。
     * start/end 任一 <0 = 全天；start==end = 全天；start<end = [start,end)；
     * start>end = 跨午夜（hour≥start || hour<end）。
     */
    fun inWindow(hour: Int): Boolean {
        val start = config.windowStartHour
        val end = config.windowEndHour
        if (start < 0 || end < 0 || start == end) return true
        return if (start < end) hour in start until end else hour >= start || hour < end
    }

    companion object {
        /** Settings.System.SCREEN_BRIGHTNESS_MODE 值（自持常量，JVM 模块不引 android 类）。 */
        const val BRIGHTNESS_MODE_MANUAL: Int = 0
        const val BRIGHTNESS_MODE_AUTOMATIC: Int = 1

        /** 勘误默认值（docs/01 FR-55：600s 无交互 / 约 20% 亮度）。 */
        const val DEFAULT_DELAY_SECONDS: Int = 600
        const val DEFAULT_DIM_PERCENT: Int = 20

        /** 配置钳位范围（APK k2/h.java 同口径：delay 1~86400s）。 */
        const val MIN_DELAY_SECONDS: Int = 1
        const val MAX_DELAY_SECONDS: Int = 86_400
        const val MIN_DIM_PERCENT: Int = 1
        const val MAX_DIM_PERCENT: Int = 99
    }
}

/**
 * Settings.System 亮度写入器端口（FR-55 接缝——JVM 测试注入 Fake，不引 Robolectric）。
 *
 * 生产实现（app 装配层 SystemBrightnessWriter，本车道交付）：两条路径——
 * - API<30：`Settings.System.putInt(resolver, SCREEN_BRIGHTNESS/MODE, v)`；
 * - API≥30：`putIntForUser(..., UserHandle.USER_CURRENT)`（亮度按用户隔离后的写回路径）。
 * 返回 false = 权限缺失（WRITE_SETTINGS）/系统写入失败，调用方按失败处理不置持久化位。
 */
interface BrightnessSettingsWriter {
    /** 读当前亮度（0..255 口径）；null = 读取失败。 */
    fun readBrightness(): Int?

    /** 读亮度模式（0=手动 1=自动）；null = 不可用（部分 ROM 无此设置）。 */
    fun readBrightnessMode(): Int?

    /** 写亮度；false = 失败（权限/系统）。 */
    fun writeBrightness(value: Int): Boolean

    /** 写亮度模式；false = 失败。 */
    fun writeBrightnessMode(value: Int): Boolean
}

/**
 * FR-55 配置 + 持久化端口（阈值/时段可配置）。
 *
 * 生产实现（app 装配层 BrightnessSettings）：独立 SharedPreferences `auto_brightness`
 * （与 APK k2/h.java:30-38 同键位：enabled/delay_seconds/applied/original_brightness/
 * original_mode；dim_percent/window_* 为我方增强键）。
 */
interface BrightnessConfigStore {
    /** 总开关（APK 默认 true；我方默认随 docs/01 FR-55 P2 语义，装配层可覆写）。 */
    var enabled: Boolean

    /** 无交互降级阈值（秒，1..86400，勘误默认 600）。 */
    var delaySeconds: Int

    /** 降级目标亮度百分比（1..99，勘误默认 20 = 约 20% 亮度）。 */
    var dimPercent: Int

    /** 生效时段起点（0..23；-1 = 不限时段）。 */
    var windowStartHour: Int

    /** 生效时段终点（0..23；-1 = 不限时段；与 start 构成 [start,end) 或跨午夜窗口）。 */
    var windowEndHour: Int

    /** 降亮度态持久化位（崩溃残留恢复依据，MyApplication.java:122-137 口径）。 */
    var applied: Boolean

    /** 降亮度前的系统亮度留档（original_brightness）。 */
    var originalBrightness: Int?

    /** 降亮度前的亮度模式留档（original_mode；null = 当时不可读）。 */
    var originalMode: Int?
}
