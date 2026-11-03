/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutionPlan
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.TypedCommandExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.UiDriver
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import cn.worktool.open.feature.executor.runtime.WeworkSelectorIds

/**
 * 指令 501 GET_GROUP_INFO（FR-28；2026-11-04 会话 10 **对齐原版语义重写**）：
 * 原版 `WeworkGetImpl.getGroupInfo(selectList)` = 按**群名名单**逐群
 * 「消息列表定位会话 → 进入 → 读群信息」——非全量枚举（枚举=508，且其依赖的
 * 通讯录「群聊」入口在企微 5.0.x 已移除，原版同构建同样「未找到群聊入口」）。
 *
 * 名单项 = 消息列表会话标题（未命名群 = 成员名串，如「成员甲、成员乙、成员丙、成员丁」）。
 * 空名单 → 如实失败（NODE_NOT_FOUND + 语义说明），不虚构枚举结果。
 *
 * 只读指令（点击导航 + 文本读取）→ 风险级 L0（FR-53 裁决③）。
 * 结果边界：群显示名序（未命名群头=「群聊(N)」；富结构待 T3.2 冻结）。
 */
class GetGroupListExecutor(
    private val sink: QueryResultSink,
    private val rules: WeworkScreenRules = WeworkScreenRules(),
    private val timeouts: StepTimeouts = StepTimeouts(),
) : TypedCommandExecutor<CommandIntent.GetGroupList>() {

    override val commandType: Int get() = MessageTypes.GET_GROUP_INFO

    override fun riskLevel(intent: CommandIntent): RiskLevel = RiskLevel.L0

    override fun buildTypedPlan(intent: CommandIntent.GetGroupList): ExecutionPlan {
        val names = intent.groupNames
        val collected = mutableListOf<String>()

        if (names.isEmpty()) {
            return ExecutionPlan(
                commandType = commandType,
                riskLevel = riskLevel(intent),
                target = intent.targetDescription,
                paramsSummary = "selectList=空",
                steps = listOf(requireSelectListStep()),
            )
        }

        val bounded = names.take(CommandIntent.GetGroupList.MAX_GROUPS)
        val steps = mutableListOf<PlanStep>(CommonSteps.openMessageTab(rules))
        bounded.forEachIndexed { index, name ->
            steps += CommonSteps.openConversation(rules, name, stepId = "open-group-$index")
            steps += awaitGroupHeaderStep(index, name, collected)
            steps += CommonSteps.goBack(stepId = "back-to-list-$index", what = "返回消息列表（群 ${index + 1}/${bounded.size}）")
        }
        steps += emitResultStep(collected)

        return ExecutionPlan(
            commandType = commandType,
            riskLevel = riskLevel(intent),
            target = intent.targetDescription,
            paramsSummary = "groups=${bounded.size}",
            steps = steps,
        )
    }

    /** 空名单如实失败：原版空名单走群管理页枚举，该入口企微 5.0.x 已移除。 */
    private fun requireSelectListStep(): PlanStep = PlanStep(
        id = "require-select-list",
        description = "校验群名名单（selectList）",
    ) { _ ->
        StepOutcome.Failure(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            "selectList 为空：全量枚举依赖通讯录「群聊」入口，企微 5.0.x 已移除" +
                "（原版同构建同样「未找到群聊入口」）——请携带群名名单（消息列表会话标题）",
        )
    }

    /**
     * 等待聊天页就绪并读取群显示名：优先标题命中「群聊(N)」（未命名群头，
     * N=成员数）；命名群头=群名（与定位名一致的 TextView 兜底）。
     * 屏蔽条件：根上仍见「消息」底栏 Tab = 还在会话列表页（会话行文本与定位名
     * 同名，防假阳性——会话 10 JVM 夹具实证）。
     */
    private fun awaitGroupHeaderStep(index: Int, name: String, collected: MutableList<String>): PlanStep = PlanStep(
        id = "read-group-header-$index",
        description = "等待群「$name」聊天页就绪并读取群显示名",
    ) { driver ->
        val header = driver.awaitNode(
            timeouts.awaitNodeMs,
            timeouts.pollMs,
            "群「$name」聊天页标题",
        ) { root ->
            if (rules.find(root, WeworkSelectorIds.MESSAGE_TAB) != null) {
                null // 仍在消息列表页（会话行同名文本防误判）
            } else {
                findGroupHeader(root, name)
            }
        }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.STEP_TIMEOUT,
                "群「$name」聊天页标题未在 ${timeouts.awaitNodeMs}ms 内出现（会话未进入/非群聊？）",
            )
        header.text?.let { collected += it }
        StepOutcome.Success
    }

    /** 末步：结构化结果出端口（FAILED/PAUSED 路径不会到达 → 不外发部分结果）。 */
    private fun emitResultStep(collected: MutableList<String>): PlanStep = PlanStep(
        id = "emit-result",
        description = "输出结构化群列表结果至查询结果端口",
    ) { _ ->
        sink.onQueryResult(commandType, QueryResult.GroupList(collected.toList()))
        StepOutcome.Success
    }

    private companion object {
        /** 未命名群头形态（成员数 N≥2）。 */
        private val UNNAMED_GROUP_HEADER = Regex("群聊\\((\\d+)\\)")

        /** 全树先序找群头 TextView（「群聊(N)」或与定位名一致的文本）。 */
        fun findGroupHeader(root: UiNode, name: String): UiNode? {
            var matched: UiNode? = null
            fun walk(node: UiNode) {
                if (matched != null) return
                val text = node.text
                if (text != null && (UNNAMED_GROUP_HEADER.matches(text) || text == name)) {
                    matched = node
                    return
                }
                for (i in 0 until node.childCount) {
                    node.childAt(i)?.let(::walk)
                    if (matched != null) return
                }
            }
            walk(root)
            return matched
        }
    }
}
