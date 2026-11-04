import java.io.File

/*
 * app 模块（T1.2）：壳工程 + 空引导页。
 * 包名 cn.worktool.open（ADR-04，docs/00 §ADR）。
 * Release 签名按 docs/ops/SIGNING.md §2 从环境变量注入（WORKTOOL_* 四项齐备才启用，
 * 缺失则产 unsigned APK / CI 跳过 assembleRelease）——密钥文件绝不入库（G13）。
 * C-7 修复（docs/04 §8，W-M4）：WORKTOOL_STORE_FILE 解析规则 = 绝对路径按原样；
 * 相对路径按【仓库根】（android/ 的上一级）解析——SIGNING.md §2 的
 * `secrets/worktool-open.jks` 即仓库根相对形态。旧实现 rootProject.file() 相对
 * android/ 解析成 android/secrets/（不存在）→ 签名构建必然失败。
 */
plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

val wtStoreFile: String? = System.getenv("WORKTOOL_STORE_FILE")
val wtStorePassword: String? = System.getenv("WORKTOOL_STORE_PASSWORD")
val wtKeyAlias: String? = System.getenv("WORKTOOL_KEY_ALIAS")
val wtKeyPassword: String? = System.getenv("WORKTOOL_KEY_PASSWORD")
val hasReleaseSigning = !wtStoreFile.isNullOrBlank() && !wtStorePassword.isNullOrBlank() &&
    !wtKeyAlias.isNullOrBlank() && !wtKeyPassword.isNullOrBlank()

