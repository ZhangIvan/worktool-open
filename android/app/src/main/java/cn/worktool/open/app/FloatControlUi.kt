/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager

/**
 * 悬浮窗控制按钮状态机（docs/16 U2 / FR-50 最小集；纯 JVM 可测）。
 *
 * 两钮最小集 = **暂停 / 恢复**（printNode/OCR/回桌面调试项不采纳——NodeTreeDebugActivity
 * 已承载）。控制接线：
 * - 暂停 → PauseManager.pause(actor="floating-window")——与主界面 PausePanel、
 *   运行时段调度器三处同一状态机，pauseSource() 单一事实源（FR-52 三处一致）；
 * - 恢复 → **两击式确认**：首击钮变「确认恢复？」并武装 [CONFIRM_WINDOW_MILLIS]
 *   窗口，窗口内二击 = requestResume()+confirmResume()（FR-52 两步语义不绕过——
 *   首击=发起、二击=显式确认）；超时自动解除武装回到「恢复」；
 * - RESUME_PENDING 态（他处已发起恢复、待确认）：悬浮窗按钮直接显示「确认恢复」，
 *   单击即 confirmResume（发起步骤已在主界面完成，此处点击即第二步显式确认）。
 */
object FloatControlUi {

    /** 两击确认窗口（首击武装后 3s 内二击生效）。 */
    const val CONFIRM_WINDOW_MILLIS = 3_000L

    const val ACTOR = "floating-window"

    /** 主按钮动作（UI 文案锚点）。 */
    enum class Action { PAUSE, RESUME, CONFIRM_RESUME }

    /** 主按钮点击结果（toast/按钮文案刷新锚点）。 */
    enum class Outcome {
        /** 执行了暂停（原 RUNNING）。 */
        PAUSED_NOW,

        /** 首击恢复 = 武装确认（按钮变「确认恢复？」，3s 内二击生效）。 */
        RESUME_ASK_CONFIRM,

        /** 二击确认（或 RESUME_PENDING 单击确认）= 已恢复执行。 */
        RESUMED_NOW,
    }

    /** 当前主按钮动作（由暂停态 + 武装位共同决定）。 */
    fun primaryAction(state: PauseManager.State, confirmArmed: Boolean): Action = when (state) {
        PauseManager.State.RUNNING -> Action.PAUSE
        PauseManager.State.PAUSED ->
            if (confirmArmed) Action.CONFIRM_RESUME else Action.RESUME
        PauseManager.State.RESUME_PENDING -> Action.CONFIRM_RESUME
    }

    /**
     * 主按钮点击分派（UI 唯一入口；[confirm] 为悬浮窗持有的两击模型）。
     * 幂等性由 task-engine PauseManager 保证（重复 pause 幂等）。
     */
    fun onPrimary(
        manager: PauseManager,
        confirm: TwoClickConfirm,
        nowMillis: Long,
    ): Outcome = when (manager.state) {
        PauseManager.State.RUNNING -> {
            manager.pause(reason = "floating-window-button", actor = ACTOR, now = nowMillis)
            Outcome.PAUSED_NOW
        }
        PauseManager.State.PAUSED ->
            if (confirm.click(nowMillis)) {
                // 二击确认：两步显式确认在 3s 窗口内完成
                manager.requestResume(actor = ACTOR, now = nowMillis)
                manager.confirmResume(actor = ACTOR, now = nowMillis)
                Outcome.RESUMED_NOW
            } else {
                Outcome.RESUME_ASK_CONFIRM
            }
        PauseManager.State.RESUME_PENDING -> {
            // 发起已在他处完成（主界面「发起恢复」），本击即第二步显式确认
            confirm.disarm()
            manager.confirmResume(actor = ACTOR, now = nowMillis)
            Outcome.RESUMED_NOW
        }
    }
}

/**
 * 两击确认模型（首击武装 → [windowMillis] 内二击生效；超时自动失效）。
 * 纯状态机，时钟注入可测；线程约束 = UI 线程单线程使用（悬浮窗按钮回调）。
 */
class TwoClickConfirm(private val windowMillis: Long = FloatControlUi.CONFIRM_WINDOW_MILLIS) {

    private var armedAt: Long? = null

    /** 当前是否处于武装态（且未超时）。 */
    fun isArmed(nowMillis: Long): Boolean =
        armedAt?.let { nowMillis - it in 0..windowMillis } == true

    /**
     * 点击：武装态内 = 确认点击（解除武装返回 true）；否则武装并返回 false。
     */
    fun click(nowMillis: Long): Boolean {
        val armed = armedAt
        return if (armed != null && nowMillis - armed in 0..windowMillis) {
            armedAt = null
            true
        } else {
            armedAt = nowMillis
            false
        }
    }

    /** 解除武装（恢复执行成功/暂停态互切时调用）。 */
    fun disarm() {
        armedAt = null
    }
}
