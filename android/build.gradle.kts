/*
 * WorkTool-Open Android 根构建脚本（T1.2）。
 * 版本集中在 gradle/libs.versions.toml；依赖策略：零遥测（SR-03 / 门禁 G3），
 * 除 Kotlin 标准库与 JUnit 外不引入任何运行时依赖。
 */
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.android.library) apply false
    alias(libs.plugins.kotlin.android) apply false
}
