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
import cn.worktool.open.feature.executor.ports.CurrentCorpPort
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 221 ADD_NEED_DEAL 添加待办（docs/10 B3，L0；仅企业内部成员）：
 * 逐目标内部成员单聊 →「+」→ 待办 → 填写内容 → 添加。逐目标成败进端口。
 */
class AddNeedDealExecutor(
    private val sink: BatchOutcomeSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.AddNeedDeal>() {

    override val commandType: Int get() = MessageTypes.ADD_NEED_DEAL

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.AddNeedDeal): ExecutionPlan {
        val flow = TargetFlow(commandType, sink)
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targets=${intent.targets.size}, content.length=${intent.content.length}",
            steps = intent.targets.flatMap { target -> stepsPerTarget(flow, intent, target) } +
                flow.emitStep(),
        )
    }

    private fun stepsPerTarget(
        flow: TargetFlow,
        intent: CommandIntent.AddNeedDeal,
        target: String,
    ) = listOf(
        flow.guardedStep("open-$target", "定位内部成员单聊「$target」并进入聊天页") { driver ->
            flow.beginTarget(target)
            flow.record(ChatFlowSteps.openConversationViaHome(driver, rules, target))
        },
        flow.guardedStep("await-chat-$target", "等待聊天页就绪") { driver ->
            flow.record(CommonSteps.awaitChatReady(rules, timeouts).action(driver))
        },
        flow.guardedStep("open-todo-$target", "点击「+」面板并进入「待办」") { driver ->
            val plus = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.CHAT_PLUS_ENTRY) }
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到待办按钮（「+」面板入口缺失）")
            if (!driver.click(plus)) {
                return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "点击「+」被界面拒绝")
            }
            val todo = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「待办」入口") { root ->
                rules.find(root, WeworkSelectorIds.CHAT_TODO_ENTRY)
            } ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到待办列表（「待办」入口未出现）")
            flow.record(CommonSteps.clickOrReject(driver, todo, "「待办」入口"))
        },
        flow.guardedStep("fill-todo-$target", "在新建待办页填写内容并添加") { driver ->
            val input = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "待办内容输入框") { root ->
                rules.find(root, WeworkSelectorIds.TODO_CONTENT_INPUT)
            } ?: return@guardedStep flow.failAsGuarded(
                ExecutorErrorCodes.NODE_NOT_FOUND, "未能打开待办新建页或填写内容",
            )
            if (!driver.click(input) || !driver.inputText(input, intent.content)) {
                return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.ACTION_REJECTED, "填写待办内容被界面拒绝")
            }
            val submit = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.TODO_SUBMIT) }
                ?: return@guardedStep flow.failAsGuarded(ExecutorErrorCodes.NODE_NOT_FOUND, "待办「添加」按钮缺失")
            flow.record(CommonSteps.clickOrReject(driver, submit, "待办「添加」"))
        },
        PlanStep(id = "finish-$target", description = "记录「$target」格结果") { _ ->
            flow.finishTarget()
            StepOutcome.Success
        },
    )
}

/**
 * 指令 222 CLOCK_IN 上下班打卡（docs/10 B3，L0；无参：按页面首个可用打卡按钮）：
 * 工作台 →「打卡」→ 识别「你已在打卡范围内」→ 点击打卡按钮 → 识别成功文案
 * （上班·正常 / 之后可打下班卡 / 今日打卡已完成）。
 */
class ClockInExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.ClockIn>() {

    override val commandType: Int get() = MessageTypes.CLOCK_IN

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.ClockIn): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "无参（按页面首个可用打卡按钮）",
            steps = listOf(
                openWorkbenchStep(),
                openClockStep(),
                awaitInRangeStep(),
                clickClockStep(),
                awaitSuccessStep(),
            ),
        )

    private fun openWorkbenchStep(): PlanStep = PlanStep(
        id = "open-workbench",
        description = "切换到底部「工作台」Tab",
    ) { driver ->
        val tab = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.WORKBENCH_TAB) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "「工作台」Tab 缺失")
        CommonSteps.clickOrReject(driver, tab, "「工作台」Tab")
    }

    private fun openClockStep(): PlanStep = PlanStep(
        id = "open-clock",
        description = "进入「打卡」页",
    ) { driver ->
        val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「打卡」入口") { root ->
            rules.find(root, WeworkSelectorIds.CLOCK_ENTRY)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "「打卡」入口未出现（企业未开通打卡族）",
        )
        CommonSteps.clickOrReject(driver, entry, "「打卡」入口")
    }

    private fun awaitInRangeStep(): PlanStep = PlanStep(
        id = "await-in-range",
        description = "等待「你已在打卡范围内」提示",
    ) { driver ->
        val marker = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "打卡范围提示") { root ->
            rules.findTextContains(root, "已在打卡范围")
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到在打卡范围（页面无范围内提示）",
        )
        StepOutcome.Success
    }

    private fun clickClockStep(): PlanStep = PlanStep(
        id = "click-clock",
        description = "点击「上班打卡/下班打卡」按钮",
    ) { driver ->
        val button = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.CLOCK_ACTION_BUTTON) }
            ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未找到上下班打卡")
        CommonSteps.clickOrReject(driver, button, "打卡按钮")
    }

    private fun awaitSuccessStep(): PlanStep = PlanStep(
        id = "await-success",
        description = "识别打卡成功文案（上班·正常/之后可打下班卡/今日打卡已完成）",
    ) { driver ->
        val success = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "打卡结果文案") { root ->
            SUCCESS_MARKERS.firstNotNullOfOrNull { marker -> rules.findTextContains(root, marker) }
        } ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "未发现完成打卡")
        StepOutcome.Success
    }

    private companion object {
        val SUCCESS_MARKERS = listOf("上班·正常", "之后可打下班卡", "今日打卡已完成")
    }
}

