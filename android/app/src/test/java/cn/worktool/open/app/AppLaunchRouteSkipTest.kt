/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.app.AppLaunchRoute.Route
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * U7 引导「不再询问」路由扩参测试（docs/16 U7；纯 JVM）。
 * 覆盖：未授权+未 skip → 仍强制引导 / 未授权+skip → 降级配对态 / 已配对+skip →
 * 主界面 / 已授权不受 skip 影响 / 旧两参签名兼容（默认 false）。
 */
class AppLaunchRouteSkipTest {

    @Test
    fun `未授权且未 skip-仍强制引导页`() {
        assertEquals(Route.SETUP_GUIDE, AppLaunchRoute.decide(paired = false, accessibilityAuthorized = false, skipAccessibilityGuide = false))
        assertEquals(Route.SETUP_GUIDE, AppLaunchRoute.decide(paired = true, accessibilityAuthorized = false, skipAccessibilityGuide = false))
    }

    @Test
    fun `未授权且已 skip-降级按配对态（不再强制引导）`() {
        assertEquals(Route.PAIRING, AppLaunchRoute.decide(false, false, skipAccessibilityGuide = true))
        assertEquals(Route.MAIN, AppLaunchRoute.decide(true, false, skipAccessibilityGuide = true))
    }

    @Test
    fun `已授权-skip 不改变判定`() {
        assertEquals(Route.MAIN, AppLaunchRoute.decide(true, true, skipAccessibilityGuide = true))
        assertEquals(Route.PAIRING, AppLaunchRoute.decide(false, true, skipAccessibilityGuide = true))
    }

    @Test
    fun `skip 只作用于无障碍卡-默认关闭时行为与旧版一致`() {
        assertEquals(Route.SETUP_GUIDE, AppLaunchRoute.decide(false, false))
        assertEquals(Route.MAIN, AppLaunchRoute.decide(true, true))
    }

    @Test
    fun `skip 后授权状态恢复-照常主界面`() {
        // skip 只是路由豁免；用户之后真正开启无障碍 → 主界面
        assertEquals(Route.MAIN, AppLaunchRoute.decide(true, true, skipAccessibilityGuide = true))
    }
}
