/*
 * WorkTool-Open Android 端（T1.2 壳工程 + T2.2 协议层 + T3.3 本地任务引擎）
 * 模块结构对齐 docs/02-ARCHITECTURE.md §3：app + core:accessibility + core:protocol(+android)
 * + core:task-engine；后续任务卡按同一节追加：core:storage / feature:* / lib:*
 *
 * T2.2 拆分说明：core:protocol 为纯 Kotlin/JVM 模块（信封编解码/加密/会话语义/
 * AtLeastOnce 队列全部 JVM 可单测，规格 = protocol/spec.md）；Android 耦合适配
 * （SQLite journal 落盘、AndroidKeystore 存档）在 core:protocol-android（AGP 库）。
 */
pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "worktool-open-android"

include(":app")
include(":core:accessibility")
include(":core:protocol")
include(":core:protocol-android")
include(":core:task-engine")
// T3.1（W-M3）：P0 指令执行器框架（FR-02/09/10/15，docs/02 §3 feature:* 行）
include(":feature:executor")
