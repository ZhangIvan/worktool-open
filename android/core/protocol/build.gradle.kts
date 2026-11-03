import java.security.MessageDigest

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
 * 零遥测（仅 OkHttp/kotlinx-serialization 两个运行时依赖 + T2.6 唯一加密回退
 * 依赖 net.i2p.crypto:eddsa，见下方钉版注记）。
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

// ---- T2.6（W-M4，裁决 dwfq-f63d4173-1：对 T2.2「非加密库堆叠」的定点豁免）----
// Ed25519 纯 JVM 回退实现（net.i2p.crypto:eddsa）：API 24~32 平台无 Ed25519
// JCA 路径（W-M3R D1 实证），spec §5.1.2 配对必填签名公钥不可生成 → 引入
// 唯一钉版本的回退库，仅经 Ed25519Keys 在 JCA 不可用时使用（JCA 优先不变）。
// 声明在本模块内（车道 android/core/protocol/**）而非根版本目录：
// 版本+sha256+零传递约束集中一处，供 AUD-07 复核依赖面（docs/03 T2.6 DoD）。
// sha256 于 2026-11-03 自 Maven Central 下载工件本地计算，并与 Central 发布的
// sha1（1901c8d4d8bffb7d79027686cfb91e704217c3e1）交叉核对。
val ED25519_FALLBACK_GAV = "net.i2p.crypto:eddsa:0.3.0"
val ED25519_FALLBACK_SHA256 =
    "4dda1120db856640dbec04140ed23242215a075fe127bdefa0dcfa29fb31267d"

dependencies {
    api(libs.kotlinx.serialization.json)
    api(libs.okhttp)
    implementation(ED25519_FALLBACK_GAV)

    testImplementation(libs.junit)
    testImplementation(libs.mockwebserver)
}

/**
 * T2.6 依赖面门禁（AUD-07 机械自证）：
 * 1. 解析 [ED25519_FALLBACK_GAV]，工件 sha256 必须等于钉死值（漂移/投毒即构建失败）；
 * 2. 断言解析结果恰好 1 个工件 —— eddsa 零传递依赖（POM 无 compile 依赖），
 *    引入它不扩张依赖面；若未来版本带入传递依赖，此任务失败并要求白名单评审。
 * 挂接 test/check：CI `gradle assembleDebug test` 与本地 `gradle test` 均强制执行。
 */
val pinEd25519Fallback by tasks.registering {
    group = "verification"
    description = "T2.6：eddsa 回退依赖钉版本+sha256+零传递面断言"
    doLast {
        val resolved = configurations.detachedConfiguration(
            dependencies.create(ED25519_FALLBACK_GAV),
        ).resolve()
        require(resolved.size == 1) {
            "eddsa 依赖面漂移：期望 0 个传递依赖（1 个工件），实际解析 ${resolved.size} 个：" +
                resolved.joinToString(", ") { it.name } + "（如需引入须先过 AUD 白名单评审）"
        }
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(resolved.single().readBytes())
            .joinToString("") { "%02x".format(it) }
        require(digest == ED25519_FALLBACK_SHA256) {
            "eddsa 工件 sha256 不符（钉死 $ED25519_FALLBACK_SHA256，实得 $digest）" +
                "——禁止未经 AUD 评审变更加密依赖"
        }
        logger.lifecycle("pinEd25519Fallback: $ED25519_FALLBACK_GAV sha256 ✓ 零传递依赖 ✓")
    }
}

tasks.named("test") { dependsOn(pinEd25519Fallback) }
tasks.named("check") { dependsOn(pinEd25519Fallback) }

// T2.2 DoD：协议单测分支覆盖率 ≥80%（jacocoTestReport 产出
// build/reports/jacoco/test/jacocoTestReport.xml，BRANCH 计数器）。
tasks.jacocoTestReport {
    reports {
        xml.required.set(true)
        html.required.set(true)
    }
}
