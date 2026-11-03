/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.os.Handler
import cn.worktool.open.core.accessibility.rules.permissions.PermissionGrantWatcher

/**
 * [PermissionGrantWatcher.GrantScheduler] 的真机实现：主线程 [Handler] 延迟执行。
 *
 * FR-57 watcher 的拟人间隔（docs/12 铁律 4 ≥800ms）与事件去抖都落在本调度器上；
 * 单线程语义与 watcher 的线程契约一致（全部任务在主线程串行）。
 * JVM 单测不经此类（android 依赖），watcher 语义用测试内调度 Fake 覆盖。
 */
class HandlerGrantScheduler(private val handler: Handler) : PermissionGrantWatcher.GrantScheduler {

    override fun schedule(delayMillis: Long, task: Runnable) {
        handler.postDelayed(task, delayMillis.coerceAtLeast(0L))
    }
}
