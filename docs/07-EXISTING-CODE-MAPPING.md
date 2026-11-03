# 既有代码映射（Existing Code Mapping）v1.1

- 版本：v1.1（2026-11-03，AUD-G 前置刷新，09 §D 登记项；v1.0=2026-11-02）
- 目的：**一套 FR/SR 编号映射两条实现线**（我方 Go 线 `zcode/main` ↔ Codex 线 `origin/worktool/synthetic-prototype`），防止双线各自扩张时语义漂移（用户指令 2026-11-02；依据 ADR-07/08）
- 取证方式：Codex 线文件以 `git -C <仓库> show <快照>:<path>` 只读（不 checkout、不 merge）
- **诚实性约定**：①凡无法从其产物核实的单元格标【未核对】；②引用其证据时保留原始标注（SYNTHETIC_E2E_TESTED / NOT_RUN / SKIP / SOURCE CANDIDATE / NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY / BLOCKED），**不得升级其结论**——其"实现/测试通过"仅指合成本地原型层（其 status.md 原文："A passing build or synthetic test is not proof of real WeCom automation"）；③其 Android 侧证据截至快照仅为"构建通过+静态审查"，设备执行全部 NOT_RUN
- 状态取值（我方 Go 线）：未开始=仅 01 需求行 ｜ 设计中=02/03 已有架构或任务落位 ｜ 已吸收设计=06 §3 对应条目已写入我方规划 ｜ 已实现雏形=W-M3 起部分模块/测试已入库（以工作区实测为准，含 WIP）
- 行动取值：独立实现 ｜ 借鉴设计 X（X=06 §3 编号①~⑧）｜ 等待合流（用户 PR 裁决）

### 快照基线登记（维护规则 1）

| 快照 hash | 取证日 | 增量要点 |
|-----------|--------|----------|
| c83423c | 2026-11-02 | v1.0 全表基线（Node 线 + android-client 首源） |
| 181b5784 快照基线（2026-11-03） | 2026-11-03 | +10 commits：`backend-go/` Go 基座全目录（protocol envelope + store/pg runner/admission/lease/budget + 迁移 0001~0007 + 22 组 PG CI 候选）、own-fixture 无障碍候选+静态隔离门（d8bb16e）、`verify_variant_isolation.py` APK 编译产物级校验三连（07fb478/b277806/181b578）；**其 `docs/status.md` 与 c83423c 逐字一致（git diff 为空）**——新候选未在其 status.md 登记证据等级 |

## 0. 编号字典警告（先读，影响全表）

其 status.md 原文：数字 101/102 在"公开事件文档"与"历史客户端常量"中含义不同，**"Do not join those catalogs on the bare number"**（禁止按裸数字 join 两个目录）。因此：
- 我方 v1 兼容信封必须继续用数字 socketType（生态兼容，ADR-03），数字↔语义映射**只**在 `protocol/` 冻结表（源=local-private/audit/02 §A 还原表）中进行；
- 我方 v2 能力标识吸收其 namespaced 命名（`wt.*` / `message.send_text` 风格，源 `contracts/capabilities.json`），杜绝裸数字出现在 v2 层；
- 本表 CMD-xxx 引用其目录编号，仅作对照锚点，不构成两线等价声明。

## 1. P0 主映射表（我方 P0 全集，01-REQUIREMENTS 口径；FR-28/29 为 P1、FR-33 为 P3 但均属 05 矩阵 v0.1 首批）

