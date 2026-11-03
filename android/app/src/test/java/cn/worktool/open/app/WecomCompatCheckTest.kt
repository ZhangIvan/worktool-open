/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.app.WecomCompatCheck.Verdict
import cn.worktool.open.app.WecomCompatCheck.Version
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

/**
 * U6 企微版本兼容矩阵测试（docs/16 U6；矩阵源 = docs/05-SUPPORT-MATRIX.md §基线
 * 只读副本）。覆盖：用户确认基线（4.1.8~4.1.39 / 5.0.0~5.0.10）上下边界 /
 * 声明扩界（4.0.2~5.0.12 未验证黄）/ 实测列 5.0.13 黄 / 范围外红（<4.0.2、>5.0.13）/
 * 未安装灰 / 版本号解析（含后缀段、非法）。
 */
class WecomCompatCheckTest {

    @Test
    fun `用户确认基线-4系边界`() {
        assertEquals(Verdict.COMPATIBLE, WecomCompatCheck.classify("4.1.8").verdict)
        assertEquals(Verdict.COMPATIBLE, WecomCompatCheck.classify("4.1.39").verdict)
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("4.1.7").verdict) // 基线下沿外（声明扩界内）
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("4.1.40").verdict)
    }

    @Test
    fun `用户确认基线-5系边界`() {
        assertEquals(Verdict.COMPATIBLE, WecomCompatCheck.classify("5.0.0").verdict)
        assertEquals(Verdict.COMPATIBLE, WecomCompatCheck.classify("5.0.10").verdict)
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("5.0.11").verdict) // APK 声明扩界
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("5.0.12").verdict) // APK 声明扩界上界
    }

    @Test
    fun `声明扩界下界 4_0_2 与更老版本红`() {
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("4.0.2").verdict) // 扩界下界（黄）
        assertEquals(Verdict.INCOMPATIBLE, WecomCompatCheck.classify("4.0.1").verdict)
        assertEquals(Verdict.INCOMPATIBLE, WecomCompatCheck.classify("3.1.0").verdict)
    }

    @Test
    fun `docs05 实测列 5_0_13 黄-更上新版本红`() {
        assertEquals(Verdict.UNVERIFIED, WecomCompatCheck.classify("5.0.13").verdict) // 实测设备列（矩阵开列未翻绿）
        assertEquals(Verdict.INCOMPATIBLE, WecomCompatCheck.classify("5.0.14").verdict)
        assertEquals(Verdict.INCOMPATIBLE, WecomCompatCheck.classify("6.0.0").verdict)
    }

    @Test
    fun `未安装-null 与空串`() {
        assertEquals(Verdict.NOT_INSTALLED, WecomCompatCheck.classify(null).verdict)
        assertEquals(Verdict.NOT_INSTALLED, WecomCompatCheck.classify("").verdict)
        assertEquals(Verdict.NOT_INSTALLED, WecomCompatCheck.classify("  ").verdict)
    }

    @Test
    fun `版本解析-数字段与后缀`() {
        assertEquals(Version(5, 0, 13), Version.parse("5.0.13"))
        assertEquals(Version(4, 1, 8), Version.parse("4.1.8"))
        assertEquals(Version(4, 1, 39), Version.parse(" 4.1.39 ")) // 容忍空白
        assertNull(Version.parse("unknown"))
        assertNull(Version.parse(null))
        assertNull(Version.parse("x.y.z"))
    }

    @Test
    fun `矩阵常量与 docs05 基线一致`() {
        // 内嵌副本与 docs/05 §基线一致性的编译期锚点（版本变更走 docs/05 修订）
        assertEquals(Version(4, 1, 8), WecomCompatCheck.BASELINE_RANGES[0].start)
        assertEquals(Version(4, 1, 39), WecomCompatCheck.BASELINE_RANGES[0].endInclusive)
        assertEquals(Version(5, 0, 0), WecomCompatCheck.BASELINE_RANGES[1].start)
        assertEquals(Version(5, 0, 10), WecomCompatCheck.BASELINE_RANGES[1].endInclusive)
        assertEquals(Version(4, 0, 2), WecomCompatCheck.DECLARED_MIN)
        assertEquals(Version(5, 0, 12), WecomCompatCheck.DECLARED_MAX)
        assertEquals(Version(5, 0, 13), WecomCompatCheck.MATRIX_MAX)
    }
}
