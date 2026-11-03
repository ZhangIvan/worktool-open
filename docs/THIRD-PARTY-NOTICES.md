# 第三方声明（Third-Party Notices）

- 版本：v2.0（2026-11-03，W-M4 实化：按**实际解析的依赖清单**逐项列版本与 license）
- 数据来源（非记忆/非猜测）：
  - Android：`S1 构建机容器内 gradle :app:dependencies --configuration releaseRuntimeClasspath`
    实测解析树（2026-11-03，df68d50）+ 各 artifact 实际解析 POM 的 `<licenses>` 声明；
  - Go：`go list -m all`（server/，27 模块）+ 从模块代理 zip 内逐个读取 LICENSE 全文判定
    （2/3-clause BSD 按"是否含 no-endorsement 条款"区分）；
  - 构建工具链（Gradle/AGP/Kotlin 编译器）：上游仓库 license 文件。
- 本清单随 `server/go.mod`、`android/gradle/libs.versions.toml` 变更同步更新（CI G3 门禁
  校验零遥测；新增依赖须过 docs/04 门禁复核后再入此清单）。

## 上游衍生

- [gallonyin/worktool](https://github.com/gallonyin/worktool) — Apache-2.0, Copyright 2022 gallonyin
  （见 LICENSE / NOTICE；本仓库对其做行为级重写，未复制其源码）

## Android（APK 运行时依赖，随产物分发）

| 依赖 | 版本 | License | 用途 | 来源 |
|------|------|---------|------|------|
| org.jetbrains.kotlin:kotlin-stdlib | 1.9.24 | Apache-2.0 | Kotlin 标准库 | POM 声明 |
| org.jetbrains:annotations | 13.0 | Apache-2.0 | stdlib 传递（注解） | POM 声明 |
| org.jetbrains.kotlinx:kotlinx-serialization-json（含 -jvm / core / core-jvm / bom） | 1.6.3 | Apache-2.0 | v1/v2 帧 JSON 编解码（T2.2） | POM 声明 |
| com.squareup.okhttp3:okhttp | 4.12.0 | Apache-2.0 | WS 客户端 + 配对 HTTP（T2.2） | POM 声明 |
| com.squareup.okio:okio（含 okio-jvm） | 3.6.0 | Apache-2.0 | okhttp 传递 | POM 声明 |
| net.i2p.crypto:eddsa（ed25519-java） | 0.3.0 | CC0 1.0 Universal | API 24~32 Ed25519 纯 JVM 回退（T2.6；core:protocol `implementation`，随 APK 分发；JCA 优先不变） | POM 声明（本地 gradle 缓存 `eddsa-0.3.0.pom` `<licenses>`：CC0 1.0 Universal） |

> 数据基线说明（CR T34-03/T2.6-R1）：上表前 5 行来自 df68d50（W-M3R）快照；eddsa 行为
> T2.6 变更集新增（快照早于引入，须在下次 `S1 :app:dependencies` 重采时合并进正式快照）。
> eddsa 为**零传递依赖**（钉版 + sha256 + 零传递断言门禁见
> `android/core/protocol/build.gradle.kts` `pinEd25519Fallback`），jar sha1
> `1901c8d4d8bffb7d79027686cfb91e704217c3e1`、sha256
> `4dda1120db856640dbec04140ed23242215a075fe127bdefa0dcfa29fb31267d`。

Android **测试依赖**（不进 APK）：

| 依赖 | 版本 | License | 来源 |
|------|------|---------|------|
| junit:junit | 4.13.2 | EPL-1.0 | 上游仓库 license（junit-team/junit4） |
| com.squareup.okhttp3:mockwebserver | 4.12.0 | Apache-2.0 | okhttp 仓库同源 license |

## Go（server/ 运行时依赖，编译进单二进制）

**直接依赖**（server/go.mod `require`）：

| 依赖 | 版本 | License | 判定依据 |
|------|------|---------|----------|
| modernc.org/sqlite | v1.34.4 | BSD-3-Clause | zip 内 LICENSE 全文（含 no-endorsement 第 3 条） |
| github.com/gorilla/websocket | v1.4.2 | BSD-2-Clause | zip 内 LICENSE 全文（2 条款，无 no-endorsement） |

**间接依赖**（runtime 图）：

| 依赖 | 版本 | License |
|------|------|---------|
| github.com/dustin/go-humanize | v1.0.1 | MIT |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | **MPL-2.0**（文件级 copyleft；以未修改的模块形态链接，随本清单履行声明义务） |
| github.com/mattn/go-isatty | v0.0.20 | MIT |
| github.com/ncruces/go-strftime | v0.1.9 | MIT |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748 | BSD-3-Clause（Go 许可模板） |
| golang.org/x/sys | v0.22.0 | BSD-3-Clause（Go 许可模板） |
| modernc.org/gc/v3 | v3.0.0-20240107210532 | BSD-3-Clause |
| modernc.org/libc | v1.55.3 | BSD-3-Clause |
| modernc.org/mathutil | v1.6.0 | BSD-3-Clause |
| modernc.org/memory | v1.8.0 | BSD-3-Clause |
| modernc.org/strutil | v1.2.0 | BSD-3-Clause |
| modernc.org/token | v1.1.0 | BSD-3-Clause |

**仅测试图**（不进产物二进制）：github.com/google/pprof（Apache-2.0）、
github.com/pmezard/go-difflib（BSD-3-Clause）、golang.org/x/{exp,mod,tools}（BSD-3）、
modernc.org/{cc/v4, ccgo/v4, fileutil, gc/v2, opt, sortutil}（BSD-3-Clause）。

Go 工具链/标准库：Go 1.23（BSD-3-Clause 风格 Go 许可）。

## 构建工具链（不随产物分发）

| 工具 | 版本 | License |
|------|------|---------|
| Gradle | 8.7（wrapper 钉版） | Apache-2.0 |
| Android Gradle Plugin | 8.5.2 | Apache-2.0 |
| Kotlin 编译器 | 1.9.24 | Apache-2.0（JetBrains，仓库 license/） |
| Android SDK（compileSdk 34 / build-tools 34.0.0） | — | Android SDK Terms（dl.google.com；二进制不复分发） |
| OpenJDK（Temurin 17，构建容器） | 17 | GPLv2+CE（构建环境，不随产物分发） |

## 管理端（admin/）

已交付（W-M4 v0.1 最小面）：Go 直接伺服的单页 HTML + 原生 JS（`admin/index.html`，
经 `server/internal/admin` `Deps.UIDir` 挂载）——**零 npm、零构建链、零第三方运行时
依赖**（无 React/TS；FR-40 最小面，FR-41/42 与 RBAC 五角色归 v0.2+/W-M5）。

> 依据 ADR-05：友盟 U-App/U-APM、TalkingData、腾讯 WWAPI aar 等专有 SDK 一律不引入，CI 门禁 G3 强制检查。
