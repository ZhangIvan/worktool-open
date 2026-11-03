/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 234 DELETE_CONTACT 删除联系人（docs/10 B2，**L2**：不可逆——
 * FR-53 独立 approver 由服务端治理，端侧执行前复检风险级，暂停门/干跑全量生效）：
 * 通讯录定位 → 详情页「…」菜单 → 删除 → 确认弹窗 → 校验移除。
 */
class DeleteContactExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.DeleteContact>() {

    override val commandType: Int get() = MessageTypes.DELETE_CONTACT

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L2

    override fun buildTypedPlan(intent: CommandIntent.DeleteContact): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "contact=${intent.contactName}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openDetailStep(intent),
                openMoreMenuStep(),
                clickDeleteStep(),
                confirmDeleteStep(),
                verifyRemovedStep(intent),
            ),
        )

    private fun openDetailStep(intent: CommandIntent.DeleteContact): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-contact-detail",
            description = "在通讯录定位「${intent.contactName}」并进入详情页（备注名通则）",
        ) { driver ->
            val node = driver.currentRoot()
                ?.let { rules.findByText(it, intent.contactName) }
                ?: return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "通讯录未找到「${intent.contactName}」（有备注必须用备注名）",
                )
            CommonSteps.clickOrReject(driver, node, "联系人「${intent.contactName}」")
        }

    private fun openMoreMenuStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-more-menu",
            description = "点击详情页右上「…」更多菜单",
        ) { driver ->
            val more = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CONTACT_DETAIL_MORE) }
                ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「…」更多菜单缺失")
            CommonSteps.clickOrReject(driver, more, "「…」更多菜单")
        }

    private fun clickDeleteStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "click-delete",
            description = "点击菜单「删除」项",
        ) { driver ->
            val delete = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「删除」菜单项") { root ->
                rules.find(root, WeworkSelectorIds.CONTACT_DELETE_ENTRY)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "「删除」菜单项未出现（权限不足族？）",
            )
            CommonSteps.clickOrReject(driver, delete, "「删除」菜单项")
        }

    private fun confirmDeleteStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "confirm-delete",
            description = "在确认弹窗点击「删除」（不可逆操作）",
        ) { driver ->
            val confirm = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "删除确认弹窗") { root ->
                rules.find(root, WeworkSelectorIds.DIALOG_DELETE_CONFIRM)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "删除确认弹窗未出现（界面拒绝删除入口）",
            )
            CommonSteps.clickOrReject(driver, confirm, "删除确认「删除」")
        }

    private fun verifyRemovedStep(intent: CommandIntent.DeleteContact): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "verify-removed",
            description = "返回通讯录校验「${intent.contactName}」已移除",
        ) { driver ->
            driver.goBack()
            val stillThere = driver.currentRoot()
                ?.let { rules.findByText(it, intent.contactName) }
            if (stillThere != null) {
                StepOutcome.Failure(ExecutorErrorCodes.VERIFY_FAILED, "删除后通讯录仍可见「${intent.contactName}」")
            } else {
                StepOutcome.Success
            }
        }
}

/**
 * 指令 236 DELETE_SINGLE_DIRECTIONAL_CONTACT 删除单向微信客户（docs/10 B2，
 * **L2**；≤50/次、去重去空在规范化侧）：通讯录 → 我的客户 → 逐名勾选 →
 * 删除 → 确认。**部分成功不压平**：存在未匹配目标 → 逐名失败记录，其余继续。
 */
class DeleteSingleDirectionalContactExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.DeleteSingleDirectionalContact>() {

    override val commandType: Int get() = ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L2

    override fun buildTypedPlan(intent: CommandIntent.DeleteSingleDirectionalContact): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "names=${intent.names.size}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openMyCustomersStep(),
            ) + intent.names.flatMap { name -> stepsPerName(flow, name) } + listOf(
                clickBatchDeleteStep(),
                confirmBatchDeleteStep(),
                flow.emitStep(),
            ),
        )
    }

    private fun openMyCustomersStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-my-customers",
            description = "进入通讯录「我的客户」列表",
        ) { driver ->
            val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「我的客户」入口") { root ->
                rules.find(root, WeworkSelectorIds.MY_CUSTOMERS_ENTRY)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.STEP_TIMEOUT,
                "「我的客户」入口未出现（通讯录页未就绪？）",
            )
            CommonSteps.clickOrReject(driver, entry, "「我的客户」入口")
        }

    /** 逐名两步：勾选 + 记格（未匹配=格失败，不中断批量；记格步不加守卫）。 */
    private fun stepsPerName(flow: TargetFlow, name: String) = listOf(
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "select-$name",
            description = "在单向微信客户列表勾选「$name」",
        ) { driver ->
            flow.beginTarget(name)
            val node = driver.currentRoot()?.let { rules.findByText(it, name) }
            if (node == null) {
                flow.failTarget(ExecutorErrorCodes.NODE_NOT_FOUND, "单向客户列表未见「$name」")
            } else if (!driver.click(node)) {
                flow.failTarget(ExecutorErrorCodes.ACTION_REJECTED, "勾选「$name」被界面拒绝")
            }
            StepOutcome.Success
        },
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "record-$name",
            description = "记录「$name」勾选结果",
        ) { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )

    private fun clickBatchDeleteStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "click-batch-delete",
            description = "点击批量「删除」按钮",
        ) { driver ->
            val delete = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CONTACT_DELETE_ENTRY) }
                ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "批量删除按钮缺失")
            CommonSteps.clickOrReject(driver, delete, "批量「删除」")
        }

    private fun confirmBatchDeleteStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "confirm-batch-delete",
            description = "在确认弹窗点击「删除」（不可逆批量操作）",
        ) { driver ->
            val confirm = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "批量删除确认弹窗") { root ->
                rules.find(root, WeworkSelectorIds.DIALOG_DELETE_CONFIRM)
            } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "批量删除确认弹窗未出现")
            CommonSteps.clickOrReject(driver, confirm, "批量删除确认「删除」")
        }
}
