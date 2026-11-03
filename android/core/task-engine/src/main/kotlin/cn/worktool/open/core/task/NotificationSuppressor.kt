/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * FR-58 自动关闭企微新消息通知（docs/01 FR-58 行；APK 实证 j2/o.java:1524-1550
 * 「主功能初始化自动流程: 尝试关闭新消息通知」）。
 *
 * 契约要点：
 * - **开关默认关**（docs/01 FR-58「开关默认关、可配置」；与 APK `autoCloseNewMessageNotice`
 *   默认 true 不同——我方为无障碍动作面，默认不自动动用户 UI，显式开启才生效）；
 * - **幂等（已关不重复操作）**：读到开关已是关态 → 不点击，只导航退出；
 *   会话级幂等：DONE 后主功能重复初始化不再进企微页面；
 * - 选择器引用 core/accessibility 规则口径（wework.* 命名空间，WeComRulePack 同构），
 *   **选择器待真机校准**（W-M6 占位值，校准迁移另行任务）；
 * - 导航/点击失败一律尝试 `navigateBack()` 补偿（不把企微留在设置页），状态机 FAILED。
 *
 * 纯 JVM 状态机（不引 Robolectric）：无障碍动作经 [WeComNoticeNavigator] 端口注入，
 * app 装配层提供真机实现（WorkToolAccessibilityService 节点树 + 原子操作）。
 */
class NotificationSuppressor(
    private val navigator: WeComNoticeNavigator,
    /** 功能开关（FR-58 **默认关**：app 装配默认 false，设置页可开）。 */
    private val enabledProvider: () -> Boolean = { false },
    /** 单会话最大尝试次数（导航类失败允许有限重试，超过即沉默等待下一次主功能初始化）。 */
    private val maxAttempts: Int = DEFAULT_MAX_ATTEMPTS,
    private val log: (String) -> Unit = {},
) {

    /** 状态机（suppressOnMainInit 每步显式迁移，测试可逐步断言）。 */
    enum class State {
        /** 初始/未处理。 */
        IDLE,
        /** 正在导航企微设置页。 */
        NAVIGATING,
        /** 已到设置页，读开关状态。 */
        AT_SETTINGS,
        /** 正在点击关闭开关。 */
        TOGGLING,
        /** 本会话已确认关闭（终态，会话内不再操作）。 */
        DONE,
        /** 失败终态（可 reset / 下次初始化在尝试上限内重试）。 */
        FAILED,
    }

    /** 单次初始化尝试的结果语义。 */
    enum class Outcome {
        /** 功能开关未开（默认态）：零 UI 动作。 */
        SKIPPED_DISABLED,
        /** 本会话已处理过 / 尝试次数耗尽：零 UI 动作。 */
        SKIPPED_ALREADY_DONE,
        /** 开关本就关着：只导航退出，不点击（幂等核心语义）。 */
        DONE_ALREADY_OFF,
        /** 开关开着 → 点击关闭 → 复核已关 → 退出。 */
        DONE_TOGGLED_OFF,
        /** 导航到设置页失败。 */
        FAILED_NAVIGATION,
        /** 设置页上找不到/读不出通知开关。 */
        FAILED_READ,
        /** 点击未生效（复核仍为开）或点击动作失败。 */
        FAILED_TOGGLE,
    }

    var state: State = State.IDLE
        private set

    /** 本会话已尝试次数（上限 [maxAttempts]）。 */
    var attempts: Int = 0
        private set

    /**
     * 主功能初始化入口（APK j2/o.java:1524-1550 触发点同位）。
     * 单次调用至多推进一条完整路径：开/幂等短路 → 导航 → 读 → （必要时）关 → 复核 → 退出。
     */
    fun suppressOnMainInit(): Outcome {
        if (state == State.DONE) return Outcome.SKIPPED_ALREADY_DONE // 会话级幂等
        if (!enabledProvider()) return Outcome.SKIPPED_DISABLED // 默认关：零 UI 动作
        if (attempts >= maxAttempts) {
            log("notice-suppress skip 尝试次数耗尽($attempts/$maxAttempts)")
            return Outcome.SKIPPED_ALREADY_DONE
        }
        attempts++

        state = State.NAVIGATING
        if (!navigator.openNoticeSettings()) {
            state = State.FAILED
            log("notice-suppress fail 导航设置页失败")
            return Outcome.FAILED_NAVIGATION
        }

        state = State.AT_SETTINGS
        val noticeOn = navigator.isNoticeOn()
        if (noticeOn == null) {
            navigator.navigateBack() // 补偿：不把企微留在设置页
            state = State.FAILED
            log("notice-suppress fail 通知开关不可读")
            return Outcome.FAILED_READ
        }
        if (!noticeOn) {
            // 幂等：已关不重复操作——只退出
            val back = navigator.navigateBack()
            state = State.DONE
            log("notice-suppress ok 本就关闭(back=$back)，零点击")
            return Outcome.DONE_ALREADY_OFF
        }

        state = State.TOGGLING
        if (!navigator.clickNoticeSwitch()) {
            navigator.navigateBack()
            state = State.FAILED
            log("notice-suppress fail 点击开关失败")
            return Outcome.FAILED_TOGGLE
        }
        // 复核：点击后必须读到关态才算关闭（避免误点别处造成静默成功）
        if (navigator.isNoticeOn() != false) {
            navigator.navigateBack()
            state = State.FAILED
            log("notice-suppress fail 点击后复核仍为开")
            return Outcome.FAILED_TOGGLE
        }

        val back = navigator.navigateBack()
        if (!back) log("notice-suppress warn 退出设置页失败（关闭本身已生效）")
        state = State.DONE
        log("notice-suppress ok 已关闭新消息通知(back=$back)")
        return Outcome.DONE_TOGGLED_OFF
    }

    /** 新会话重置（状态机 FAILED 后手动/下次冷启动重置尝试计数）。 */
    fun reset() {
        state = State.IDLE
        attempts = 0
    }

    companion object {
        const val DEFAULT_MAX_ATTEMPTS: Int = 2
    }
}

/**
 * 企微「设置 → 新消息通知」导航端口（FR-58 接缝——纯 JVM 测试注入 Fake）。
 *
 * 生产实现（app 装配层 WeComNoticeNavigatorImpl，本车道交付）：WorkToolAccessibilityService
 * 节点树 + 原子操作点击/返回。导航链选择器（resourceId/text 双轨）引用 core/accessibility
 * 规则口径（wework.* 命名空间，WeComRulePack.selectors.json 同构形态）：
 * `wework.nav.me_tab → wework.settings.entry → wework.settings.new_message_notice`
 * ——**选择器待真机校准**（W-M6 占位值，校准结果迁移回 WeComRulePack 另行任务）。
 */
interface WeComNoticeNavigator {

    /**
     * 导航到企微「新消息通知」设置页（我 → 设置 → 新消息通知）。
     * false = 任一步超时/找不到节点（界面不可得、企微未在前台、选择器失配）。
     */
    fun openNoticeSettings(): Boolean

    /**
     * 读「新消息通知」开关当前态。true=开 false=关；
     * null = 页面上找不到开关或读不出选中态（调用方按失败处理并退出页面）。
     */
    fun isNoticeOn(): Boolean?

    /** 点击开关（期望从开 → 关）；false = 点击动作未被接受。 */
    fun clickNoticeSwitch(): Boolean

    /** 返回退出设置页（GLOBAL_ACTION_BACK，恢复企微至原界面）；false = 返回失败。 */
    fun navigateBack(): Boolean
}
