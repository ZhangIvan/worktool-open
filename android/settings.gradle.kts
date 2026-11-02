/*
 * WorkTool-Open Android 端（T1.2 壳工程）
 * 模块结构对齐 docs/02-ARCHITECTURE.md §3：app + core:accessibility + core:protocol
 * 后续任务卡按同一节追加：core:task-engine / core:storage / feature:* / lib:*
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