android {
    namespace = "cn.worktool.open.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "cn.worktool.open"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    if (hasReleaseSigning) {
        // C-7：绝对路径按原样；相对路径按仓库根（android/ 上一级）解析。
        // 文件不存在 = 响亮失败（旧实现会在配置/执行期报晦涩错误，且永远找不到
        // android/secrets/ 下的文件——密钥实际在仓库根 secrets/ 或 CI 临时目录）。
        val storeFileResolved: File = File(wtStoreFile!!).let {
            if (it.isAbsolute) it else rootProject.projectDir.parentFile.resolve(it)
        }
        if (!storeFileResolved.isFile) {
            throw GradleException(
                "WORKTOOL_STORE_FILE 解析为 ${storeFileResolved.absolutePath} 但文件不存在" +
                    "（C-7：绝对路径按原样，相对路径按仓库根解析；见 docs/ops/SIGNING.md §2）"
            )
        }
        signingConfigs {
            create("release") {
                storeFile = storeFileResolved
                storePassword = wtStorePassword
                keyAlias = wtKeyAlias
                keyPassword = wtKeyPassword
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (hasReleaseSigning) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
        // W-M3R 真机装配（D1 lab）：lab 变体 = 独立 buildType（非 flavor），保证
        // debug/release 产品变体任务名与源集零改动（CI assembleDebug 不受影响，
        // G12 精神），产物任务名 assembleLabDebug。lab 专属内容全部在
        // src/labDebug/ 源集：networkSecurityConfig 信任 lab CA + 配对最小 UI
        // （INTERNET 权限已上移产品 main 源集，CR UI-01 修复，2026-11-03）；
        // applicationId 加 .lab 后缀与产品安装共存。
        create("labDebug") {
            initWith(getByName("debug"))
            isDebuggable = true
            applicationIdSuffix = ".lab"
            versionNameSuffix = "-lab"
            matchingFallbacks += listOf("debug") // 库模块无 labDebug 变体 → 回落 debug
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    sourceSets {
        // W-M3R 裁决 B（2026-11-03）：D1（Android 12/API 31）JCA 无 Ed25519 任何算法名，
        // 配对必填 ed25519PublicKey（spec §5.1.2 冻结）→ lab 一次性密钥对经构建参数注入
        // labDebug 资产（build/generated/，不入库）；资产只含公钥单字段（CR-R2 白名单
        // 拷贝），私钥仅存 local-private/ 注入参数侧，不入库、不入日志、不入 APK 产物（G13）。
        // 产品级回退提供者 = T2.6（W-M4，钉版本纯 Kotlin Ed25519，过 AUD-07 后生效）。
        getByName("labDebug") {
            assets.srcDir(layout.buildDirectory.dir("generated/labAssets"))
            res.srcDir(layout.buildDirectory.dir("generated/labRes"))
        }
    }
}

// lab 注入资产生成：WORKTOOL_LAB_ED25519_KEY_JSON（或 gradle property）提供
// {"ed25519PublicKeyHex":..,"ed25519PrivatePkcs8Hex":..}；未提供则资产缺位，
// lab 配对 UI 如实报错（fail-closed，不落任何兜底密钥）。
// CR-R2 白名单拷贝：注入参数侧含私钥（仅存 local-private/，G13），但端侧只消费
// ed25519PublicKeyHex（LabPairingActivity.loadLabInjectedEd25519PublicHex）——
// 资产只落公钥单字段，私钥绝不写入 build/generated、绝不进入 labDebug APK 产物。
val labEd25519KeyJson: String? = providers.gradleProperty("worktoolLabEd25519KeyJson")
    .orElse(providers.environmentVariable("WORKTOOL_LAB_ED25519_KEY_JSON"))
    .orNull

val wtGenerateLabEd25519Asset = tasks.register("wtGenerateLabEd25519Asset") {
    outputs.upToDateWhen { false } // 始终按最新构建参数重生成（值不入日志）
    doLast {
        val dir = layout.buildDirectory.dir("generated/labAssets").get().asFile
        dir.mkdirs()
        val f = File(dir, "lab_ed25519_key.json")
        if (labEd25519KeyJson.isNullOrBlank()) {
            f.delete()
        } else {
            // 注入 JSON 须含形状合法的 ed25519PublicKeyHex（64 位小写 hex，与端侧
            // 运行时校验同契约）；形状非法 = 构建期注入错误，立即失败不产半份资产
            val parsed = groovy.json.JsonSlurper().parseText(labEd25519KeyJson) as? Map<*, *>
                ?: throw GradleException("lab 注入密钥 JSON 形状非法（须为对象）")
            val publicHex = parsed["ed25519PublicKeyHex"] as? String
            if (publicHex == null || !publicHex.matches(Regex("^[0-9a-f]{64}$"))) {
                throw GradleException("lab 注入密钥缺/非法 ed25519PublicKeyHex（fail-closed，资产不落盘）")
            }
            f.writeText("""{"ed25519PublicKeyHex":"$publicHex"}""")
        }
    }
}

tasks.matching { it.name == "mergeLabDebugAssets" }.configureEach {
    dependsOn(wtGenerateLabEd25519Asset)
}

// lab 网络安全配置构建期注入（2026-11-04 公开仓脱敏）：lab CA 信任域主机不再
// 硬编码于入库 XML——经 gradle property worktoolLabHost / 环境变量
// WORKTOOL_LAB_HOST 注入（真实部署传内网地址；缺省=RFC5737 合成段 192.0.2.10，
// 合成段构建仅用于 CI 形状校验）。源集 res 指向 generated/labRes（同名静态
// XML 已从 labDebug/res/xml 移除，避免资源合并冲突）。
val labHost: String = providers.gradleProperty("worktoolLabHost")
    .orElse(providers.environmentVariable("WORKTOOL_LAB_HOST"))
    .orElse("192.0.2.10")
    .get()

val wtGenerateLabNetworkConfig = tasks.register("wtGenerateLabNetworkConfig") {
    outputs.upToDateWhen { false }
    doLast {
        val dir = layout.buildDirectory.dir("generated/labRes/xml").get().asFile
        dir.mkdirs()
        File(dir, "network_security_config.xml").writeText(
            """
            |<?xml version="1.0" encoding="utf-8"?>
            |<network-security-config>
            |    <domain-config cleartextTrafficPermitted="false">
            |        <domain includeSubdomains="false">$labHost</domain>
            |        <trust-anchors>
            |            <certificates src="@raw/ca_lab" />
            |        </trust-anchors>
            |    </domain-config>
            |</network-security-config>
            """.trimMargin(),
        )
    }
}

tasks.matching { it.name.startsWith("mergeLabDebugResources") }.configureEach {
    dependsOn(wtGenerateLabNetworkConfig)
}

dependencies {
    // 无障碍服务骨架（T1.2 声明透明服务；T2.4 落地原子操作）
    implementation(project(":core:accessibility"))
    // 协议层（T2.2：纯 JVM 协议语义 + Android 落地适配）
    implementation(project(":core:protocol"))
    implementation(project(":core:protocol-android"))
    // 本地任务引擎（T3.3：模式A 规则/cron/fallback 仲裁/暂停/干跑，纯 JVM）
    implementation(project(":core:task-engine"))
    // T3.1 指令执行器框架（app 层装配适配：FeatureExecutorBinding 合流两车道）
    implementation(project(":feature:executor"))

    testImplementation(libs.junit)
}
