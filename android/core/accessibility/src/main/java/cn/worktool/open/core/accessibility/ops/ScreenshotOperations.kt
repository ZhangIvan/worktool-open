/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.ops

/**
 * 原子操作⑤：截图（T2.4）。
 *
 * 版本策略（降级如实标注）：
 * - API 30+（Android 11）：`AccessibilityService.takeScreenshot`（真机实现见
 *   `android/AccessibilityScreenshotApi`）。注意：XML 能力位 `android:canTakeScreenshot`
 *   自 API 31 引入；API 30 设备上方法存在但门禁行为未在真机确认，故 [ScreenshotApi.available]
 * 语义为「当前环境承诺可用」，由真机实现自判（API 30 先按可尝试处理，D1 首验时校准）。
 * - API < 30：无障碍通道无截图能力，稳定返回 [ScreenshotResult.UnsupportedPlatform]，
 *   不做任何降级拍摄（MediaProjection 录屏路属 FR-08/P2，另行评审）。
 *
 * 截图产物为内存 PNG 字节，不落盘、不外发（SR-03 零遥测 / SR-06 日志脱敏）。
 */
sealed class ScreenshotResult {
    /** 成功：PNG 字节 + 尺寸。 */
    data class Ok(
        val width: Int,
        val height: Int,
        val pngBytes: ByteArray,
    ) : ScreenshotResult()

    /** 当前平台/服务态不支持（API < 30 或服务未连接）。 */
    object UnsupportedPlatform : ScreenshotResult()

    /** 平台支持但执行失败（框架错误码 + 消息）。 */
    data class Failed(val errorCode: Int?, val message: String?) : ScreenshotResult()
}

/**
 * 截图能力通道。真机实现包装 `AccessibilityService.takeScreenshot`；
 * JVM 单测以 Fake 验证门控语义。
 */
interface ScreenshotApi {
    /** 当前环境是否承诺可截（false = 稳定走 UnsupportedPlatform）。 */
    val available: Boolean

    /** 异步执行；回调在实现方给定线程（真机实现为主线程）。 */
    fun take(callback: (ScreenshotResult) -> Unit)
}

/** 截图门控外壳：可用性收敛 + 统一降级语义（JVM 可测）。 */
class ScreenshotOperations(private val api: ScreenshotApi) {

    fun takeScreenshot(callback: (ScreenshotResult) -> Unit) {
        if (!api.available) {
            callback(ScreenshotResult.UnsupportedPlatform)
            return
        }
        api.take(callback)
    }
}
