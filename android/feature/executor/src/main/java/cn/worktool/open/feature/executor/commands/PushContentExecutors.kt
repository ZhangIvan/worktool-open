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
import cn.worktool.open.feature.executor.ports.BatchOutcomeSink
import cn.worktool.open.feature.executor.ports.RemoteFilePort
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 218 PUSH_FILE 推送任意文件（docs/10 B2，L0；配 server fileUpload/upload）：
 * 端侧从 fileUrl 下载 → objectName 落盘（[RemoteFilePort]）→ 逐目标会话：
 * 「+」→ 文件 → 选择 objectName → 发送。目标>9 的建议层约束归网关（硬校验
 * 在服务端；端侧按收到载荷执行）。逐目标成败进 [BatchOutcomeSink]。
 */
class PushFileExecutor(
    private val remoteFiles: RemoteFilePort,
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.PushFile>() {

    override val commandType: Int get() = MessageTypes.PUSH_FILE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.PushFile): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, objectName=${intent.objectName}, " +
                "fileType=${intent.fileType}, fileUrl.length=${intent.fileUrl.length}, " +
                "extraText.length=${intent.extraText?.length ?: 0}",
            steps = listOf(fetchStep(intent)) +
                intent.targets.flatMap { target -> stepsPerTarget(flow, intent, target) } +
                flow.emitStep(),
        )
    }

    /** 下载+落盘（首步；失败=整指令失败，零目标执行）。 */
    private fun fetchStep(intent: CommandIntent.PushFile): PlanStep = PlanStep(
        id = "fetch-file",
        description = "从 fileUrl 下载文件并按 objectName=${intent.objectName} 落盘",
    ) { _ ->
        if (!remoteFiles.fetch(intent.fileUrl, intent.objectName)) {
            StepOutcome.Failure(
                ExecutorErrorCodes.DOWNLOAD_FAILED,
                "文件下载或落盘失败（fileUrl 不可达/越域/存储异常）",
            )
        } else {
            StepOutcome.Success
        }
    }

    private fun stepsPerTarget(
        flow: TargetFlow,
        intent: CommandIntent.PushFile,
        target: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
        },
        flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
            flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
        },
        flow.guardedStep("open-plus-$target", "点击「+」面板并进入「文件」") { driver ->
            val plus = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CHAT_PLUS_ENTRY) }
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "「+」面板入口缺失")
            if (!driver.click(plus)) {
                return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "点击「+」被界面拒绝")
            }
            val fileEntry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「文件」入口") { root ->
                rules.find(root, WeworkSelectorIds.CHAT_FILE_ENTRY)
            } ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "「文件」入口未出现")
            flow.record(CommonSteps.clickOrReject(driver, fileEntry, "「文件」入口"))
        },
        flow.guardedStep("pick-file-$target", "在文件选择器选中 ${intent.objectName} 并发送") { driver ->
            val item = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "文件「${intent.objectName}」") { root ->
                rules.findByText(root, intent.objectName)
            } ?: return@guardedStep flow.failAsGuarded(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "文件选择器未见「${intent.objectName}」（落盘失败？）",
            )
            flow.record(CommonSteps.clickOrReject(driver, item, "文件「${intent.objectName}」"))
        },
        flow.guardedStep("send-file-$target", "点击发送") { driver ->
            val send = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CHAT_SEND) }
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "发送按钮缺失")
            flow.record(CommonSteps.clickOrReject(driver, send, "发送"))
        },
        PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )
}

/**
 * 指令 224 PUSH_LINK 发送链接（docs/10 B2，L0；厂商无公开 schema → FR-34
 * 冒烟范围注记：仅契约自拟）：可选 icon 预取（失败=降级发送，不阻断）→
 * 逐目标会话输入链接卡片文本（标题+副标题+URL）→ 发送 → 校验 URL 上屏。
 * 真机「粘贴 URL 自动生成卡片」的触发路径待取证校准。
 */
class PushLinkExecutor(
    private val remoteFiles: RemoteFilePort,
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.PushLink>() {

    override val commandType: Int get() = MessageTypes.PUSH_LINK

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.PushLink): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        val cardText = "${intent.title}\n${intent.description}\n${intent.url}"
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, title.length=${intent.title.length}, " +
                "url.length=${intent.url.length}, icon=${intent.iconUrl != null}",
            steps = (if (intent.iconUrl != null) listOf(prefetchIconStep(intent)) else emptyList()) +
                intent.targets.flatMap { target -> stepsPerTarget(flow, intent, target, cardText) } +
                flow.emitStep(),
        )
    }

    /** icon 预取：失败=降级（无 icon 卡片），不记败不阻断。 */
    private fun prefetchIconStep(intent: CommandIntent.PushLink): PlanStep = PlanStep(
        id = "prefetch-icon",
        description = "预取 icon 图片（失败=降级无 icon 卡片发送）",
    ) { _ ->
        remoteFiles.fetch(intent.iconUrl.orEmpty(), "link-icon")
        StepOutcome.Success
    }

    private fun stepsPerTarget(
        flow: TargetFlow,
        intent: CommandIntent.PushLink,
        target: String,
        cardText: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位会话「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
        },
        flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
            flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
        },
        flow.guardedStep("send-link-$target", "输入链接卡片文本（标题/副标题/URL）并发送") { driver ->
            flow.record(ChatFlowSteps.inputAndSend(driver, rules, cardText))
        },
        flow.guardedStep("verify-link-$target", "校验链接 URL 已上屏") { driver ->
            val appeared = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "链接上屏") { root ->
                rules.findTextContains(root, intent.url)
            }
            if (appeared == null) {
                flow.failTarget(ExecutorErrorCodes.VERIFY_FAILED, "链接发送后未见 URL 上屏")
            }
            StepOutcome.Success
        },
        PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )
}
