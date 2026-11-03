/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.PauseManager

/**
 * 暂停/恢复快捷按钮的 UI 状态映射（FR-52，产品冲刺 A 车道；纯 JVM 可测）。
 *
 * FR-52 状态机（task-engine [PauseManager] 所有者）：
 * RUNNING --pause--> PAUSED --requestResume--> RESUME_PENDING --confirmResume--> RUNNING
 *
 * 主按钮语义随态切换：
 * - RUNNING →「暂停机器人」（一键暂停，即刻生效）；
 * - PAUSED →「发起恢复」（第一步显式确认发起）；
 * - RESUME_PENDING →「确认恢复」（第二步确认，真正恢复执行）；
 * - RESUME_PENDING 态附出次按钮「撤回恢复」（cancelResumeRequest → 回 PAUSED）。
 *
 * 从 PAUSED 直接 confirmResume 被 task-engine 拒绝（PauseConfirmationRequired）——
 * UI 永远不发起该非法调用（[primaryAction] 只产出合法动作），两步确认不可绕过。
 */
object PausePanelState {

    /** 主按钮动作。 */
    enum class Action { PAUSE, REQUEST_RESUME, CONFIRM_RESUME }

    /** 渲染状态（按钮文案锚点 code 由 UI 层映射字符串资源）。 */
    data class Panel(
        val action: Action,
        /** 态锚点：running / paused / resume_pending。 */
        val stateCode: String,
        /** RESUME_PENDING 态显示「撤回恢复」次按钮。 */
        val showCancelResume: Boolean,
    )

    const val STATE_RUNNING = "running"
    const val STATE_PAUSED = "paused"
    const val STATE_RESUME_PENDING = "resume_pending"

    fun of(state: PauseManager.State): Panel = when (state) {
        PauseManager.State.RUNNING -> Panel(Action.PAUSE, STATE_RUNNING, showCancelResume = false)
        PauseManager.State.PAUSED -> Panel(Action.REQUEST_RESUME, STATE_PAUSED, showCancelResume = false)
        PauseManager.State.RESUME_PENDING -> Panel(Action.CONFIRM_RESUME, STATE_RESUME_PENDING, showCancelResume = true)
    }

    /**
     * 主按钮点击分派（UI 唯一入口；只做合法迁移）。
     * 幂等性由 task-engine 保证（重复 pause 幂等），此处不额外防御。
     */
    fun dispatchPrimary(manager: PauseManager, action: Action, nowMillis: Long, actor: String) {
        when (action) {
            Action.PAUSE -> manager.pause(reason = "app-main-button", actor = actor, now = nowMillis)
            Action.REQUEST_RESUME -> manager.requestResume(actor = actor, now = nowMillis)
            Action.CONFIRM_RESUME -> manager.confirmResume(actor = actor, now = nowMillis)
        }
    }
}
