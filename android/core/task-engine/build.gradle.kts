/*
 * core:task-engine（T3.3）：本地模式任务引擎，纯 Kotlin/JVM 模块。
 *
 * 为什么是 JVM 模块而非 AGP 库：本地规则（关键词/好友通过/本地 cron）、fallback 仲裁、
 * PauseManager/干跑、(ruleId,eventHash) 去重全部不依赖任何 Android API；纯 JVM 化使
 * CI 的 `gradle test` 直接在 JVM 上跑单测（docs/03 T3.3 DoD：规则/仲裁/去重 ≥15 例
 * JVM 纯测试）。Android 侧经 app 接线消费；执行器（指令 201~234）由 T3.1 的
 * feature:* 执行器实现 [cn.worktool.open.core.task.ActionExecutor] 后注册注入。
 *
 * 职责（docs/02 §2 模式A / §5.2；docs/01 FR-15/38/39/46/52/54）：
 * - 关键词自动回复规则引擎（FR-39，规则=JSON 配置：匹配→动作意图）
 * - 新好友自动通过钩子（FR-15，产出 PASS_FRIEND 意图，消费 T3.1 执行器接口）
 * - 本地 cron（FR-38 模式A：端侧定时独立跑）
 * - fallback 仲裁（02 §5.2：fallbackLocalScheduling 默认关 + server 不可达超阈值；
 *   重连后 source=local 上报，(ruleId,eventHash) 去重，spec §5.4.5）
 * - PauseManager（FR-52 暂停态在线指令只入队，恢复须显式确认）与 DryRunRunner（FR-54）
 * - 优先级常量与仲裁器：用户接管 > 服务端指令 > 本地规则（02 §5.2）
 *
 * 安全红线：零遥测（SR-03/G3，仅复用 core:protocol 的 kotlinx-serialization 传递依赖）、
 * G12 零 hook/动态加载；实现不引入新第三方依赖。
 */
plugins {
    alias(libs.plugins.kotlin.jvm)
    alias(libs.plugins.kotlin.serialization)
}

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

kotlin {
    jvmToolchain(17)
}

dependencies {
    // 复用 T2.2 协议层（MessageTypes/WeworkMessageBean/Scheduler 抽象，避免重造；
    // kotlinx-serialization 经 core:protocol 的 api 传递，本模块不新增第三方依赖）
    api(project(":core:protocol"))

    testImplementation(libs.junit)
}
