package io.worktool.open.prototype.policy

/** This source set has no alternate transport fallback. */
object VariantFactory {
    fun endpointPolicy():EndpointPolicy=HttpsEndpointPolicy()
    fun commandPolicy():CommandPolicy=StandardCommandPolicy
    val presentation=VariantPresentation(
        "SYNTHETIC ONLY · 无真实企微执行",
        "HTTPS endpoint（仅 origin，无路径、查询或凭据）",
        "输入自有控制面的 HTTPS origin。没有默认地址，不支持明文降级；配对后仍需手动恢复连接。",
        null
    )
}
