/*
 * core:accessibility（T1.2 骨架，T2.4 落地原子操作库）。
 * 职责（docs/02 §3）：无障碍服务、节点定位规则引擎、原子操作（点击/长按/输入/滑动/截图）。
 * 零第三方依赖。
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
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}
