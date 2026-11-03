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
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 213 ADD_FRIEND_BY_PHONE（docs/10 B2，L1；**同 type 双语义**）：
 * - 添加语义（leavingMsg 存在）：通讯录 → 添加朋友 → 手机号搜索 → 发送申请
 *   （附言 leavingMsg）。日配额 100 人次/风控由服务端治理（端侧执行前复检风险级）。
 * - 修改语义（leavingMsg 缺省）：定位联系人（name 优先，phone 兜底）→ 详情页
 *   设置备注 markName/markExtra/标签 tagList → 确定。
 * 通过后的 105 事件回调归事件面（T3.5），不在本执行器。
 */
class AddFriendByPhoneExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.AddFriendByPhone>() {

    override val commandType: Int get() = MessageTypes.ADD_FRIEND_BY_PHONE

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.AddFriendByPhone): ExecutionPlan =
        if (intent.leavingMsg != null) addPlan(intent) else modifyPlan(intent)

    /** 添加语义：搜索手机号 → 发送好友申请（附言）。 */
    private fun addPlan(intent: CommandIntent.AddFriendByPhone): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=add, phone=${intent.phone}, markName=${intent.markName != null}, " +
                "tags=${intent.tagList.size}, leavingMsg.length=${intent.leavingMsg?.length ?: 0}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openAddFriendStep(),
                inputPhoneStep(intent),
                sendRequestStep(intent),
            ),
        )

    /** 修改语义：定位联系人详情 → 写备注/标签 → 确定。 */
    private fun modifyPlan(intent: CommandIntent.AddFriendByPhone): ExecutionPlan {
        val locateKey = intent.name ?: intent.phone
        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "mode=modify, locate=$locateKey, markName=${intent.markName != null}, " +
                "markExtra=${intent.markExtra != null}, tags=${intent.tagList.size}",
            steps = listOf(
                CommonSteps.openContactsTab(rules),
                openContactDetailStep(locateKey),
                writeMarkStep(intent),
                confirmMarkStep(),
            ),
        )
    }

    private fun openAddFriendStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-add-friend",
            description = "进入通讯录「添加朋友」页",
        ) { driver ->
            val entry = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「添加朋友」入口") { root ->
                rules.find(root, WeworkSelectorIds.ADD_FRIEND_ENTRY)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.STEP_TIMEOUT,
                "「添加朋友」入口未出现（通讯录页未就绪？）",
            )
            CommonSteps.clickOrReject(driver, entry, "「添加朋友」入口")
        }

    private fun inputPhoneStep(intent: CommandIntent.AddFriendByPhone): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "input-phone",
            description = "在搜索框输入手机号并等待结果卡片",
        ) { driver ->
            val input = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.SEARCH_INPUT) }
                ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "手机号搜索框缺失")
            if (!driver.click(input) || !driver.inputText(input, intent.phone)) {
                return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "输入手机号被界面拒绝")
            }
            val result = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "搜索结果卡片") { root ->
                rules.find(root, WeworkSelectorIds.SEARCH_RESULT_ITEM)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "手机号 ${intent.phone} 无搜索结果（号码不存在或不可见）",
            )
            CommonSteps.clickOrReject(driver, result, "搜索结果卡片")
        }

    private fun sendRequestStep(intent: CommandIntent.AddFriendByPhone): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "send-request",
            description = "发送好友申请（附言长度=${intent.leavingMsg?.length ?: 0}）",
        ) { driver ->
            val send = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「发送」申请按钮") { root ->
                rules.find(root, WeworkSelectorIds.ADD_FRIEND_SEND_BUTTON)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "「发送」申请按钮缺失（已是好友=重复添加族？）",
            )
            CommonSteps.clickOrReject(driver, send, "「发送」申请")
        }

    private fun openContactDetailStep(locateKey: String): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-contact-detail",
            description = "在通讯录定位「$locateKey」并进入详情页",
        ) { driver ->
            val node = driver.currentRoot()
                ?.let { rules.findByText(it, locateKey) }
                ?: return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "通讯录未找到「$locateKey」（备注名通则：有备注必须用备注名）",
                )
            CommonSteps.clickOrReject(driver, node, "联系人「$locateKey」")
        }

    private fun writeMarkStep(intent: CommandIntent.AddFriendByPhone): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "write-mark",
            description = "在详情页写入备注/标签（markName=${intent.markName != null}, tags=${intent.tagList.size}）",
        ) { driver ->
            val mark = intent.markName ?: intent.tagList.joinToString(",").takeIf { it.isNotEmpty() }
            if (mark == null) {
                return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.PAYLOAD_INVALID,
                    "修改语义至少需要 markName/markExtra/tagList 之一",
                )
            }
            val input = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CONTACT_MARK_INPUT) }
                ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "备注输入框缺失")
            if (!driver.click(input) || !driver.inputText(input, mark)) {
                return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.ACTION_REJECTED, "写入备注被界面拒绝")
            }
            StepOutcome.Success
        }

    private fun confirmMarkStep(): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "confirm-mark",
            description = "点击确定保存备注修改",
        ) { driver ->
            val confirm = driver.currentRoot()
                ?.let { rules.find(it, WeworkSelectorIds.CONTACT_MARK_CONFIRM) }
                ?: return@PlanStep StepOutcome.Failure(ExecutorErrorCodes.NODE_NOT_FOUND, "备注确定按钮缺失")
            CommonSteps.clickOrReject(driver, confirm, "备注确定")
        }
}

