/*
 * core:accessibility（T1.2 骨架，T2.4 落地）。
 * 职责（docs/02 §3）：无障碍服务、节点定位规则引擎、原子操作（点击/长按/输入/滑动/截图）。
 * 零第三方运行时依赖（SR-03 / G3）；测试仅 JUnit。
 * 规则引擎与原子操作库为纯 Kotlin（JVM 单测）；android/ 子包为真机实现（androidTest 覆盖）。
 */
plugins {
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "cn.worktool.open.core.accessibility"
    compileSdk = 34

    defaultConfig {
        minSdk = 26
        // 框架自带 runner（android.test.InstrumentationTestRunner）：不引入 androidx.test 依赖，
        // androidTest 用 JUnit3 风格（junit:junit 自带 junit.framework 兼容层）。
        testInstrumentationRunner = "android.test.InstrumentationTestRunner"
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    testImplementation(libs.junit)
    androidTestImplementation(libs.junit)
}
