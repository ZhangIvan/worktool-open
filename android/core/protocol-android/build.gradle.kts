/*
 * core:protocol-android（T2.2）：Android 耦合适配层（AGP 库）。
 *
 * 职责（纯 JVM 的 :core:protocol 之外的设备侧落地件）：
 * - SqliteJournalStore：AtLeastOnce 队列 journal 的 SQLite 落盘
 *   （先落盘后副作用语义的持久层，docs/02 §5.1；表结构见 ProtocolDatabase）；
 * - AndroidKeystoreCredentialStore：deviceToken 的 Keystore 加密存储（SR-05/G5）。
 *
 * 说明：本模块为薄适配（无业务逻辑、无 UI、无权限声明）；协议语义单测全部在
 * :core:protocol（纯 JVM，CI `gradle test` 必跑）。设备侧行为验证 = T2.5 真机联调（deferred）。
 */
plugins {
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "cn.worktool.open.core.protocol.android"
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

dependencies {
    api(project(":core:protocol"))
}