| 我方 FR | 语义 | Codex 线对应物 | Codex 线代码位置 | Codex 线测试/证据（保留原始标注） | 我方 Go 线状态 | 行动 |
|---------|------|----------------|------------------|-----------------------------------|----------------|------|
| FR-01 | 新消息监听与上报（≤3s、离线暂存重发） | CMD-101 `wt.type.receive.message.list`、CMD-202 `wt.loop.receive.new.message`、CMD-283 `wt.do.receive.new.message`（均 wire=none） | 无实现代码；仅 `contracts/capabilities.json` 目录条目 + wire `device_event` 通道定义（wire.schema.json） | coverage.csv：NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY；Android NOT_RUN | 设计中（02 §3 core:protocol、05 首批） | 独立实现（v1 数字信封以 audit/02 §A 为源） |
| FR-02 | 发送消息（含@，指令 203） | CMD-203 `wt.send.message` → **wire `message.send_text`**（唯一两动作之一） | `server/developer/plugin.ts`（POST /api/developer/v1/messages，仅 type 203）、`server/app.ts`（dispatch/transition）；Android `core/NativeCommandEngine.kt`（仅合成收件箱效果） | 服务端 SYNTHETIC_E2E_TESTED（tests/e2e.test.ts + artifacts/verify.txt）；Android NOT_RUN；其 developer-api-slice.md 自记 28 passed/0 failed（运行日志 developer-validation.txt 未随源码公开，计数【未核对】） | 设计中（T3.1 执行器计划） | 独立实现；其 203 负例集（幂等变参/越权/撤销）转我方 Go 用例（借鉴①②③） |
| FR-09 | 创建外部群（指令 206） | CMD-206 `wt.init.group`（wire=none） | 无（目录条目） | NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY；Android NOT_RUN | 未开始 | 独立实现 |
| FR-10 | 修改群信息/成员（指令 207/225） | CMD-207 `wt.update.group`、CMD-225 `wt.modify.group.member.info`（wire=none） | 无（目录条目） | 同上 | 未开始 | 独立实现 |
| FR-15 | 新好友自动通过（纯端侧规则） | CMD-212 `wt.pass.all.friend.request`（wire=none；指令式，非规则式） | 无（目录条目） | 同上 | 未开始 | 独立实现（端侧规则引擎，无需服务端） |
| FR-28 | 群列表/全群列表查询（501/508） | CMD-501 `wt.get.group.info`、CMD-508 `wt.get.all.group.info`（wire=none） | 无（目录条目） | 同上 | 未开始 | 独立实现（响应 Schema 以我方 protocol/ 为准） |
| FR-29 | 好友列表/全好友查询（502/507） | CMD-502 `wt.get.friend.info`、CMD-507 `wt.get.all.friend.info`（wire=none） | 无（目录条目） | 同上 | 未开始 | 同上 |
| FR-33 | 上传本地文件/日志（指令 509=GET_LOCAL_FILE，语义以 audit/02 F30 为准，非"列表"——01/05 已同步更正） | CMD-509 `wt.get.local.file` → **wire `file.export`**。**语义偏差**：其为"审批制数据导出"合成动作（exports 表+approver），非端侧文件枚举 | `server/app.ts`（exportAllowed 审批校验）、`server/developer/plugin.ts`、`server/storage.ts`（exports 表） | 服务端 SYNTHETIC_E2E_TESTED（含禁止自批/改 scope/借批负例，TEST-REPORT）；Android NOT_RUN | 未开始 | 独立实现；合流时需对齐 509 语义与命名（防"同名不同义"） |
| FR-34 | 对外 REST API（apifox 兼容，30+ 指令，v1 原生） | `/api/developer/v1`**隔离面**（明确"not legacy API compatibility"）：仅 203 一个类型，其余 501 CAPABILITY_UNAVAILABLE（error-codes.json 422）。**+181b5784**：backend-go admission 候选=同口径（"Only synthetic203 -> message.send_text, one child"），幂等 digest+订阅快照+容量配额原子图，仍无 legacy 兼容面 | Node：`server/developer/plugin.ts`、`server/developer/schema.ts`、`server/app.ts`；Go：`backend-go/internal/store/pg/admission{,_canonical}.go`（迁移 0006） | 同 FR-02 证据；其 app token 一次性下发/仅存 SHA256/epoch 轮换实测（synthetic）。admission：Go 本地 golden 29 例（19 有效+10 拒绝）声称 PASS（"run evidence archived separately"【未核对】）；真 PG ATC01~08 组 "passed actual PostgreSQL CI on source bab1a3c"（其 ci-pg-integration.md 原文） | 已吸收设计（02 §4 internal/api v1 原生 + ⑤）；我方工作区 v1 路由 `/webserver/wework/{robotId}` 在库 | 独立实现（v1 兼容为我方原生）；双 API 面（v1 兼容 vs developer 隔离面）取舍→**等待合流**由用户裁决；admission 幂等 digest 语义并入 T4.3 契约借鉴范围（docs/11 §A 组 1 同批） |
| FR-37 | 指令队列管理（清空/指定清除/批量/插队，apifox） | **+181b5784 部分对应**：backend-go `lease{,_claim,_retire}.go` 的 ClaimReadyCommand（queued→claimed 单飞发送意图；重放已 claim 命令不再返回第二个 intent）——但为 **DB claim 层**，非 apifox 队列管理 API（无清空/插队面）；Node 线无对应 | `backend-go/internal/store/pg/lease_claim.go`、`lease.go`、迁移 0007（gateway_lease_connection） | LF01~08 真 PG "**not yet claimed run**"（SOURCE CANDIDATE，22 组 CI 候选待其父批准执行）；本地无 DSN 环境 **SKIP, not PG PASS**（其 lease_README 原文）；吞吐 NOT_MEASURED（全租户串行 correctness-first） | 设计中→部分在库（02 §4 队列面规划 + 我方 scheduler/pipeline 幂等门测试已在库） | 独立实现（apifox 队列 API 为我方原生）；lease claim 契约（attempt 仅 claim 时生成一次/COMMIT_OUTCOME_UNKNOWN 禁盲重试/围栏不能撤回已入线帧）按 docs/11 §A 组 1 入 T4.3（W-M6 PG 轨） |
| FR-35 | 机器人消息回调（签名+重试） | 签名回调：HMAC-SHA256(`eventId.timestamp.rawBody`)、±30s、重试 1/2/4s、第 4 次死信、2s 墙钟/64KiB 上限；outbox 与结果同事务；默认禁网、仅字面 loopback 接收器 | `server/developer/callbacks.ts`、`server/developer/plugin.ts`（同事务 enqueue） | 真实 loopback 接收器实测（synthetic，developer-api.test.ts）；生产 HTTPS/密钥 NOT_IMPLEMENTED（WTO-008） | 已吸收设计（⑤，T3.2 DoD 已含） | 借鉴设计⑤ + 独立实现（补生产面：持久密钥、目的地策略、保留期） |
| FR-38 | 定时任务（cron；本地模式端侧可独立跑） | **无对应**（TEST-REPORT："批量和真实定时任务未完成"）；**181b5784 复核**：backend-go 亦无 scheduler/cron/worker（其 README 自认无 API handler、无 worker） | 无 | 未实现（其自认） | 已实现雏形（我方 `server/internal/scheduler` cron_expr/cron/dryrun 测试在库，工作区含 WIP） | 独立实现 |
| FR-39 | 关键词自动回复（端侧规则引擎） | **无对应**（无端侧规则引擎） | 无 | — | 设计中（T3.3） | 独立实现 |
| FR-46 | 离线消息暂存与重发 | 语义=其"结果先本地持久化再上传；服务端事务提交后 result_ack 方可清理重传队列；重放收敛不重复执行"（设备-服务端往返，非企微消息暂存） | Android `transport/SqliteJournal.kt`、`transport/OkHttpDeviceLink.kt`、`androidTest/.../TransportPersistenceTest.kt`；服务端 `server/app.ts`/README 重放语义 | Android 侧：instrumentation APK 仅**编译通过**、执行 NOT_RUN（status.md 证据表）；服务端重传收敛负例通过（synthetic，TEST-REPORT） | 设计中（02 §5.1 端侧 journal） | 独立实现；借鉴其"持久化→上传→ACK→清理"次序与同 attempt 重放规则（CONTRACT.md） |
| FR-52 | 用户接管：一键暂停（安全断点停、只入队不执行、三处一致） | 撤销/取消族：`stop_request`（仅停止请求非许可）、`cancel_before_delivery`、accepted 安全点停止、权限撤销增量 policyVersion 阻新调度；Android 暂停门（pause 状态读引擎权威 gate）。**+181b5784**：backend-go lease LF05 覆盖 revoke/epoch/scopes/binding/policy/cancel **before claim**（撤销先于 claim 的 DB 层复检），"Revoke commits authority changes before any separately ordered lease cleanup" | Node：`server/app.ts`（requestPolicyStop/dispatch 守卫/1s 扫描）、Android `ui/MainActivity.kt`+`core/NativeCommandEngine.kt`；Go：`backend-go/internal/store/pg/lease{,_claim,_retire}.go`（LF05） | 服务端：队列取消与安全点停止无合成副作用、凭据撤销阻新会话（synthetic 通过，最终 31 项聚合，tests/e2e-final-rbac-first-run.txt）；Android 暂停修复 ea10a55 的新断言"authored but NOT_RUN"（status.md）；连接态 5s 撤销 SLO"未执行有统计意义验证"（其自认）。LF05 真 PG **not yet claimed run**（SOURCE CANDIDATE） | 已吸收设计（02 §5.2 优先级：用户接管>服务端指令>本地规则；暂停三分法=T4.1，D1 批复） | 独立实现；借鉴 stop_request/安全点语义（CONTRACT.md R1 refinement：transport_ack≠执行许可）；lease 撤销-先于-claim 复检并入 T4.3 契约借鉴 |
| FR-53 | 误发保护（L0/L1/L2 分级+限速+白名单+独立 approver+熔断） | **部分**：approver 分权（file.export 审批：绑定申请人+设备+scope digest，approver 独立且须当前活跃）；派发/执行前复检主体与角色；RBAC 五角色。**无** L0/L1/L2 分级、无接收人白名单、无去重窗、无全局熔断（除登录限流）。**+181b5784**：admission 的 charged_jobs/charged_bytes 为**内部容量配额**（其原文"never money"），订阅快照+同 digest 幂等——仍非风险分级/白名单/熔断，原"无分级"结论维持 | Node：`server/app.ts`（exportAllowed/actorAuthorized/commandAuthorized）、`server/storage.ts`（exports CHECK approver≠actor）；Go：`backend-go/internal/store/pg/admission.go`（容量计数器原子扣减） | REGRESSION-GAPS 五反例修正后独立回归通过（对象级授权/失活复检/owner 只读/自批拒绝）；范围仅 file.export（synthetic）。admission 真 PG ATC01~08 "passed actual PostgreSQL CI on source bab1a3c" | 已吸收设计（④，FR-53 备注"复用既有审批角色"；L2 独立 approver 复用） | 借鉴设计④ + 独立实现（L1/L2 分级、白名单、熔断为我方增量，其线无对应物）；guard 对象化默认拒收=T4.5 |
| FR-53 | 误发保护（L0/L1/L2 分级+限速+白名单+独立 approver+熔断） | **部分**：approver 分权（file.export 审批：绑定申请人+设备+scope digest，approver 独立且须当前活跃）；派发/执行前复检主体与角色；RBAC 五角色。**无** L0/L1/L2 分级、无接收人白名单、无去重窗、无全局熔断（除登录限流） | `server/app.ts`（exportAllowed/actorAuthorized/commandAuthorized）、`server/storage.ts`（exports CHECK approver≠actor） | REGRESSION-GAPS 五反例修正后独立回归通过（对象级授权/失活复检/owner 只读/自批拒绝）；范围仅 file.export（synthetic） | 已吸收设计（④，FR-53 备注"复用既有审批角色"） | 借鉴设计④ + 独立实现（L1/L2 分级、白名单、熔断为我方增量，其线无对应物） |

