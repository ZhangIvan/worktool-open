/*
 * WorkTool-Open Android 根构建脚本（T1.2 + T2.2）。
 * 版本集中在 gradle/libs.versions.toml；依赖策略：零遥测（SR-03 / 门禁 G3），
 * 运行时依赖仅 Kotlin 标准库系 + OkHttp（网络）+ kotlinx-serialization（JSON，
 * docs/03 T2.2 指定）；测试依赖 JUnit4 / MockWebServer。统计/埋点 SDK 一律禁止。
 */
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.android.library) apply false
    alias(libs.plugins.kotlin.android) apply false
    alias(libs.plugins.kotlin.jvm) apply false
    alias(libs.plugins.kotlin.serialization) apply false
}