/**
 * 指令 220 ADD_FRIEND_BY_GROUP 群内加好友（docs/10 B2，L1）：
 * 进入目标外部群 → 群设置成员列表 → 点击成员头像进资料页 →「添加好友」
 * （附言）→ 申请发出。配额/风控同 213（服务端治理）。
 */
class AddFriendByGroupExecutor(
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.AddFriendByGroup>() {

    override val commandType: Int get() = MessageTypes.ADD_FRIEND_BY_GROUP

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L1

    override fun buildTypedPlan(intent: CommandIntent.AddFriendByGroup): ExecutionPlan =
        ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "group=${intent.groupName}, member=${intent.memberName}, " +
                "markName=${intent.markName != null}, tags=${intent.tagList.size}, " +
                "leavingMsg.length=${intent.leavingMsg?.length ?: 0}",
            steps = listOf(
                CommonSteps.openConversation(rules, intent.groupName),
                CommonSteps.awaitChatReady(rules, timeouts),
                CommonSteps.openGroupSettings(rules, timeouts),
                openMemberDetailStep(intent),
                clickAddFriendStep(intent),
            ),
        )

    private fun openMemberDetailStep(intent: CommandIntent.AddFriendByGroup): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "open-member-detail",
            description = "在群设置成员列表点击成员「${intent.memberName}」进入资料页",
        ) { driver ->
            val member = driver.currentRoot()
                ?.let { rules.findByText(it, intent.memberName) }
                ?: return@PlanStep StepOutcome.Failure(
                    ExecutorErrorCodes.NODE_NOT_FOUND,
                    "群成员列表未见「${intent.memberName}」（不在本群或须用备注名）",
                )
            CommonSteps.clickOrReject(driver, member, "成员「${intent.memberName}」")
        }

    private fun clickAddFriendStep(intent: CommandIntent.AddFriendByGroup): cn.worktool.open.feature.executor.exec.PlanStep =
        cn.worktool.open.feature.executor.exec.PlanStep(
            id = "click-add-friend",
            description = "在资料页点击「添加好友」（附言长度=${intent.leavingMsg?.length ?: 0}）",
        ) { driver ->
            val add = driver.awaitNode(timeouts.awaitNodeMs, timeouts.pollMs, "「添加好友」按钮") { root ->
                rules.find(root, WeworkSelectorIds.CONTACT_ADD_FRIEND_BUTTON)
            } ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "「添加好友」按钮缺失（已是好友=重复添加族？）",
            )
            CommonSteps.clickOrReject(driver, add, "「添加好友」")
        }
}
