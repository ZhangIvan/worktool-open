/*
 * core:protocol（T2.2）：WorkTool-Open 通讯协议层，纯 Kotlin/JVM 模块。
 *
 * 为什么是 JVM 模块而非 AGP 库：协议逻辑（信封编解码/加密/会话语义/投递队列）
 * 不依赖任何 Android API，纯 JVM 化使 CI 的 `gradle test` 直接在 JVM 上跑单测
 * （docs/03 T2.2 DoD：协议单测 ≥80% 分支 + hmac-vector 金标准复算），
 * Android 侧经 core:protocol-android / app 消费（OkHttp 与 kotlinx-serialization
 * 均为 Android 兼容依赖）。
 *
 * 职责（docs/02 §3/§5，规格 = protocol/spec.md v1.1.0）：
 * - v1 兼容信封编解码（WeworkMessageListBean 全字段，§4）
 * - v2 帧信封 + 逐帧鉴权（HMAC 输入式含 seq，AUD-03 C-9）+ AES-256-GCM（§5.2/§5.3）
 * - 配对请求/响应（Ed25519+X25519，Java Security API，§5.1）
 * - seq/eventId 管理与去重（§5.4）、AtLeastOnce 队列 + journal 语义（§5.4.5）
 * - WS 客户端（OkHttp）与断线指数退避重连（§3）
 *
 * 安全红线：SR-01 无 trust-all、SR-09 默认拒绝明文/裸 IP、G12 禁 hook/动态加载、
 * 零遥测（仅 OkHttp/kotlinx-serialization 两个运行时依赖）。
 */
plugins {
    alias(libs.plugins.kotlin.jvm)
    alias(libs.plugins.kotlin.serialization)
    jacoco
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    api(libs.kotlinx.serialization.json)
    api(libs.okhttp)

    testImplementation(libs.junit)
    testImplementation(libs.mockwebserver)
}

// T2.2 DoD：协议单测分支覆盖率 ≥80%（jacocoTestReport 产出
// build/reports/jacoco/test/jacocoTestReport.xml，BRANCH 计数器）。
tasks.jacocoTestReport {
    reports {
        xml.required.set(true)
        html.required.set(true)
    }
}
