/*
 * core:protocol（T1.2 空占位，T2.2 落地实现）。
 * 职责（docs/02 §3）：WS 客户端、v1 兼容/v2 安全信封编解码、心跳重连、
 * AtLeastOnce 队列、离线暂存。规格与测试夹具 = 仓库 protocol/（T2.1 交付）。
 * 零第三方依赖。
 */
plugins {
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "cn.worktool.open.core.protocol"
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
