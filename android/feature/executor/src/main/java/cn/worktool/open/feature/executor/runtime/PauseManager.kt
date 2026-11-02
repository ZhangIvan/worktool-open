/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

/**
 * 暂停门（FR-52 用户接管）：执行器**执行前**与**每步安全断点**查询的接口。
 *
 * FR-52 语义（docs/01 FR-52 行）：
 * - 一键暂停 = 停止取指；进行中动作序列**执行至安全断点停止**；
 * - 接管期间在线指令只入队不执行（入队归上层调度；执行器侧体现为
 *   [CommandRunner] 返回 PAUSED 且零副作用）；
 * - 恢复须显式确认（恢复动作不在执行器侧，由悬浮窗/控制台触发）。
 *
 * 生产实现落位：FR-50 悬浮窗控制 + 控制台（app 壳层，T3.x 后续任务卡接线）；
 * 暂停态三处一致（悬浮窗/控制台/审计）由实现方保证，本接口只暴露只读查询。
 */
interface PauseManager {

    /** true = 用户接管暂停中（机器人不得产生新副作用）。 */
    fun isPaused(): Boolean

    /**
     * 暂停来源标识（如 `floating-window` / `console` / `auto-breaker`）；
     * 未暂停返回 null。用于执行报告与审计留痕（FR-52 三处一致性核对）。
     */
    fun pauseSource(): String?
}

/** 永不暂停的门（测试/纯本地模式默认装配）。 */
class NeverPausedPauseManager : PauseManager {
    override fun isPaused(): Boolean = false
    override fun pauseSource(): String? = null
}
