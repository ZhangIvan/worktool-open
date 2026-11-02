# android/

WorkTool-Open Android 端（Kotlin，包名 `cn.worktool.open`〔ADR-04〕，AGP 8.5 / Gradle 8.7 / JDK17，minSdk 26 / targetSdk 34 / compileSdk 34）。

## 模块（T1.2 现状）

| 模块 | 状态 | 说明 |
|------|------|------|
| `app` | ✅ 壳工程 | MainActivity 空引导页；manifest 遵守 SR-10（allowBackup=false、零权限、exported 最小） |
| `core:accessibility` | ✅ 骨架 | 透明命名的 AccessibilityService（`WorkToolAccessibilityService`）；默认弱权限：`canRetrieveWindowContent=false`、不监听任何包；T2.4 落地原子操作 |
| `core:protocol` | ✅ 空占位 | T2.2 落地 WS/信封/心跳/离线暂存 |

- 完整模块规划：`docs/02-ARCHITECTURE.md` §3（core:task-engine / core:storage / feature:* / lib:ocr / lib:updater 按任务卡追加）
- 任务卡：T1.2（壳工程）、T2.2（协议层）、T2.4（无障碍核心）、T3.1（P0 指令执行器）、T3.3（本地模式）——见 `docs/03-EXECUTION-PLAN.md`

## 依赖策略（SR-03 / 门禁 G3）

**零遥测**：不引入任何统计/埋点/崩溃收集 SDK；当前运行时依赖为零（仅 Kotlin 标准库），测试依赖仅 JUnit4。

## 构建

- **CI**（主要途径）：`.github/workflows/android.yml`（paths 触发 `android/**`；JDK17 temurin + gradle/actions/setup-gradle；`assembleDebug` + `test`；debug APK 以 artifact 上传）。本仓库不要求本机安装 Android SDK。
- **本地**：JDK17 + Gradle 8.7 + Android SDK（platform 34）。Release 签名经环境变量 `WORKTOOL_STORE_FILE/STORE_PASSWORD/KEY_ALIAS/KEY_PASSWORD` 注入（见 `docs/ops/SIGNING.md` §2；四项齐备才签名，缺失产出 unsigned / CI 跳过）。
- 单测：`gradle test`（JVM，示例见 `app/src/test/`）。
