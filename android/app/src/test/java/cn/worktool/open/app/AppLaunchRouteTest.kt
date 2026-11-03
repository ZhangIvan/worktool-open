/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertEquals
import org.junit.Test

/** BP-1/SP-1 + BP-7/SP-3 启动路由判定（未授权无障碍→引导页→未配对→配对→已配对→主界面）。 */
class AppLaunchRouteTest {

    @Test
    fun `未授权无障碍且未配对-进设置引导页`() {
        assertEquals(
            AppLaunchRoute.Route.SETUP_GUIDE,
            AppLaunchRoute.decide(paired = false, accessibilityAuthorized = false),
        )
    }

    @Test
    fun `未授权无障碍但已配对-仍进设置引导页（无障碍前置优先）`() {
        assertEquals(
            AppLaunchRoute.Route.SETUP_GUIDE,
            AppLaunchRoute.decide(paired = true, accessibilityAuthorized = false),
        )
    }

    @Test
    fun `已授权无障碍未配对-进配对引导流`() {
        assertEquals(
            AppLaunchRoute.Route.PAIRING,
            AppLaunchRoute.decide(paired = false, accessibilityAuthorized = true),
        )
    }

    @Test
    fun `已授权无障碍已配对-进主界面`() {
        assertEquals(
            AppLaunchRoute.Route.MAIN,
            AppLaunchRoute.decide(paired = true, accessibilityAuthorized = true),
        )
    }

    // ---- 引导页放行去向 ----

    @Test
    fun `引导完成未配对-去配对`() {
        assertEquals(
            AppLaunchRoute.Route.PAIRING,
            AppLaunchRoute.destinationAfterGuide(paired = false),
        )
    }

    @Test
    fun `引导完成已配对-去主界面（不重复配对）`() {
        assertEquals(
            AppLaunchRoute.Route.MAIN,
            AppLaunchRoute.destinationAfterGuide(paired = true),
        )
    }
}
