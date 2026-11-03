/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * CR Q4：POST_NOTIFICATIONS 请求门判定（[NotificationPermissionGate.needsRequest]）。
 * 语义：API 33+ 且未授予且本进程未请求过 → 才请求；拒绝后（已请求过）不重复骚扰。
 */
class NotificationPermissionGateTest {

    @Test
    fun `API33未授予未请求过-需要请求`() {
        assertTrue(NotificationPermissionGate.needsRequest(sdkInt = 33, granted = false, alreadyRequested = false))
        assertTrue(NotificationPermissionGate.needsRequest(sdkInt = 34, granted = false, alreadyRequested = false))
    }

    @Test
    fun `API低于33-无运行时通知权限-永不请求`() {
        assertFalse(NotificationPermissionGate.needsRequest(sdkInt = 26, granted = false, alreadyRequested = false))
        assertFalse(NotificationPermissionGate.needsRequest(sdkInt = 32, granted = false, alreadyRequested = false))
    }

    @Test
    fun `已授予-不请求`() {
        assertFalse(NotificationPermissionGate.needsRequest(sdkInt = 33, granted = true, alreadyRequested = false))
    }

    @Test
    fun `本进程已请求过-拒绝后不重复骚扰`() {
        // 引导页请求过（无论授予/拒绝）→ 主界面入口不再弹；反之亦然（共享 once 门）。
        assertFalse(NotificationPermissionGate.needsRequest(sdkInt = 33, granted = false, alreadyRequested = true))
    }

    @Test
    fun `已授予优先于已请求标记-两条件独立`() {
        assertFalse(NotificationPermissionGate.needsRequest(sdkInt = 33, granted = true, alreadyRequested = true))
    }
}
