/*
 * app 模块（T1.2）：壳工程 + 空引导页。
 * 包名 cn.worktool.open（ADR-04，docs/00 §ADR）。
 * Release 签名按 docs/ops/SIGNING.md §2 从环境变量注入（WORKTOOL_* 四项齐备才启用，
 * 缺失则产 unsigned APK / CI 跳过 assembleRelease）——密钥文件绝不入库（G13）。
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
        signingConfigs {
            create("release") {
                storeFile = rootProject.file(wtStoreFile!!)
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
