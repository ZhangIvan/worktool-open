/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.intent.ExtendedCommandTypes
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 239 CREATE_CUSTOMER_GROUP_LIVE_QRCODE 客户群永久群活码（docs/10 B3，L1）：
 * 工作台 → 客户群 →（create=输入新群名 / select=选择已有群）→ 生成 →
 * 保存到相册 → **经 504 通道推送** `{groupName, qrcode, extraText:"239"}`。
 * selectList ≤5 不重复、newGroupName 必填在规范化侧校验。
 */
class CreateCustomerGroupLiveQrCodeExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.CreateCustomerGroupLiveQrCode>() {

    override val commandType: Int get() = ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.CreateCustomerGroupLiveQrCode): ExecutionPlan {
        val create = intent.groupMode != "select" && intent.selectList.isEmpty()
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=${intent.groupMode ?: "auto"}, create=$create, " +
                "newGroupName=${intent.newGroupName}, selectList=${intent.selectList.size}, " +
                "template=${intent.groupTemplate != null}",
            steps = listOf(
                openWorkbenchStep(),
                openCustomerGroupStep(),
            ) + (if (create) {
                listOf(fillNewGroupNameStep(intent))
            } else {
                intent.selectList.flatMap { selectExistingGroupSteps(it) }
            }) + listOf(
                generateStep(),
                saveToAlbumStep(),
                pushQrStep(intent),
            ),
        )
    }

    private fun openWorkbenchStep(): PlanStep = PlanStep(
        id = "open-workbench",
        description = "切换到底部「工作台」Tab",
    ) { driver ->
        val tab = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.WORKBENCH_TAB) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「工作台」Tab 缺失")
        CommonSteps.clickOrReject(driver, tab, "「工作台」Tab")
    }

    private fun openCustomerGroupStep(): PlanStep = PlanStep(
        id = "open-customer-group",
        description = "进入工作台「客户群」管理页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「客户群」入口") { root ->
            rules.find(root, WeworkSelectorIds.CUSTOMER_GROUP_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「客户群」入口未出现（客户群功能未开通？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「客户群」入口")
    }

    private fun fillNewGroupNameStep(intent: CommandIntent.CreateCustomerGroupLiveQrCode): PlanStep = PlanStep(
        id = "fill-new-group-name",
        description = "输入群满后自动建群的统一群名「${intent.newGroupName}」",
    ) { driver ->
        val input = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.LIVE_QR_NAME_INPUT) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "新群名输入框缺失")
        if (!driver.click(input) || !driver.inputText(input, intent.newGroupName)) {
            return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "输入新群名被界面拒绝")
        }
        StepOutcome.Success
    }

    private fun selectExistingGroupSteps(group: String): List<PlanStep> = listOf(
        PlanStep(
            id = "select-$group",
            description = "在「选择已有群聊」页精确匹配并勾选「$group」",
        ) { driver ->
            val node = driver.currentRoot()?.let { rules.findByText(it, group) }
                ?: return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "选择页未见完整群名「$group」（须按页面文本精确匹配）",
                )
            CommonSteps.clickOrReject(driver, node, "已有群「$group」")
        },
    )

    private fun generateStep(): PlanStep = PlanStep(
        id = "generate-live-qr",
        description = "点击生成群活码",
    ) { driver ->
        val confirm = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.LIVE_QR_CONFIRM) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "生成确认按钮缺失（新建群聊功能受限族？）",
            )
        CommonSteps.clickOrReject(driver, confirm, "生成确认")
    }

    private fun saveToAlbumStep(): PlanStep = PlanStep(
        id = "save-to-album",
        description = "把群活码保存到相册",
    ) { driver ->
        val save = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「保存到相册」按钮") { root ->
            rules.find(root, WeworkSelectorIds.LIVE_QR_SAVE_BUTTON)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "「保存到相册」按钮未出现（活码生成失败族）",
        )
        CommonSteps.clickOrReject(driver, save, "「保存到相册」")
    }

    /** 末步：经 504 通道推送（extraText 固定 "239" 区分普通群二维码回调）。 */
    private fun pushQrStep(intent: CommandIntent.CreateCustomerGroupLiveQrCode): PlanStep = PlanStep(
        id = "push-504",
        description = "读活码内容并经 504 通道推送（extraText=239 标记）",
    ) { driver ->
        val qr = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "活码内容") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_QRCODE_CONTENT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "活码内容未出现（二维码内容解析失败族）",
        )
        structured.onStructuredResult(
            MessageTypes.GET_GROUP_QRCODE,
            buildJsonObject {
                put("groupName", intent.newGroupName)
                put("qrcode", qr.text.orEmpty())
                put("extraText", LIVE_QR_MARKER)
            },
        )
        StepOutcome.Success
    }

    private companion object {
        const val LIVE_QR_MARKER = "239"
    }
}
