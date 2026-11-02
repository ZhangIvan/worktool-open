/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.PlanStep
import cn.worktool.open.feature.executor.exec.StepOutcome

/**
 * 分页列表读取器（501/502 查询执行器共用，docs/05「导航+分页读取→结构化结果」）。
 *
 * 收敛语义：**翻页后读取无新增项 = 已到末页**（按条目文本去重判定，与真机
 * 「滚动到底不再出新条目」一致；滚动动作返回 false 视同到末页——读操作无
 * 副作用，两种信号都按安全收敛处理）。
 *
 * 每页一步（`<stepPrefix>-N`，对齐 PassFriendExecutor 每成员一步的粒度口径）：
 * FR-52 暂停门在页边界重查；已收敛的后续页**空过**（零滚动零读取副作用语义，
 * 仅重读当前页确认无新增）。
 *
 * @param maxPages 翻页护栏（防界面异常（条目文本反复变化）导致的无界翻页；
 *                 超限 = VERIFY_FAILED「翻页未收敛」）
 */
internal class PaginatedListReader(
    private val rules: WeworkScreenRules,
    private val containerSelector: String,
    private val itemSelector: String,
    private val what: String,
) {

    /** 已收集条目（界面读取序，文本去重）。 */
    val collected = LinkedHashSet<String>()

    /** 是否已到末页（读取无新增 / 滚动被拒）。 */
    var converged = false
        private set

    /** 读取当前页：无新增 → 收敛。返回本页新增条数。 */
    fun readPage(root: UiNode?): Int {
        val items = root?.let { rules.findAll(it, itemSelector) } ?: emptyList()
        val texts = items.mapNotNull { it.text?.trim() }.filter { it.isNotEmpty() }
        val fresh = texts.filter { collected.add(it) }
        if (fresh.isEmpty()) converged = true
        return fresh.size
    }

    /** 铺 maxPages 个分页步骤 + 收敛校验步骤。 */
    fun pageSteps(maxPages: Int, stepPrefix: String): List<PlanStep> =
        (1..maxPages).map { index -> pageStep(index, stepPrefix) } + verifyStep(maxPages)

    private fun pageStep(index: Int, stepPrefix: String): PlanStep = PlanStep(
        id = "$stepPrefix-$index",
        description = "读取$what 第 $index 页（无新增条目=已到末页，后续页空过）",
    ) { driver ->
        if (converged) return@PlanStep StepOutcome.Success // 已收敛：空过（幂等重读也可，但零成本直接空过）
        readPage(driver.currentRoot())
        if (converged) return@PlanStep StepOutcome.Success
        val container = driver.currentRoot()
            ?.let { rules.find(it, containerSelector) }
            ?: return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.NODE_NOT_FOUND,
                "$what 滚动容器缺失（页面未就绪？）",
            )
        if (!driver.scroll(container, ScrollDirection.FORWARD)) {
            // 滚动被拒 = 已在末页（列表不响应前向滚动）；读操作无副作用，安全收敛
            converged = true
        }
        StepOutcome.Success
    }

    private fun verifyStep(maxPages: Int): PlanStep = PlanStep(
        id = "verify-converged",
        description = "校验翻页收敛（已达$what 末页；护栏 maxPages=$maxPages）",
    ) { _ ->
        if (!converged) {
            return@PlanStep StepOutcome.Failure(
                ExecutorErrorCodes.VERIFY_FAILED,
                "翻页未收敛（已达护栏上限 $maxPages 页，界面可能异常，已收集 ${collected.size} 项作废不外发）",
            )
        }
        StepOutcome.Success
    }
}
