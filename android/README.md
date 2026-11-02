# android/

WorkTool-Open Android 端（Kotlin，包名 `cn.worktool.open`〔ADR-04〕，AGP 8.5 / Gradle 8.7 / JDK17，minSdk 26 / targetSdk 34 / compileSdk 34）。

## 模块（T2.2 后现状）

| 模块 | 状态 | 说明 |
|------|------|------|
| `app` | ✅ 壳工程+调试页 | MainActivity 引导页 + `NodeTreeDebugActivity`（T2.4：exported=false，实时节点树 dump/选择器试掷/截图自检）；manifest 遵守 SR-10（allowBackup=false、exported 最小） |
| `core:accessibility` | ✅ **T2.4 交付** | `WorkToolAccessibilityService` 实现：SR-10 最小化 config（三项能力位+两类事件订阅，见 `accessibility_service_config.xml`）；UiNode 抽象 + 外部 JSON 选择器规则引擎（零依赖解析器）；5 原子操作（click/longClick/scroll/inputText/screenshot，API30+ 低版本降级）；真机执行 NOT_RUN（随 T2.5 验证） |
| `core:protocol` | ✅ **T2.2 交付** | 协议层（纯 Kotlin/JVM）：v1 兼容信封编解码（WeworkMessageListBean 全字段+未知字段保真）、v2 帧信封+逐帧 HMAC（C-9 输入式）+AES-256-GCM、配对（Ed25519+X25519，Java Security API）、seq/nonce/eventId 会话状态机、AtLeastOnce 队列+journal 语义、OkHttp WS 客户端+指数退避重连。规格=`protocol/spec.md` v1.1.0 |
| `core:protocol-android` | ✅ **T2.2 交付** | Android 耦合薄适配：SQLite journal 落盘（`SqliteJournalStore`）、deviceToken 的 AndroidKeystore 加密存储（`AndroidKeystoreCredentialStore`，SR-05/G5）；设备侧行为验证随 T2.5 真机联调（deferred） |

- `core:protocol` 设计说明：协议逻辑零 Android API 依赖 → 纯 JVM 模块使 CI `gradle test` 直接跑协议单测（无 Robolectric/设备）；Android 侧经 `core:protocol-android`/`app` 消费。
- 完整模块规划：`docs/02-ARCHITECTURE.md` §3（core:task-engine / core:storage / feature:* / lib:ocr / lib:updater 按任务卡追加）
- 任务卡：T1.2（壳工程）、T2.2（协议层）、T2.4（无障碍核心）、T3.1（P0 指令执行器）、T3.3（本地模式）——见 `docs/03-EXECUTION-PLAN.md`

## 依赖策略（SR-03 / 门禁 G3）

**零遥测**：不引入任何统计/埋点/崩溃收集 SDK。运行时依赖共两个（docs/03 T2.2 指定）：
- `com.squareup.okhttp3:okhttp`——WS 客户端 + 配对 HTTP（唯一网络依赖）；
- `org.jetbrains.kotlinx:kotlinx-serialization-json`——v1/v2 信封 JSON 编解码。

加密一律 Java Security API（JDK17 内置 Ed25519/X25519/HmacSHA256/AES-GCM），**不引入第三方加密库**。测试依赖：JUnit4 + MockWebServer（仅 test classpath，不入产物）。

## 协议层测试（T2.2 DoD）

CI 必跑（`.github/workflows/android.yml`：`assembleDebug test` + `:core:protocol:jacocoTestReport` 覆盖率硬门禁 ≥80% BRANCH）：

| 测试面 | 文件 | 依据 |
|---|---|---|
| 夹具正例全量 + 反例必拒 | `FixtureSuiteTest`、`ValidatorNegativePathsTest` | `protocol/fixtures/**` 与 `fixtures/negative/**`（冻结测试接口，spec §7） |
| **hmac-vector 金标准逐字节复算**（关闭 AUD-03 C-4） | `HmacVectorTest` | `fixtures/hmac-vector.json`（C-9 输入式 + tamper_seq 反例） |
| 篡改 seq/ts/nonce 必败 + §5.2.4 校验顺序 + eventId 去重 | `session/FrameVerifierTest` | spec §5.2/§5.4 |
| RFC 向量：HKDF(5869)/X25519(7748)/Ed25519(8032) | `CryptoVectorTest` | RFC 附录向量 |
| RFC 8785 JCS 规范化 | `JcsTest` | 夹具 payloadDigest 逐字节对账 |
| AtLeastOnce 先落盘后副作用 / 崩溃重放 / E09 门禁 / epoch 拒旧 | `delivery/DeliveryTest` | spec §5.4.5、state-transitions E09 |
| 重连退避（v1 5→600s / v2 5→120s）/ 端点策略（SR-09）/ 心跳 / 45s 失联 | `transport/TransportTest` | spec §3 |
| OkHttp 真实链路冒烟（WS hello / 配对 HTTP / 重协商签名） | `OkHttpSmokeTest` | MockWebServer |

本地复跑（无需 Android SDK）：`gradle :core:protocol:test :core:protocol:jacocoTestReport`
（报告：`core/protocol/build/reports/jacoco/test/`；当前 BRANCH ≥80%——精确数值以 CI 摘要为准）。

已知边界（记录于 `canonical/Jcs.kt` kdoc）：非整数 double 的 JCS 最短十进制取自
`java.lang.Double.toString`，JDK<19 在极少数值上可能多一位尾数（JDK-4511638）；本协议
载荷均为整数，若服务端（Go）侧出现非整数数值载荷需以同一 RFC 8785 算法对齐复核。

## 构建

- **CI**（主要途径）：`.github/workflows/android.yml`（paths 触发 `android/**` 与 `protocol/**`〔协议权威件变更触发端侧单测复核，AUD-03 C-8〕；push/pull_request 统一分支过滤 main/zcode/main；JDK17 temurin + gradle/actions/setup-gradle；`assembleDebug` + `test` + 协议覆盖率门禁；debug APK 与覆盖率报告以 artifact 上传）。本仓库不要求本机安装 Android SDK。
- **本地**：JDK17 + Gradle 8.7 + Android SDK（platform 34）。Release 签名经环境变量 `WORKTOOL_STORE_FILE/STORE_PASSWORD/KEY_ALIAS/KEY_PASSWORD` 注入（见 `docs/ops/SIGNING.md` §2；四项齐备才签名，缺失产出 unsigned / CI 跳过）。
- 单测：`gradle test`（JVM；协议层夹具按仓库根相对路径 `protocol/fixtures/**` 引用，spec §7）。

## T2.2 deferred（真机联调 = T2.5，RIG 通道未通）

- `core:protocol-android` 的 SQLite/Keystore 设备侧行为验证（JVM 侧语义等价已由
  `InMemoryJournalStore`/`InMemoryDeviceCredentialStore` 承担）；
- v2 会话对真实服务端（T2.3 联调）与 901~904 控制帧实链路（schema 随 T3.4 冻结，spec §9 Q3）。