### 1.1 backend-go 模块 → 我方 FR 行映射（快照 181b5784 新增；裁决=docs/11 §A v1.1，D3 已批复）

> **诚实更正**：backend-go **无独立 gateway 模块、无独立状态机模块**——其 README 自认"does not open listeners"；状态迁移（queued→claimed/delivered、delivered/accepted/running→execution_unknown 一次性迁移）内嵌于 lease claim/retire 事务 SQL 与迁移 0007，不构成可映射我方 FR 行的独立 state machine。我方 `server/internal/statemachine/machine.go`+`gateway/v2.go` 为独立模块，两线在此维度**无可比物**。

| backend-go 模块（181b5784） | 内容一句话（保留其标注） | 映射到我方哪行 | 裁决/去处 |
|------------------------------|--------------------------|----------------|-----------|
| `internal/protocol`（envelope/json） | wire 1.0 外层信封校验（10 kinds、synthetic mode、seq≥1）；body UNVALIDATED；无 JCS/HMAC/会话/重放（其 README 自认） | 跨行协议基座 → 我方 `server/internal/wire` + `android/core/protocol`（协议 1.2.0 三语言金标准 72/72）；表内最近似 SR-08 行的协议面 | docs/11 §A 组 2：**不采纳（我方保留）**；仅 stop_request/result_ack/attemptId 语义入 T4.7 |
| `internal/store/pg/lease{,_claim,_retire}` | 网关租约/DB claim/围栏（DB 时钟单源、fence CAS、COMMIT_OUTCOME_UNKNOWN）；LF01~08 真 PG not yet claimed run | **FR-37**（claim 单飞意图）+ **FR-52**（LF05 撤销先于 claim） | docs/11 §A 组 1：不采纳（当下）/T4.3 契约借鉴，PG 轨=W-M6（D4） |
| `internal/store/pg/admission{,_canonical}` | synthetic203 幂等 digest+订阅快照+容量配额原子图；charged_jobs/bytes="never money" | **FR-34**（REST 提交幂等）+ **FR-53**（容量配额≠风险分级，原结论维持） | 并入 T4.3 契约借鉴范围；真 PG 14 组已过其隔离 CI（bab1a3c，其原文） |
| `internal/store/pg/runner.go`/`budget.go` | 有界事务生命周期+commit 未知保守分类+部署级连接池预算（fake 适配测试 PASS，真 PG NOT_RUN） | FR-46/48 可靠性语义层 + 02 §5.1（commit-unknown 已登记 B3/T4.2） | 契约借鉴入 T4.2/T4.3；我方 SQLite 单写者无池预算需求 |
| 迁移 0001~0007 | append-only、逐迁移 fail-closed 守卫、byte-identical 旧迁移 | 我方 store schema 演进纪律（02 §5.1） | PG 轨（W-M6）启动时按 docs/11 §A 组 1 一并吸收 |

