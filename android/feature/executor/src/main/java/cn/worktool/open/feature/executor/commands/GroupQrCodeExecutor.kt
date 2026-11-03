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
import cn.worktool.open.feature.executor.ports.StructuredResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/**
 * 指令 504 GET_GROUP_QRCODE 获取群二维码（docs/10 B2，L0）：
 * **我方自拟兼容面**——厂商 APK 无主动 504 执行器（仅常量；正式生效面=回调通道
 * 类型 0 与 239 成功后附推）。收到主动 504 指令不静默丢：进群 → 群设置 →
 * 群二维码 → 读码 → 结构化推送（extraText 固定 "504" 标记主动面，区别 239 的
 * "239" 标记）。获取前提（APP 高级设置开关）归设备配置面，缺码=失败不虚构。
 */
class GetGroupQrCodeExecutor(
    private val structured: StructuredResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetGroupQrCode>() {

    override val commandType: Int get() = MessageTypes.GET_GROUP_QRCODE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetGroupQrCode): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groupName=${intent.groupName}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                openQrCodeStep(),
                readAndPushStep(intent),
            ),
        )

    private fun openQrCodeStep(): PlanStep = PlanStep(
        id = "open-group-qrcode",
        description = "在群设置页点击「群二维码」入口",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「群二维码」入口") { root ->
            rules.find(root, WeworkSelectorIds.SETTINGS_GROUP_QRCODE_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「群二维码」入口未出现（非群主/权限族？）",
        )
        CommonSteps.clickOrReject(driver, entry, "「群二维码」入口")
    }

    private fun readAndPushStep(intent: CommandIntent.GetGroupQrCode): PlanStep = PlanStep(
        id = "read-and-push",
        description = "读取群二维码内容并推送（extraText=504 标记主动面）",
    ) { driver ->
        val qr = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "群二维码内容") { root ->
            rules.find(root, WeworkSelectorIds.GROUP_QRCODE_CONTENT)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.STEP_TIMEOUT,
            "群二维码内容未出现（设备「获取群二维码」开关未开？）",
        )
        structured.onStructuredResult(
            commandType,
            buildJsonObject {
                put("groupName", intent.groupName)
                put("qrcode", qr.text.orEmpty())
                put("extraText", ACTIVE_MARKER)
            },
        )
        StepOutcome.Success
    }

    private companion object {
        const val ACTIVE_MARKER = "504"
    }
}
