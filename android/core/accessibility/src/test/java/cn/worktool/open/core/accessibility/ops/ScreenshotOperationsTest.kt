/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.ops

import cn.worktool.open.core.accessibility.fake.FakeScreenshotApi
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * 截图原子操作门控语义单测（Fake ScreenshotApi）。
 *
 * 真机链路（AccessibilityScreenshotApi → takeScreenshot → Bitmap/PNG 转换）
 * 无法 JVM 覆盖，NOT_RUN 如实标注：D1 真机首验（T2.5 后）经调试页「截图自检」按钮执行。
 */
class ScreenshotOperationsTest {

    @Test
    fun unavailablePlatformDegradesStable() {
        val api = FakeScreenshotApi(available = false, result = ScreenshotResult.Ok(1, 1, ByteArray(0)))
        val ops = ScreenshotOperations(api)
        var got: ScreenshotResult? = null
        ops.takeScreenshot { got = it }
        assertEquals(ScreenshotResult.UnsupportedPlatform, got)
        assertEquals(0, api.takeCalls) // 不可用时根本不触碰底层通道
    }

    @Test
    fun availableDelegatesToApi() {
        val ok = ScreenshotResult.Ok(1080, 2400, ByteArray(8))
        val api = FakeScreenshotApi(available = true, result = ok)
        var got: ScreenshotResult? = null
        ScreenshotOperations(api).takeScreenshot { got = it }
        assertEquals(ok, got)
        assertEquals(1, api.takeCalls)
    }

    @Test
    fun apiFailurePassesThrough() {
        val failed = ScreenshotResult.Failed(4, "boom")
        val api = FakeScreenshotApi(available = true, result = failed)
        var got: ScreenshotResult? = null
        ScreenshotOperations(api).takeScreenshot { got = it }
        assertEquals(failed, got)
    }
}
