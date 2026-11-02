/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * JVM 单测示例（T1.2 DoD：≥1 个 JVM 单测）。
 * 验证应用静态标识与 ADR-04（包名 cn.worktool.open，docs/00 §ADR）一致，
 * 防止后续重构漂移到上游包名。
 */
class AppInfoTest {

    @Test
    fun applicationIdMatchesAdr04() {
        assertEquals("cn.worktool.open", AppInfo.APPLICATION_ID)
    }

    @Test
    fun versionMatchesMilestoneBaseline() {
        assertEquals("0.1.0", AppInfo.VERSION)
    }
}
