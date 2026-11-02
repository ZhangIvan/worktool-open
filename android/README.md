# android/

WorkTool-Open Android 端（Kotlin，包名 `cn.worktool.open`，minSdk 24 / targetSdk 34+）。

- 模块规划：`docs/02-ARCHITECTURE.md` §3（core:accessibility / core:protocol / core:task-engine / core:storage / feature:* / lib:ocr / lib:updater）
- 任务卡：T1.2（壳工程）、T2.2（协议层）、T2.4（无障碍核心）、T3.1（P0 指令执行器）、T3.3（本地模式）——见 `docs/03-EXECUTION-PLAN.md`
- 构建依赖 CI 或 S1 构建容器（本仓库不要求本机安装 Android SDK）
