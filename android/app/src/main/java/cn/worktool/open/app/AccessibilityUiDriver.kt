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
class AccessibilityUiDriver(
    /**
     * 前台 Activity 上下文提供者（会话 9 系列收口：MIUI 对**服务上下文**的
     * startActivity 按 BAL 间歇拦截（MIUIOP 10021 rejectTime 与尝试时刻吻合），
     * Activity 上下文恒放行——对齐原 APK「前台 UI 发起跳转」模型）。装配层
     * （Lab/主界面运行时）持有前台 Activity 时注入；null = 仅服务上下文路径。
     */
    private val foregroundActivityContext: (() -> android.content.Context?)? = null,
) : UiDriver {

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
        val s = service ?: return null
        // 观测面（2026-11-04 会话 9l 修正）：企微多窗（主活动窗 + 半屏 Panel 窗）时
        // interactiveRoots 的**第一个**企微窗口可能是 Panel（内容树不含列表容器，
        // 502 实证「滚动容器缺失」）。优先 rootInActiveWindow：包名命中目标即用
        // （= 真正聚焦的活动页）；未命中再扫交互窗口；最后回退活动窗根。
        val active: AccessibilityNodeInfo? = s.rootInActiveWindow
        if (active?.packageName == WETARGET_PACKAGE) return AndroidUiNode(active)
        s.interactiveRoots().firstOrNull { it.packageName == WETARGET_PACKAGE }
            ?.let { return AndroidUiNode(it) }
        return active?.let { AndroidUiNode(it) }
    }

    /**
     * 拉起企微到前台并等待其界面树可得（2026-11-03 会话 9 补位，对齐原 APK
     * 「收到指令拉起企微」行为）：已在前台→立即 true；否则经服务上下文
     * startActivity（NEW_TASK）拉起 launcher intent，轮询 ≤10s 等 wework 窗口。
     * 失败分支原因写入 [lastForegroundFailure]（执行报告 errorReason 用——
     * 会话 9c 实证 timeCost=31ms 秒退需分支级定位，不再笼统）。
     */
    override fun ensureTargetForeground(): Boolean {
        val s = service ?: run {
            android.util.Log.w("WTLaunch", "ensureTargetForeground: service instance=null")
            lastForegroundFailure = "无障碍服务未连接（instance=null，绑定被系统回收/未完成？）"
            return false
        }
        if (hasTargetRoot()) return true
        // 诊断面包屑（会话 9h：企微已前台但 hasTargetRoot 20s 不中——定位 windows/root 判定）
        runCatching {
            val wins = s.interactiveRoots()
            android.util.Log.i(
                "WTLaunch",
                "pre-launch windows=${wins.size} pkgs=" +
                    s.windows?.mapNotNull { it.root?.packageName ?: "<nullroot:${it.title ?: "?"}>" }?.joinToString(","),
            )
        }
        val pm = try {
            s.packageManager
        } catch (e: Exception) {
            lastForegroundFailure = "PackageManager 不可得: ${e.javaClass.simpleName}"
            return false
        }
        val intent = try {
            pm.getLaunchIntentForPackage(WETARGET_PACKAGE)
        } catch (_: Exception) {
            null
        } ?: android.content.Intent(android.content.Intent.ACTION_MAIN).apply {
            addCategory(android.content.Intent.CATEGORY_LAUNCHER)
            setPackage(WETARGET_PACKAGE)
            component = android.content.ComponentName(WETARGET_PACKAGE, "$WETARGET_PACKAGE.launch.LaunchSplashActivity")
        }
        intent.addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        return try {
            // 拉起优先级（会话 9 收口）：前台 Activity 上下文（MIUI 恒放行）→
            // 服务上下文 → application 上下文；任一未抛即视为已发出。
            var launched = false
            val actx = foregroundActivityContext?.invoke()
            if (actx != null) launched = runCatching { actx.startActivity(intent) }.isSuccess
            if (!launched) launched = runCatching { s.startActivity(intent) }.isSuccess
            if (!launched) launched = runCatching { s.applicationContext.startActivity(intent) }.isSuccess
            if (!launched) {
                lastForegroundFailure = "三种上下文拉起企微均异常"
                android.util.Log.w("WTLaunch", "拉起企微：三种上下文全部异常")
                return false
            }
            if (awaitTargetRoot()) return true
            lastForegroundFailure = "拉起已发出但 ${LAUNCH_WAIT_MS}ms 内企微窗口未就绪"
            awaitTargetRoot()
        } catch (e: InterruptedException) {
            Thread.currentThread().interrupt()
            lastForegroundFailure = "等待企微就绪被中断"
            false
        } catch (e: Exception) {
            lastForegroundFailure = "startActivity 异常: ${e.javaClass.simpleName}: ${e.message?.take(80)}"
            false
        }
    }

    /** 轮询等企微窗口出现（≤[LAUNCH_WAIT_MS]）。 */
    private fun awaitTargetRoot(): Boolean {
        val deadline = System.currentTimeMillis() + LAUNCH_WAIT_MS
        var ticks = 0
        while (System.currentTimeMillis() < deadline) {
            if (hasTargetRoot()) {
                android.util.Log.i("WTLaunch", "目标就绪 poll#$ticks")
                return true
            }
            if (++ticks % 5 == 0) runCatching {
                val s = service
                android.util.Log.i(
                    "WTLaunch",
                    "poll#$ticks windows=${s?.windows?.size} roots=${s?.interactiveRoots()?.size} " +
                        "activeRoot=${s?.rootInActiveWindow?.packageName}",
                )
            }
            Thread.sleep(LAUNCH_POLL_MS)
        }
        return hasTargetRoot()
    }

    /**
     * 目标窗口可得判定（会话 9i 实证修复：服务 windows 列表恒空——flag 未生效时
     * interactiveRoots 全空，即使目标应用已前台）。**回退 rootInActiveWindow**：
     * 与 [currentRoot] 的回退语义一致——目标在前台时活动窗口根即目标根。
     */
    private fun hasTargetRoot(): Boolean {
        val s = service ?: return false
        if (s.interactiveRoots().any { it.packageName == WETARGET_PACKAGE }) return true
        return s.rootInActiveWindow?.packageName == WETARGET_PACKAGE
    }

    /** 最近一次 ensureTargetForeground 失败原因（执行报告 errorReason 透传）。 */
    private var lastForegroundFailure: String? = null

    override val lastTargetForegroundFailure: String?
        get() = lastForegroundFailure

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

/** 企微目标包名 + 拉起等待参数（ensureTargetForeground 用，集中一处防散落）。 */
internal const val WETARGET_PACKAGE: String = "com.tencent.wework"
internal const val LAUNCH_WAIT_MS: Long = 10_000
internal const val LAUNCH_POLL_MS: Long = 400
