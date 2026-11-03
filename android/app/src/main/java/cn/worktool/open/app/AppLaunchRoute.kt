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

    fun decide(paired: Boolean, accessibilityAuthorized: Boolean): Route =
        decide(paired, accessibilityAuthorized, skipAccessibilityGuide = false)

    /**
     * U7「不再询问」扩参（docs/16）：无障碍未授权**且**用户已记「不再询问」→
     * 不再强制 [Route.SETUP_GUIDE]（降级按配对态走 PAIRING/MAIN）。
     *
     * 安全边界：skip 只影响**路由强制**——主界面状态卡的无障碍警示行（CR UI-02）
     * 与指令执行失败提示不受影响（用户没开无障碍时指令必然失败，持续可见的警示
     * 承接「误关后失明」风险）；卡内实时检测同理保留。
     */
    fun decide(paired: Boolean, accessibilityAuthorized: Boolean, skipAccessibilityGuide: Boolean): Route =
        when {
            !accessibilityAuthorized && !skipAccessibilityGuide -> Route.SETUP_GUIDE
            !paired -> Route.PAIRING
            else -> Route.MAIN
        }

    /**
     * 引导页「完成」按钮去向（SP-3）：三卡全绿后——已配对 → 主界面（无需重复配对）；
     * 未配对 → 配对引导流（按钮文案「完成，去配对」）。
     */
    fun destinationAfterGuide(paired: Boolean): Route = if (paired) Route.MAIN else Route.PAIRING
}
