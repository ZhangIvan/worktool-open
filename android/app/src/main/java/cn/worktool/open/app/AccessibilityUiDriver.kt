/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.view.accessibility.AccessibilityNodeInfo
import cn.worktool.open.core.accessibility.WorkToolAccessibilityService
import cn.worktool.open.core.accessibility.android.AndroidUiNode
import cn.worktool.open.core.accessibility.ops.AtomicOperations
import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.feature.executor.runtime.UiDriver

/**
 * 真机 UiDriver（T3.4 执行回路接线）：把 T2.4 无障碍通道（服务根节点 + 原子操作库）
 * 适配为 T3.1 [UiDriver] 接缝——W-M3R 缺失的「真机可替换」实现。
 *
 * - [currentRoot]：服务 rootInActiveWindow 快照（界面不可得/服务未连接 → null，
 *   计划首步 NODE_NOT_FOUND 失败路径，与 JVM Fake 同语义）；
 * - click/inputText/scroll：原子操作库（含手势/剪贴板回退，docs/02 §3）；
 * - goBack：GLOBAL_ACTION_BACK；
 * - sleep/nowMillis：真实时钟（超时语义归 [UiDriver.awaitNode] 轮询循环）。
 *
 * 服务未连接（用户未授权无障碍）时一切界面操作返回 null/false——执行计划按步失败
 * 上报错误码，不抛异常（崩溃面收敛）。
 */
class AccessibilityUiDriver : UiDriver {

    private val service: WorkToolAccessibilityService?
        get() = WorkToolAccessibilityService.instance

    /** 原子操作库按服务实例懒建（服务重连后重建；gestures/clipboard 网关随之刷新）。 */
    private var opsHolder: AtomicOperations? = null
    private var opsService: WorkToolAccessibilityService? = null

    private fun ops(): AtomicOperations? {
        val s = service ?: return null
        if (opsHolder == null || opsService !== s) {
            opsHolder = s.atomicOperations
            opsService = s
        }
        return opsHolder
    }

    override fun currentRoot(): UiNode? {
        val root: AccessibilityNodeInfo? = service?.rootInActiveWindow ?: return null
        return root?.let { AndroidUiNode(it) }
    }

    override fun click(node: UiNode): Boolean =
        ops()?.click(node) is cn.worktool.open.core.accessibility.ops.OpResult.Ok

    override fun inputText(node: UiNode, text: String): Boolean =
        ops()?.inputText(node, text) is cn.worktool.open.core.accessibility.ops.OpResult.Ok

    override fun scroll(node: UiNode, direction: ScrollDirection): Boolean =
        ops()?.scroll(node, direction) is cn.worktool.open.core.accessibility.ops.OpResult.Ok

    override fun goBack(): Boolean =
        service?.performGlobalAction(AccessibilityServiceHelper.GLOBAL_ACTION_BACK) == true

    override fun sleep(ms: Long) {
        try {
            Thread.sleep(ms)
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }

    override fun nowMillis(): Long = System.currentTimeMillis()
}

/** 框架常量隔离（AccessibilityService.GLOBAL_ACTION_BACK；防直接依赖 android 类阻塞 JVM 测试引用本文件符号表）。 */
internal object AccessibilityServiceHelper {
    const val GLOBAL_ACTION_BACK: Int = android.accessibilityservice.AccessibilityService.GLOBAL_ACTION_BACK
}