## 2. SR 映射表（SR-08/11/12/13/14）

| 我方 SR | 语义 | Codex 线对应物 | Codex 线代码位置 | Codex 线测试/证据 | 我方 Go 线状态 | 行动 |
|---------|------|----------------|------------------|-------------------|----------------|------|
| SR-08 | 服务端鉴权：配对 token+HMAC 防篡改+全链路 TLS | 设备凭据=随机 256-bit、服务端仅存 hash、可轮换撤销（epoch）；一次性 enrollment token；WS 会话 bearer；浏览器 HttpOnly+SameSite=Strict+CSRF；**无逐帧 HMAC**（仅回调出站有 HMAC）；TLS=loopback 例外+对外强制（未部署验证） | `server/app.ts`（enrollment/device-sessions/auth/onRequest loopback 钩子）、`server/storage.ts`（devices.secret 存 hash）；Android `transport/KeystoreCredentialVault.kt` | server.test.ts 4/4 + e2e 14/14（synthetic，verify.txt）；Android Keystore 代码存在但设备 NOT_RUN；生产 TLS 部署未验证 | 已吸收设计（⑦；02 §5 v2：配对码+Ed25519 公钥+逐帧 HMAC+nonce 去重为我方增量） | 独立实现（v2 逐帧 HMAC/ECDH 强于其线；借鉴⑦绑定纪律） |
| SR-11 | 指令参数安全：fileUrl/fileBase64 等外链仅白名单域（默认拒收） | 部分：schema 严格（禁额外字段/任意路径/脚本）；回调目的地=字面 loopback 白名单、默认 501、拒 DNS/私网/重定向/改端口（SSRF 默认拒）；**fileUrl 类参数面尚不存在**（首批仅两个合成 payload） | `server/developer/callbacks.ts`（目的地策略）、`contracts/wire.schema.json` + `negative-extra-path.json` | developer-api.test.ts：default SSRF denial、HMAC 篡改/时窗负例（synthetic 通过） | 设计中（G11 门禁已定） | 独立实现 + 借鉴⑤之"字面白名单默认拒"作为我方回调/fileUrl 校验范式 |
| SR-12 | 接管/熔断/审批/撤销事件全量审计留痕 | audit 表 + command_events 事件流，与状态迁移同事务（redacted audit：仅 metadata/referenceId，不落正文）；撤销=stop_request+`command.policy_stop_requested` 留痕；集成审计修正（批准未绑申请人/设备、状态事件漏记）已回归 | `server/app.ts`（audit()/recordTransition()）、`server/storage.ts`（audit/command_events/outbox 同事务） | server.test.ts + e2e 审计断言（synthetic 通过）；REGRESSION-GAPS 集成审计反例已修 | 已吸收设计（02 §5.1"结果/事件/审计/outbox 同事务"） | 独立实现（同事务范式已吸收；④五反例入我方审计用例） |
| SR-13 | 产品实现零 hook（frida/Xposed/注入类仅限 test/D1 lab） | **构建级实现**：lab/debug/release 三变体源集各自 VariantFactory；lab 专属 EndpointPolicy/FixtureCommandPolicy；变体隔离脚本+双端同测 | `android-client/app/src/{lab,debug,release}/.../policy/`、`android-client/tools/verify_variant_isolation.py`、`testDebug/testRelease` OrdinaryVariantIsolationTest、`.github/workflows/android.yml` | 首源 12cb1a3 CI：JVM test/lint 任务运行+静态 APK 审查通过，但"no retained JUnit XML was used to assert an exact test-count"（status.md，计数【未核对】） | 设计中（G12 门禁已定） | 借鉴设计⑧（变体源集隔离=SR-13/G12 的构建级实现方式）+ 独立实现 |
| SR-14 | 静态数据保护：敏感数据最小化/加密存储、逻辑删除的 WAL/备份残留处置文档化 | **未实现**（其自认）：SQLite 明文（"Synthetic payloads are stored in local SQLite plaintext"，README）；逻辑删除 only（deviceEraseState=pending、backupExpiryAt=null）；无保留期 GC；WAL/备份/离线副本"not claimed physically erased" | `server/storage.ts`（deletions 表）、`server/README.md`（Persistence/ADR-001 节） | TEST-REPORT："逻辑删除 endpoint 不证明擦除 WAL/备份/离线副本；本轮没有完整删除恢复验收" | 设计中（02 §6.1 已定最小化保留→加密路线） | 独立实现（两线同缺口，互为印证；其诚实披露的残留面清单可直接并入我方 02 §6.1 审查项） |

