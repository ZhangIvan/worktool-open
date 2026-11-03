/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

/**
 * 启动路由判定（BP-1/SP-1 + BP-7/SP-3 接线，纯 JVM 可测）。
 *
 * 判定顺序（ask ⑥ 口径）：**未授权无障碍 → [Route.SETUP_GUIDE] → 未配对 →
 * [Route.PAIRING] → 已配对 → [Route.MAIN]（主界面 + 自动 start）**。
 *
 * 无障碍前置优先于配对：指令执行通道（AccessibilityUiDriver）依赖无障碍服务，
 * 没有它「配对成功」也只是离线空壳（docs/14 BP-3/会话 2 实证 201001），故未授权
 * 时一律先进引导页（三卡全绿后才放行去配对/主界面）。
 *
 * 「已配对」判据 = OnboardingForms.launchRoute == MAIN（有效凭据 + 可用 server
 * 地址）；「无障碍已授权」判据 = AccessibilityServiceGate.isAuthorized（系统设置
 * enabled_accessibility_services 含我方服务，见该类注）。
 */
object AppLaunchRoute {

    enum class Route {
        /** 系统设置引导页（无障碍/电池豁免/MIUI 自启动三卡，SP-3）。 */
        SETUP_GUIDE,

        /** 配对引导流（输入 server 地址 + 6 位配对码）。 */
        PAIRING,

        /** 主界面（已配对；onStart 自动恢复会话）。 */
        MAIN,
    }

    fun decide(paired: Boolean, accessibilityAuthorized: Boolean): Route = when {
        !accessibilityAuthorized -> Route.SETUP_GUIDE
        !paired -> Route.PAIRING
        else -> Route.MAIN
    }

    /**
     * 引导页「完成」按钮去向（SP-3）：三卡全绿后——已配对 → 主界面（无需重复配对）；
     * 未配对 → 配对引导流（按钮文案「完成，去配对」）。
     */
    fun destinationAfterGuide(paired: Boolean): Route = if (paired) Route.MAIN else Route.PAIRING
}
