/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.pm.PackageManager

/**
 * 企微（企业微信）版本兼容检测（docs/16 U6；纯 JVM 可测）。
 *
 * 兼容矩阵**单一事实源 = docs/05-SUPPORT-MATRIX.md §基线**；本对象内嵌只读副本
 * （版本变更走 docs/05 修订，不在此处扩界）：
 * - **绿（用户确认基线，2026-11-02）**：4.1.8~4.1.39 与 5.0.0~5.0.10；
 * - **黄（声明扩界，未验证 ⬜）**：APK 内置版本映射声明兼容 4.0.2~5.0.12（含
 *   4.0.x 全系与 5.0.11/5.0.12），以及 docs/05 实测设备列已开列的 5.0.13——
 *   进入矩阵但无用户确认基线，提示「未验证」；
 * - **红（不兼容）**：低于 4.0.2 或高于 5.0.13——显式提示「当前企微版本不在兼容
 *   范围，功能可能异常」；
 * - **灰（未安装）**：PackageManager 查不到 com.tencent.wework。
 *
 * 「下载推荐版」不采纳（docs/16 §优化-采纳 #9 只做检测展示）。
 */
object WecomCompatCheck {

    const val WECOM_PACKAGE = "com.tencent.wework"

    /** 用户确认基线（docs/05 §基线；含边界）。 */
    val BASELINE_RANGES: List<ClosedRange<Version>> = listOf(
        Version(4, 1, 8)..Version(4, 1, 39),
        Version(5, 0, 0)..Version(5, 0, 10),
    )

    /** 声明扩界下限（APK 内置映射 4.0.2 起；docs/05 加注 2026-11-03）。 */
    val DECLARED_MIN: Version = Version(4, 0, 2)

    /** 声明扩界上限（APK 内置映射至 5.0.12；docs/05 实测设备列另有 5.0.13）。 */
    val DECLARED_MAX: Version = Version(5, 0, 12)

    /** 矩阵已开列的最高实测版本（docs/05 实测设备列 5.0.13；进矩阵未翻绿）。 */
    val MATRIX_MAX: Version = Version(5, 0, 13)

    /** 检测结论（状态卡企微版本行渲染锚点）。 */
    enum class Verdict {
        /** 未安装企业微信（灰）。 */
        NOT_INSTALLED,

        /** 用户确认基线内（绿）。 */
        COMPATIBLE,

        /** 声明扩界/矩阵开列但无确认基线（黄 ⬜ 未验证）。 */
        UNVERIFIED,

        /** 兼容范围外（红：显式不兼容提示）。 */
        INCOMPATIBLE,

        /** 已安装但 versionName 不可解析（按未验证处理，附原始串）。 */
        UNKNOWN,
    }

    data class Result(val verdict: Verdict, val versionName: String?)

    /** 三段版本号（major.minor.patch；缺失段按 0，仅数字段参与比较）。 */
    data class Version(val major: Int, val minor: Int, val patch: Int) : Comparable<Version> {
        override fun compareTo(other: Version): Int = compareValuesBy(
            this, other,
            { it.major }, { it.minor }, { it.patch },
        )

        companion object {
            /** 解析 "4.1.8" / "5.0.13"（忽略非数字后缀如 "5.0.13.78"→取前三段）；null = 不可解析。 */
            fun parse(versionName: String?): Version? {
                if (versionName.isNullOrBlank()) return null
                val parts = versionName.trim().split('.')
                if (parts.isEmpty()) return null
                val nums = IntArray(3)
                for (i in 0 until 3) {
                    val seg = parts.getOrNull(i) ?: continue
                    val n = seg.takeWhile { it.isDigit() }.toIntOrNull() ?: return null
                    nums[i] = n
                }
                if (parts[0].takeWhile { it.isDigit() }.isEmpty()) return null
                return Version(nums[0], nums[1], nums[2])
            }
        }
    }

    /** 纯判定（versionName 来自 PackageManager；null = 未安装）。 */
    fun classify(versionName: String?): Result {
        if (versionName.isNullOrBlank()) return Result(Verdict.NOT_INSTALLED, null)
        val v = Version.parse(versionName) ?: return Result(Verdict.UNKNOWN, versionName)
        val verdict = when {
            BASELINE_RANGES.any { v in it } -> Verdict.COMPATIBLE
            v in DECLARED_MIN..MATRIX_MAX -> Verdict.UNVERIFIED
            else -> Verdict.INCOMPATIBLE
        }
        return Result(verdict, versionName)
    }

    /**
     * PackageManager 读取已安装企微 versionName（app 壳层 Android 面；测试经
     * [classify] 覆盖判定，本方法仅做包查询）。查不到包 = null（未安装）。
     */
    fun readInstalledVersion(pm: PackageManager): String? {
        return try {
            pm.getPackageInfo(WECOM_PACKAGE, 0).versionName
        } catch (_: Exception) {
            null
        }
    }
}
