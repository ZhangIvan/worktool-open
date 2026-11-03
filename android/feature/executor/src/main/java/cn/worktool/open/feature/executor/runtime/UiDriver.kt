/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.accessibility.tree.NodeAction
import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 界面驱动抽象（T3.1）：执行计划在 [UiNode] 抽象上运行的唯一通道。
 *
 * 这是「JVM 可测 ↔ 真机可替换」的接缝：
 * - JVM 单测：Fake 驱动（feature:executor src/test，企微界面 FakeNode 夹具）；
 * - 真机：T2.5 通道打通后由无障碍服务实现（包装 core:accessibility 的
 *   `AccessibilityNodeInfo` 根节点 + 原子操作库，W-M3R 接线，当前 NOT_RUN）。
 *
 * 实现约束：
 * - [currentRoot] 每次调用返回**当前界面快照**根节点（无障碍事件驱动或轮询取窗）；
 * - [awaitNode] 默认实现轮询 [currentRoot] + [sleep]，超时语义由 [nowMillis] 决定，
 *   测试实现可用假时钟确定性推进（不依赖真实时间）；
 * - 所有操作只经执行计划步骤调用，驱动自身不持有业务状态（纯状态机在计划侧）。
 */
interface UiDriver {

    /** 当前界面根节点快照；界面不可得（无窗/服务未连）返回 null。 */
    fun currentRoot(): UiNode?

    /**
     * 等待界面满足条件：轮询 [currentRoot]，[condition] 返回非 null 即视为满足并返回该节点；
     * 超时（[nowMillis] 越过 deadline）返回 null。
     *
     * @param timeoutMs 最长等待毫秒数
     * @param pollIntervalMs 轮询间隔（经 [sleep] 推进，真机=真实等待，Fake=假时钟推进）
     * @param what 等待目标描述（报告/日志用）
     */
    fun awaitNode(
        timeoutMs: Long,
        pollIntervalMs: Long,
        what: String,
        condition: (UiNode) -> UiNode?,
    ): UiNode? {
        val deadline = nowMillis() + timeoutMs
        while (true) {
            val root = currentRoot()
            if (root != null) {
                condition(root)?.let { return it }
            }
            if (nowMillis() >= deadline) return null
            sleep(pollIntervalMs)
        }
    }

    /** 点击节点（真机实现转发原子操作库 click，含手势回退）。 */
    fun click(node: UiNode): Boolean

    /**
     * 确保目标应用（企微）在前台且界面树可得（2026-11-03 会话 9 补位）：
     * 已在前台→true；否则拉起并轮询至就绪。返回 false=无法达成（服务未连接/
     * 企微未安装/超时）。JVM 默认 true（Fake 企微界面夹具假定就绪），真机由
     * [cn.worktool.open.app.AccessibilityUiDriver] 覆写——执行计划首步由此
     * 保障，不再隐含「企微已在前台」假设。
     */
    fun ensureTargetForeground(): Boolean = true

    /**
     * 最近一次 [ensureTargetForeground] 失败的具体原因（真机实现填充；
     * JVM 默认 null → 报告用通用文案）。执行报告 errorReason 透传，供
     * 服务端/取证区分「服务未连接/企微缺失/启动被拦/超时」分支。
     */
    val lastTargetForegroundFailure: String? get() = null

    /**
     * 长按节点（消息长按菜单：回复/转发/撤回/多选入口，204/205/226/227/228 用）。
     *
     * 默认实现直发 LONG_CLICK 节点动作（与 core:accessibility AtomicOperations.longClick
     * 的首选路径同源）；真机实现可覆写为转发原子操作库（含手势长按回退）。
     */
    fun longClick(node: UiNode): Boolean = node.performAction(NodeAction.LONG_CLICK, null)

    /** 在节点上输入文本（真机实现转发原子操作库 inputText，含剪贴板回退）。 */
    fun inputText(node: UiNode, text: String): Boolean

    /** 滚动容器节点。 */
    fun scroll(node: UiNode, direction: ScrollDirection): Boolean

    /** 返回上一级界面（真机=系统返回；Fake=出栈）。 */
    fun goBack(): Boolean

    /** 等待/睡眠指定毫秒（真机=真实等待；Fake=推进假时钟）。 */
    fun sleep(ms: Long)

    /** 单调毫秒时钟（超时判定唯一时间源）。 */
    fun nowMillis(): Long
}