## 3. Codex 线 WTO-001~009 差距 → 我方归属映射（其差距编号原文见 docs/status.md；快照 c83423c 起口径，181b5784 复核）

> **快照 181b5784 总注**：其 `docs/status.md` 与 c83423c **逐字一致（git diff 为空）**——下列"增量"列来自其新增代码/文档实况（backend-go/、d8bb16e、bab1a3c、765f0f1、工具三连），其 status.md **未登记任何新证据等级**，按其 WTO-009 自身口径不得视为状态变化。

| WTO | 其差距摘要（保留其口径） | 快照 181b5784 增量（代码/文档实况） | 我方归属（需求/任务/文档） | 归属行动 |
|-----|--------------------------|--------------------------------------|---------------------------|----------|
| WTO-001（P1） | 配置 GET/PUT 路由标注 M1 未实现（其设计盘点 docs/design-baseline.json 内） | 无增量（backend-go 自认无 API handler/无 listener） | FR-40/42 + 02 §4 internal/admin（机器人 CRUD/配置面，T3.2） | 独立实现（我方 admin API 规划内；无需依赖其线） |
| WTO-002（P1） | download-grants/下载路由缺失（绑定授权+一次性/防重放+数据 scope 测试） | 无增量 | 02 §6.1 泄露面收敛（"导出需多主体审批+一次性 download-grant"）+ SR-14 | 借鉴其缺口界定，我方 admin 设计一次性 grant（多主体审批已吸收④）；独立实现 |
| WTO-003（P2） | release/更新路由显式 CAPABILITY_UNAVAILABLE；签名更新验证与回滚未做 | 无增量 | FR-44（自更新默认关+HTTPS+同签名校验）+ SR-07 | 独立实现（v0.1 不排期，边界已明示） |
| WTO-004（P0 for legacy adapter） | 数字目录正式命名空间映射与冲突拒绝测试缺失；"never map 101/102 or another bare numeric constant to an operation" | 无增量（backend-go envelope 用 kind 字符串字面量校验，未做数字映射，无冲突拒绝测试） | protocol/ 冻结映射表（audit/02 §A 为源）+ FR-01/34 | 借鉴其规则入我方 protocol/spec（v2 namespaced capabilityId、v1 数字映射仅存冻结表）；独立实现 |
| WTO-005（P1 by capability） | 67 项参考清单=62 项 schema-pending+2 合成 wire+3 控制子集，"不是 67 个已冻结/已实现指令" | 无增量（backend-go 仅触达 synthetic203 一类） | docs/05 逐指令支持矩阵 + 01 FR 全表 | 方法论互补：我方以 05 矩阵逐项追踪（⬜→✅ 需真机证据）；吸收 capabilities.json 的 capabilityId 命名为 v2 能力标识 |
| WTO-006（P1） | AccessibilityService 宿主、权限引导、生命周期、事件过滤未实现（首 APK 无 service/receiver 注册） | **代码增量（d8bb16e）**：own-fixture 无障碍候选（`FixtureServiceRegistry.kt` 215 行、`OwnFixtureAccessibilityService`、lab 变体 `ownFixtureAccessibility` 开关、`FixtureReceiptStore`）+静态隔离门 `check_accessibility_manifest.py`；**仍非系统 AccessibilityService 宿主**（own fixture Views 范围内），设备证据 NOT_RUN 维持，不升级 | T2.4（core:accessibility + 5 原子操作 + 节点树调试页）+ SR-13/G12 | 独立实现（真机无障碍为我方主线）；借鉴⑧变体隔离承载 lab 工具 |
| WTO-007（P1） | 授权的真机→自有后端连通与队列/ACK/撤销实测缺失 | 无增量（backend-go 自认 "no service/PG is started"；无网络面）。对照：我方 W-M3R 已有 D1 真机配对/hello+30x 干跑证据 | T2.2/T2.3（协议+网关联调：真机 D1 收发指令）+ T2.5（RIG 通道） | 独立实现（S1/D1 链路为我方独有资产，06 §4④） |
| WTO-008（P1 before production callbacks） | 生产密钥 provider、HTTPS 目的地策略、保留期、运维监控未做（当前回调仅合成） | **代码增量（bab1a3c）**：admission 提交图含 F≤4 callback reservations（回调预留计数，与结果同事务）——仅为**预留**，生产密钥/provider/HTTPS 目的地策略仍未做，不升级 | FR-35（回调分发）+ T3.2 | 独立实现（设计⑤已吸收，生产四项补齐列入 T3.2 验收） |
| WTO-009（Evidence maintenance） | 99 项历史 PRD NOT_RUN 保持原样+追加断言级当前证据；247 条交叉引用≠247 个特性 | **反向注记**：其 status.md 未随 +10 commits 更新（backend-go/无障碍/工具三连的证据等级均未登记），违反其自身"追加断言级当前证据"口径——如实记录，不代其登记；backend-go 局部证据（14 组真 PG CI on bab1a3c）仅存在于 docs/ci-pg-integration.md 行文 | docs/05 规则 + 04 §5.1 Release Gate | 已吸收（⑥：我方矩阵图例/证据规则与其同源；测试计数按当前源码重跑） |

## 4. 维护规则

1. **快照刷新**：Codex 线每次推进后，由主会话以新快照 hash 重读 status.md/coverage.csv/TEST-REPORT 三件套，仅更新本表受影响单元格；快照 hash 记录在文首，旧结论不删除而是在单元格内标注"（快照 X 时）"。
2. **单向引用**：本表只引用不复制——其边表/Schema 细节以 06 §3 方式归口到我方 `protocol/`，禁止在本表复述造成二次漂移。
3. **结论升级禁令**：任何单元格从 NOT_RUN/SYNTHETIC_E2E_TESTED 改为更强表述，必须附其新证据文件路径与原文引用；无证据即标【未核对】。
4. **新增 FR/SR**：先入 01 需求表（AUD 流程），再在本表补行；两线任一侧新增能力目录（CMD/capabilityId）同样如此。
