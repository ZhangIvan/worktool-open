/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.SingleFlightGuard
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 237 CLEAN_CACHE 清理企微存储空间（docs/10 B3，**L2**：破坏性清理——
 * 缓存 + 资源文件和其他企业的聊天记录；R2 复核：可逆安全替代 vs 不可逆清理
 * 分别取证，本执行器=企微内置清理流）：
 * 我 → 设置 → 存储空间 → 清理 → 确认。**互斥单飞**：清理进行中再次下发 →
 * MUTEX_BUSY（护栏经 [SingleFlightGuard] 注入，末步释放）。
 */
class CleanCacheExecutor(
    private val guard: SingleFlightGuard,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.CleanCache>() {

    override val commandType: Int get() = ExtendedCommandTypes.CLEAN_CACHE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L2

    override fun buildTypedPlan(intent: CommandIntent.CleanCache): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "scope=缓存+资源文件和其他企业的聊天记录, mutex=guard",
            steps = listOf(
                acquireGuardStep(),
                openMeTabStep(),
                openSettingsStep(),
                openStorageStep(),
                cleanStep(),
                confirmStep(),
                releaseGuardStep(),
            ),
            // B3-237 收口：失败/暂停/异常路径同样释放互斥（对齐上游「异常即释放」，
            // 原 KDoc 护栏语义废除——契约 S4 异常后重试不再 MUTEX_BUSY）。
            cleanup = { guard.release() },
        )

    /** 互斥首步：清理进行中 → MUTEX_BUSY（上游 101012 互斥码族）。 */
    private fun acquireGuardStep(): PlanStep = PlanStep(
        id = "acquire-mutex",
        description = "获取清理互斥锁（进行中再次触发=拒绝）",
    ) { _ ->
        if (!guard.tryAcquire()) {
            StepOutcome.Failure(ExecutorErrorCodes.MUTEX_BUSY, "企业微信缓存清理正在执行中")
        } else {
            StepOutcome.Success
        }
    }

    private fun openMeTabStep(): PlanStep = PlanStep(
        id = "open-me-tab",
        description = "切换到底部「我」Tab",
    ) { driver ->
        val tab = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.ME_TAB) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「我」Tab 缺失")
        CommonSteps.clickOrReject(driver, tab, "「我」Tab")
    }

    private fun openSettingsStep(): PlanStep = PlanStep(
        id = "open-settings",
        description = "进入「设置」页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「设置」入口") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「设置」入口未出现")
        CommonSteps.clickOrReject(driver, entry, "「设置」入口")
    }

    private fun openStorageStep(): PlanStep = PlanStep(
        id = "open-storage",
        description = "进入「存储空间」页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「存储空间」入口") { root ->
            rules.find(root, WeworkSelectorIds.STORAGE_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「存储空间」入口未出现")
        CommonSteps.clickOrReject(driver, entry, "「存储空间」入口")
    }

    private fun cleanStep(): PlanStep = PlanStep(
        id = "click-clean",
        description = "点击「清理」（勾选缓存+资源文件和其他企业的聊天记录）",
    ) { driver ->
        val clean = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.CLEAN_STORAGE_BUTTON) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「清理」按钮缺失")
        CommonSteps.clickOrReject(driver, clean, "「清理」")
    }

    private fun confirmStep(): PlanStep = PlanStep(
        id = "confirm-clean",
        description = "在确认弹窗点击「确定」（长耗时预算归超时配置面）",
    ) { driver ->
        val confirm = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "清理确认弹窗") { root ->
            rules.find(root, WeworkSelectorIds.DIALOG_CONFIRM)
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "清理确认弹窗未出现")
        CommonSteps.clickOrReject(driver, confirm, "清理确认「确定」")
    }

    /** 末步：成功路径释放互斥锁（失败/暂停路径由 plan.cleanup 兜底释放）。 */
    private fun releaseGuardStep(): PlanStep = PlanStep(
        id = "release-mutex",
        description = "清理完成释放互斥锁",
    ) { _ ->
        guard.release()
        StepOutcome.Success
    }
}
