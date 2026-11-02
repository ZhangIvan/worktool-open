/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.accessibilityservice.AccessibilityService
import android.graphics.Bitmap
import android.os.Build
import android.view.Display
import cn.worktool.open.core.accessibility.ops.ScreenshotApi
import cn.worktool.open.core.accessibility.ops.ScreenshotResult
import java.io.ByteArrayOutputStream
import java.util.concurrent.Executor

/**
 * [ScreenshotApi] 真机实现（T2.4 原子操作⑤）。
 *
 * 版本门控：API 30+（R）走 `AccessibilityService.takeScreenshot`；
 * API < 30 稳定 [ScreenshotResult.UnsupportedPlatform]（无障碍通道无截图能力，
 * 降级路径 MediaProjection 属 FR-08/P2 另行评审——docs/03 T2.4 低版本降级标注）。
 *
 * API 30 vs 31 细节（如实标注）：`android:canTakeScreenshot` 能力位属性 API 31 起在
 * AccessibilityServiceInfo 生效；API 30 设备上 takeScreenshot 方法可用但门禁行为未在
 * 真机确认（D1 真机首验时校准 available 判定，见 docs/05 支持矩阵 NOT_RUN 登记）。
 *
 * 本类含 Bitmap/HardwareBuffer 转换，无法在 JVM 单测覆盖（android.jar 桩），
 * 门控语义由 ScreenshotOperationsTest 以 Fake ScreenshotApi 验证。
 */
class AccessibilityScreenshotApi(
    private val serviceRef: () -> AccessibilityService?,
    private val sdkInt: Int = Build.VERSION.SDK_INT,
) : ScreenshotApi {

    override val available: Boolean
        get() = sdkInt >= Build.VERSION_CODES.R && serviceRef() != null

    override fun take(callback: (ScreenshotResult) -> Unit) {
        val service = serviceRef()
        if (sdkInt < Build.VERSION_CODES.R || service == null) {
            callback(ScreenshotResult.UnsupportedPlatform)
            return
        }
        takeOnRPlus(service, DirectExecutor(), callback)
    }

    private fun takeOnRPlus(
        service: AccessibilityService,
        executor: Executor,
        callback: (ScreenshotResult) -> Unit,
    ) {
        // API 30 签名：takeScreenshot(int displayId, Executor, TakeScreenshotCallback)
        service.takeScreenshot(
            Display.DEFAULT_DISPLAY,
            executor,
            object : AccessibilityService.TakeScreenshotCallback {
                override fun onSuccess(screenshot: AccessibilityService.ScreenshotResult) {
                    callback(convert(screenshot))
                }

                override fun onFailure(errorCode: Int) {
                    callback(ScreenshotResult.Failed(errorCode, "takeScreenshot onFailure"))
                }
            },
        )
    }

    private fun convert(result: AccessibilityService.ScreenshotResult): ScreenshotResult {
        val buffer = result.hardwareBuffer
        return try {
            val bitmap = Bitmap.wrapHardwareBuffer(buffer, result.colorSpace)
                ?.copy(Bitmap.Config.ARGB_8888, false)
            if (bitmap == null) {
                ScreenshotResult.Failed(null, "wrapHardwareBuffer returned null")
            } else {
                val out = ByteArrayOutputStream()
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)
                ScreenshotResult.Ok(bitmap.width, bitmap.height, out.toByteArray())
            }
        } catch (e: Exception) {
            ScreenshotResult.Failed(null, "convert failed: ${e.message}")
        } finally {
            buffer.close()
        }
    }

    private class DirectExecutor : Executor {
        override fun execute(command: Runnable) {
            command.run()
        }
    }
}
