/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility

import android.accessibilityservice.AccessibilityService
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo
import cn.worktool.open.core.accessibility.android.AccessibilityScreenshotApi
import cn.worktool.open.core.accessibility.android.AndroidUiNode
import cn.worktool.open.core.accessibility.android.HandlerGrantScheduler
import cn.worktool.open.core.accessibility.android.ServiceGestureDispatcher
import cn.worktool.open.core.accessibility.android.SystemClipboardGateway
import cn.worktool.open.core.accessibility.event.AccessibilityEventSink
import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.core.accessibility.ops.AtomicOperations
import cn.worktool.open.core.accessibility.ops.ScreenshotOperations
import cn.worktool.open.core.accessibility.rules.SelectorEngine
import cn.worktool.open.core.accessibility.rules.permissions.PermissionGrantConfig
import cn.worktool.open.core.accessibility.rules.permissions.PermissionGrantWatcher
import cn.worktool.open.core.accessibility.tree.UiNode
import java.util.concurrent.CopyOnWriteArrayList

/**
 * 无障碍服务（T2.4：从 T1.2 骨架升级为可执行通道）。
 *
 * 能力声明见 res/xml/accessibility_service_config.xml（SR-10 最小化 + G10 diff 审查项）：
 * - canRetrieveWindowContent=true：节点树可见（规则引擎/调试页 dump 前提）
 * - canPerformGestures=true：点击/长按的手势回退通道（docs/02 §3 原子操作）
 * - canTakeScreenshot=true：截图原子操作（API 31+ 生效，API 30 真机待校准）
 * - flagReportViewIds：resourceId 选择器前提
 * - 事件仅订阅 typeWindowStateChanged|typeWindowContentChanged（骨架最小集）
 * - 不设 packageNames（不限定监听应用——调试页需可 dump 任意前台应用以校准规则）
 *
 * 服务实例经 [instance] 暴露给调试页（app 模块）；指令执行器（T3.1）经
 * [atomicOperations]/[screenshotOperations]/[selectorEngine] 使用能力。
 */
class WorkToolAccessibilityService : AccessibilityService() {

    private val eventSinks = CopyOnWriteArrayList<AccessibilityEventSink>()

    /**
     * FR-57 权限弹窗自动授权 watcher（docs/01 FR-57；docs/14 BP-6/SP-4）。
     * onServiceConnected 注册为事件 sink；识别 4 类企微权限弹窗并按三级授权流自动点允许
     * （docs/12 铁律 2/4 内建：两连败放弃 MANUAL_REQUIRED + 相邻动作 ≥800ms 拟人间隔；
     * SR-06：审计行只含类型/级别/结果码，无弹窗正文）。
     */
    var permissionGrantWatcher: PermissionGrantWatcher? = null
        private set

    /** 原子操作库（click/longClick/scroll/inputText；screenshot 见 [screenshotOperations]）。 */
    val atomicOperations: AtomicOperations by lazy {
        AtomicOperations(
            gestures = ServiceGestureDispatcher(this),
            clipboard = SystemClipboardGateway(applicationContext),
        )
    }

    /** 原子操作⑤：截图（API<30 稳定降级 UnsupportedPlatform）。 */
    val screenshotOperations: ScreenshotOperations by lazy {
        ScreenshotOperations(AccessibilityScreenshotApi({ this }))
    }

    /** 规则引擎（无状态，可复用）。 */
    val selectorEngine: SelectorEngine = SelectorEngine()

    override fun onServiceConnected() {
        super.onServiceConnected()
        instance = this
        registerPermissionGrantWatcher()
    }

    /**
     * 注册 FR-57 watcher（服务连接即生效；识别/授权语义在纯 Kotlin 的
     * `rules/permissions/PermissionGrantWatcher`，本方法只做 android 侧装配）。
     */
    private fun registerPermissionGrantWatcher() {
        val watcher = PermissionGrantWatcher(
            config = PermissionGrantConfig(),
            rootsProvider = {
                // 多窗口观测（弹窗可能是独立窗口层）；flag 缺失 windows=null 时回退活动窗口根。
                val roots = interactiveRoots().map(::AndroidUiNode)
                if (roots.isEmpty()) {
                    rootInActiveWindow?.let { listOf(AndroidUiNode(it)) } ?: emptyList()
                } else {
                    roots
                }
            },
            operations = atomicOperations,
            scheduler = HandlerGrantScheduler(Handler(Looper.getMainLooper())),
            audit = { line ->
                // SR-06：watcher 保证行内只有类型/级别/结果码（无窗口文本/弹窗正文）。
                Log.i(PERMISSION_GRANT_LOG_TAG, line)
            },
        )
        permissionGrantWatcher = watcher
        addEventSink(watcher)
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        // 骨架：快照化转发给已注册 sink；无人注册时零处理（事件量可能极大，
        // T3.1 FR-01 落地时按包名/类型在配置中收敛订阅面）。
        if (event == null || eventSinks.isEmpty()) return
        val snapshot = AccessibilityEventSnapshot(
            eventType = event.eventType,
            packageName = event.packageName?.toString(),
            className = event.className?.toString(),
            texts = event.text?.map { it.toString() } ?: emptyList(),
            eventTimeMillis = event.eventTime,
        )
        eventSinks.forEach { it.onAccessibilityEvent(snapshot) }
    }

    override fun onInterrupt() {
        // 无反馈通道需要中断。
    }

    override fun onDestroy() {
        super.onDestroy()
        if (instance === this) instance = null
        permissionGrantWatcher = null
        eventSinks.clear()
    }

    fun addEventSink(sink: AccessibilityEventSink) {
        eventSinks += sink
    }

    fun removeEventSink(sink: AccessibilityEventSink) {
        eventSinks -= sink
    }

    /** 当前活动窗口根节点（多窗口遍历 flagRetrieveInteractiveWindows 待 T3.x 需要时再放开）。 */
    fun rootNode(): UiNode? = rootInActiveWindow?.let(::AndroidUiNode)

    /**
     * 全部交互窗口根节点快照（2026-11-03 会话 2 观测面缺陷修复：rootInActiveWindow
     * 只观测活动窗口；flagRetrieveInteractiveWindows 已在服务能力位开启，此处消费）。
     * 供 UiDriver 按包名挑目标窗口；单窗口设备返回与 rootNode() 等价。
     */
    fun interactiveRoots(): List<AccessibilityNodeInfo> {
        val out = mutableListOf<AccessibilityNodeInfo>()
        val windows = windows ?: return out
        for (w in windows) {
            w.root?.let { out += it }
        }
        return out
    }

    companion object {
        /** FR-57 watcher 审计日志 tag（SR-06：内容由 watcher 保证只含结果码）。 */
        private const val PERMISSION_GRANT_LOG_TAG = "PermGrantWatch"

        /** 已连接服务实例；未启用/已销毁为 null。@Volatile 供跨线程读（调试页主线程读）。 */
        @Volatile
        var instance: WorkToolAccessibilityService? = null
            private set
    }
}