/**
 * 指令 223 SWITCH_CORP 切换企业（docs/10 B3，L1；每次切换=登出登录勿频繁）：
 * 已在目标企业 → 直接成功（零 UI 动作）；否则消息页抽屉 → 企业列表点目标 →
 * 校验首页企业名已切换。
 */
class SwitchCorpExecutor(
    private val corp: CurrentCorpPort,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.SwitchCorp>() {

    override val commandType: Int get() = MessageTypes.SWITCH_CORP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.SwitchCorp): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "targetCorp=${intent.targetCorp}, currentCorp=${corp.currentCorpName()}",
            steps = listOf(
                checkAlreadyStep(intent),
                openDrawerStep(),
                clickTargetCorpStep(intent),
                verifySwitchedStep(intent),
            ),
        )

    /** 已在目标企业：空过成功（后续步对已成功指令零副作用口径——步自身短路）。 */
    private fun checkAlreadyStep(intent: CommandIntent.SwitchCorp): PlanStep = PlanStep(
        id = "check-already",
        description = "若已在目标企业「${intent.targetCorp}」则直接成功（零 UI 动作）",
    ) { _ ->
        alreadySwitched = corp.currentCorpName() == intent.targetCorp
        StepOutcome.Success
    }

    private fun openDrawerStep(): PlanStep = PlanStep(
        id = "open-drawer",
        description = "打开消息页左上抽屉企业列表",
    ) { driver ->
        if (alreadySwitched) return@PlanStep StepOutcome.Success
        val home = ChatFlowSteps.goHome(driver, rules)
        if (home is StepOutcome.Failure) return@PlanStep home
        val drawer = driver.currentRoot()?.let { rules.find(it, WeworkSelectorIds.DRAWER_ENTRY) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "未找到企业列表（抽屉入口缺失）",
            )
        CommonSteps.clickOrReject(driver, drawer, "抽屉入口")
    }

    private fun clickTargetCorpStep(intent: CommandIntent.SwitchCorp): PlanStep = PlanStep(
        id = "click-target-corp",
        description = "在企业列表点击「${intent.targetCorp}」",
    ) { driver ->
        if (alreadySwitched) return@PlanStep StepOutcome.Success
        // B3-223 收口：企业列表滚动查找（契约 S5 滚动 1 轮；对齐 openConversation
        // 兜底口径——首屏未命中→可滚动容器内翻页重查）。
        val item = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "企业「${intent.targetCorp}」") { root ->
            rules.findByText(root, intent.targetCorp)
                ?: scrollFindText(driver, root, intent.targetCorp)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "未找到目标企业: ${intent.targetCorp}",
        )
        CommonSteps.clickOrReject(driver, item, "企业「${intent.targetCorp}」")
    }

    /** 滚动查找文本（≤4 页；容器=树内首个可滚动节点；无容器/滚不动=null）。 */
    private fun scrollFindText(driver: cn.worktool.open.feature.executor.runtime.UiDriver, root: cn.worktool.open.core.accessibility.tree.UiNode, text: String): cn.worktool.open.core.accessibility.tree.UiNode? {
        var container: cn.worktool.open.core.accessibility.tree.UiNode? = null
        fun findScrollable(node: cn.worktool.open.core.accessibility.tree.UiNode): Boolean {
            if (node.isScrollable) {
                container = node
                return true
            }
            for (i in 0 until node.childCount) {
                if (node.childAt(i)?.let(::findScrollable) == true) return true
            }
            return false
        }
        findScrollable(root)
        val c = container ?: return null
        repeat(4) {
            if (!driver.scroll(c, cn.worktool.open.core.accessibility.tree.ScrollDirection.FORWARD)) return null
            driver.currentRoot()?.let { r ->
                rules.findByText(r, text)?.let { return it }
            }
        }
        return null
    }

    private fun verifySwitchedStep(intent: CommandIntent.SwitchCorp): PlanStep = PlanStep(
        id = "verify-switched",
        description = "校验首页企业名已切换为「${intent.targetCorp}」（切换企业校验）",
    ) { driver ->
        if (alreadySwitched) return@PlanStep StepOutcome.Success
        val name = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "首页企业名") { root ->
            rules.find(root, WeworkSelectorIds.HOME_CORP_NAME)
        } ?: return@PlanStep StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "切换企业校验失败（首页企业名不可读）",
        )
        if (name.text?.trim() == intent.targetCorp) {
            StepOutcome.Success
        } else {
            StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "切换企业校验失败（当前=${name.text}，目标=${intent.targetCorp}）",
            )
        }
    }

    /** 已在目标企业短路标记（单执行器实例内；FR-52 暂停下一次执行重置）。 */
    private var alreadySwitched = false
        set(value) {
            field = value
        }
}
