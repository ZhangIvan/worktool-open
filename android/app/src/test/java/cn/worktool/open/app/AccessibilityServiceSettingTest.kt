/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** BP-7/SP-3：enabled_accessibility_services 设置值解析（无障碍卡检测 + 路由同源判据）。 */
class AccessibilityServiceSettingTest {

    private val pkg = "cn.worktool.open"
    private val svc = "cn.worktool.open.core.accessibility.WorkToolAccessibilityService"

    @Test
    fun `全称ComponentName精确命中`() {
        val setting = "$pkg/$svc"
        assertTrue(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }

    @Test
    fun `短称flattenToShortString命中`() {
        // 部分 ROM 写 .Cls 短形态 → 归一为 pkg.cls 后比较
        val setting = "$pkg/.core.accessibility.WorkToolAccessibilityService"
        assertTrue(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }

    @Test
    fun `多项设置中含我方服务命中`() {
        val setting = "com.example.other/com.example.other.OtherSvc:$pkg/$svc"
        assertTrue(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }

    @Test
    fun `仅其他服务开启-未命中`() {
        val setting = "com.example.other/com.example.other.OtherSvc"
        assertFalse(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }

    @Test
    fun `同类名不同包-未命中`() {
        val setting = "com.evil.mirror/$svc"
        assertFalse(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }

    @Test
    fun `空值与畸形值-未命中`() {
        assertFalse(AccessibilityServiceSetting.containsService(null, pkg, svc))
        assertFalse(AccessibilityServiceSetting.containsService("", pkg, svc))
        assertFalse(AccessibilityServiceSetting.containsService("   ", pkg, svc))
        assertFalse(AccessibilityServiceSetting.containsService("$pkg", pkg, svc)) // 无 '/'
        assertFalse(AccessibilityServiceSetting.containsService("/$svc", pkg, svc)) // 空包名
    }

    @Test
    fun `包名或类名部分一致但不完整-未命中`() {
        // 前缀相同但类名不同（如调试变体服务）不误判
        val setting = "$pkg/cn.worktool.open.core.accessibility.OtherService"
        assertFalse(AccessibilityServiceSetting.containsService(setting, pkg, svc))
    }
}
