/*
 * feature:executor（T3.1）：P0 指令执行器框架。
 *
 * 职责（docs/02-ARCHITECTURE.md §3 feature:* 行 + docs/03 T3.1 + docs/08 W-M3）：
 * - CommandExecutor 接口 + 注册表：输入 = 规范化 CommandIntent（P0 子集 203/206/207/225/212，
 *   规格依据 protocol/spec.md §4.2 指令表）；
 * - 输出 = 步骤化执行计划，在 [cn.worktool.open.core.accessibility.tree.UiNode]
 *   抽象上纯状态机运行（复用 core:accessibility 的 SelectorEngine/AtomicOperations，
 *   不重造轮子），便于 JVM 单测与真机驱动替换（真机执行 NOT_RUN，T2.5 后 W-M3R 补）；
 * - 每执行器内置：FR-53 风险级标注（L0/L1/L2）、FR-54 干跑钩子、FR-52 暂停门。
 *
 * 依赖策略（SR-03 / 门禁 G3）：零第三方运行时依赖；本模块主源集为纯 Kotlin
 * （不 import android.*，真机 UiDriver 实现归后续任务卡接线）；测试仅 JUnit。
 */
plugins {
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.android)
}

android {
    namespace = "cn.worktool.open.feature.executor"
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
    // UiNode 抽象 + 选择器引擎 + 原子操作库（T2.4 交付，复用而非重造）
    api(project(":core:accessibility"))
    // WeworkMessageBean/MessageTypes 等协议载荷模型（T2.2 交付，指令号/字段权威）
    implementation(project(":core:protocol"))

    testImplementation(libs.junit)
}
