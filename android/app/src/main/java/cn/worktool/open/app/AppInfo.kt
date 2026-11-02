/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

/**
 * 应用静态标识（T1.2）。
 * 包名遵循 ADR-04：`cn.worktool.open`（上游包名保留给上游，见 docs/00 §ADR）。
 */
object AppInfo {

    /** applicationId，与 AndroidManifest/build.gradle.kts 保持一致。 */
    const val APPLICATION_ID: String = "cn.worktool.open"

    /** 里程碑版本（docs/03 §4：v0.1.0 = M4）。 */
    const val VERSION: String = "0.1.0"
}
