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
import cn.worktool.open.feature.executor.ports.QrCodeJoinPort
import cn.worktool.open.feature.executor.ports.QrJoinOutcome
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 233 SCAN_QR_CODE 扫一扫（docs/10 B3，L0；双语义：入群扫码/登录验证码，
 * 字段名未冻结）：扫一扫入口 → 相册选图 → 识别结果 →「加入群聊」。
 * qrContent 直传时跳过相机/相册步（验证码回调语义归服务端面，端侧仅入群流）。
 */
class ScanQrCodeExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ScanQrCode>() {

    override val commandType: Int get() = MessageTypes.SCAN_QR_CODE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.ScanQrCode): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "qrContent=${intent.qrContent != null}",
            steps = listOf(
                openScanStep(),
                openAlbumStep(),
                awaitResultStep(),
                joinStep(),
            ),
        )

    private fun openScanStep(): PlanStep = PlanStep(
        id = "open-scan",
        description = "打开「扫一扫」",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「扫一扫」入口") { root ->
            rules.find(root, WeworkSelectorIds.SCAN_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到扫一扫按钮",
        )
        CommonSteps.clickOrReject(driver, entry, "「扫一扫」入口")
    }

    private fun openAlbumStep(): PlanStep = PlanStep(
        id = "open-album",
        description = "在扫一扫页打开「相册」选图",
    ) { driver ->
        val album = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「相册」入口") { root ->
            rules.find(root, WeworkSelectorIds.SCAN_ALBUM_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到扫一扫列表（相册入口缺失）")
        CommonSteps.clickOrReject(driver, album, "「相册」入口")
    }

    private fun awaitResultStep(): PlanStep = PlanStep(
        id = "await-result",
        description = "等待二维码识别结果页",
    ) { driver ->
        val result = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "扫码结果页") { root ->
            rules.find(root, WeworkSelectorIds.SCAN_JOIN_BUTTON) ?: rules.findByText(root, "二维码")
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "扫码结果页未出现（二维码解析失败族）",
        )
        StepOutcome.Success
    }

    private fun joinStep(): PlanStep = PlanStep(
        id = "join-group",
        description = "点击「加入群聊」（入群扫码语义）",
    ) { driver ->
        val join = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.SCAN_JOIN_BUTTON) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "未找到扫码后加入群聊的选择入口",
            )
        CommonSteps.clickOrReject(driver, join, "「加入群聊」")
    }
}

/**
 * 指令 243 QR_CODE_JOIN_GROUP 二维码图片 URL 入群（docs/10 B3，L1；个微侧——
 * 依赖 FR-56 WeChat 规则包，生产接线经 [QrCodeJoinPort]，企微执行器不可用面）：
 * 下载二维码写相册 → 个微「扫一扫→相册第一张→加入群聊」→ 入群结果。
 * 二维码失效 → QR_CODE_INVALID（上游 201113）；下载失败 → DOWNLOAD_FAILED。
 */
class QrCodeJoinGroupExecutor(
    private val qrJoin: QrCodeJoinPort,
) : TypedCommandExecutor<CommandIntent.QrCodeJoinGroup>() {

    override val commandType: Int get() = ExtendedCommandTypes.QR_CODE_JOIN_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.QrCodeJoinGroup): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "fileUrl.length=${intent.fileUrl.length}, side=WeChat(FR-56)",
            steps = listOf(
                downloadStep(intent),
                scanAlbumJoinStep(),
            ),
        )

    private fun downloadStep(intent: CommandIntent.QrCodeJoinGroup): PlanStep = PlanStep(
        id = "download-and-save-album",
        description = "下载二维码图片并写入系统相册（相册第一张）",
    ) { _ ->
        if (!qrJoin.downloadAndSaveToAlbum(intent.fileUrl)) {
            StepOutcome.Failure(ExecutorErrorCodes.DOWNLOAD_FAILED, "二维码图片下载或写相册失败")
        } else {
            StepOutcome.Success
        }
    }

    private fun scanAlbumJoinStep(): PlanStep = PlanStep(
        id = "wechat-scan-album-join",
        description = "个微「发现→扫一扫→相册第一张→加入群聊」流程",
    ) { _ ->
        when (qrJoin.weChatScanAlbumJoin()) {
            QrJoinOutcome.JOINED -> StepOutcome.Success
            QrJoinOutcome.QR_EXPIRED ->
                StepOutcome.Failure(ExecutorErrorCodes.QR_CODE_INVALID, "二维码已过期/失效（页面中间提示）")
            QrJoinOutcome.QR_NOT_RECOGNIZED ->
                StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "扫码无结果（非群二维码）")
        }
    }
}
