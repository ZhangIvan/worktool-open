# 既有代码映射（Existing Code Mapping）v1.0

- 版本：v1.0（2026-11-02）
- 目的：**一套 FR/SR 编号映射两条实现线**（我方 Go 线 `zcode/main` ↔ Codex 线 `origin/worktool/synthetic-prototype`，**快照 c83423c**），防止双线各自扩张时语义漂移（用户指令 2026-11-02；依据 ADR-07/08）
- 取证方式：Codex 线文件以 `git -C <仓库> show origin/worktool/synthetic-prototype:<path>` 只读（不 checkout、不 merge）
- **诚实性约定**：①凡无法从其产物核实的单元格标【未核对】；②引用其证据时保留原始标注（SYNTHETIC_E2E_TESTED / NOT_RUN / NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY / BLOCKED），**不得升级其结论**——其"实现/测试通过"仅指合成本地原型层（其 status.md 原文："A passing build or synthetic test is not proof of real WeCom automation"）；③其 Android 侧证据截至快照仅为"构建通过+静态审查"，设备执行全部 NOT_RUN
- 状态取值（我方 Go 线）：未开始=仅 01 需求行 ｜ 设计中=02/03 已有架构或任务落位 ｜ 已吸收设计=06 §3 对应条目已写入我方规划
- 行动取值：独立实现 ｜ 借鉴设计 X（X=06 §3 编号①~⑧）｜ 等待合流（用户 PR 裁决）

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
| FR-34 | 对外 REST API（apifox 兼容，30+ 指令，v1 原生） | `/api/developer/v1`**隔离面**（明确"not legacy API compatibility"）：仅 203 一个类型，其余 501 CAPABILITY_UNAVAILABLE（error-codes.json 422） | `server/developer/plugin.ts`、`server/developer/schema.ts`、`server/app.ts` | 同 FR-02 证据；其 app token 一次性下发/仅存 SHA256/epoch 轮换实测（synthetic） | 已吸收设计（02 §4 internal/api v1 原生 + ⑤） | 独立实现（v1 兼容为我方原生）；双 API 面（v1 兼容 vs developer 隔离面）取舍→**等待合流**由用户裁决 |
| FR-35 | 机器人消息回调（签名+重试） | 签名回调：HMAC-SHA256(`eventId.timestamp.rawBody`)、±30s、重试 1/2/4s、第 4 次死信、2s 墙钟/64KiB 上限；outbox 与结果同事务；默认禁网、仅字面 loopback 接收器 | `server/developer/callbacks.ts`、`server/developer/plugin.ts`（同事务 enqueue） | 真实 loopback 接收器实测（synthetic，developer-api.test.ts）；生产 HTTPS/密钥 NOT_IMPLEMENTED（WTO-008） | 已吸收设计（⑤，T3.2 DoD 已含） | 借鉴设计⑤ + 独立实现（补生产面：持久密钥、目的地策略、保留期） |
| FR-38 | 定时任务（cron；本地模式端侧可独立跑） | **无对应**（TEST-REPORT："批量和真实定时任务未完成"） | 无 | 未实现（其自认） | 设计中（02 §2/§3 模式A、T3.3） | 独立实现 |
| FR-39 | 关键词自动回复（端侧规则引擎） | **无对应**（无端侧规则引擎） | 无 | — | 设计中（T3.3） | 独立实现 |
| FR-46 | 离线消息暂存与重发 | 语义=其"结果先本地持久化再上传；服务端事务提交后 result_ack 方可清理重传队列；重放收敛不重复执行"（设备-服务端往返，非企微消息暂存） | Android `transport/SqliteJournal.kt`、`transport/OkHttpDeviceLink.kt`、`androidTest/.../TransportPersistenceTest.kt`；服务端 `server/app.ts`/README 重放语义 | Android 侧：instrumentation APK 仅**编译通过**、执行 NOT_RUN（status.md 证据表）；服务端重传收敛负例通过（synthetic，TEST-REPORT） | 设计中（02 §5.1 端侧 journal） | 独立实现；借鉴其"持久化→上传→ACK→清理"次序与同 attempt 重放规则（CONTRACT.md） |
| FR-52 | 用户接管：一键暂停（安全断点停、只入队不执行、三处一致） | 撤销/取消族：`stop_request`（仅停止请求非许可）、`cancel_before_delivery`、accepted 安全点停止、权限撤销增量 policyVersion 阻新调度；Android 暂停门（pause 状态读引擎权威 gate） | `server/app.ts`（requestPolicyStop/dispatch 守卫/1s 扫描）、Android `ui/MainActivity.kt`+`core/NativeCommandEngine.kt` | 服务端：队列取消与安全点停止无合成副作用、凭据撤销阻新会话（synthetic 通过，最终 31 项聚合，tests/e2e-final-rbac-first-run.txt）；Android 暂停修复 ea10a55 的新断言"authored but NOT_RUN"（status.md）；连接态 5s 撤销 SLO"未执行有统计意义验证"（其自认） | 已吸收设计（02 §5.2 优先级：用户接管>服务端指令>本地规则） | 独立实现；借鉴 stop_request/安全点语义（CONTRACT.md R1 refinement：transport_ack≠执行许可） |
| FR-53 | 误发保护（L0/L1/L2 分级+限速+白名单+独立 approver+熔断） | **部分**：approver 分权（file.export 审批：绑定申请人+设备+scope digest，approver 独立且须当前活跃）；派发/执行前复检主体与角色；RBAC 五角色。**无** L0/L1/L2 分级、无接收人白名单、无去重窗、无全局熔断（除登录限流） | `server/app.ts`（exportAllowed/actorAuthorized/commandAuthorized）、`server/storage.ts`（exports CHECK approver≠actor） | REGRESSION-GAPS 五反例修正后独立回归通过（对象级授权/失活复检/owner 只读/自批拒绝）；范围仅 file.export（synthetic） | 已吸收设计（④，FR-53 备注"复用既有审批角色"） | 借鉴设计④ + 独立实现（L1/L2 分级、白名单、熔断为我方增量，其线无对应物） |

## 2. SR 映射表（SR-08/11/12/13/14）

| 我方 SR | 语义 | Codex 线对应物 | Codex 线代码位置 | Codex 线测试/证据 | 我方 Go 线状态 | 行动 |
|---------|------|----------------|------------------|-------------------|----------------|------|
| SR-08 | 服务端鉴权：配对 token+HMAC 防篡改+全链路 TLS | 设备凭据=随机 256-bit、服务端仅存 hash、可轮换撤销（epoch）；一次性 enrollment token；WS 会话 bearer；浏览器 HttpOnly+SameSite=Strict+CSRF；**无逐帧 HMAC**（仅回调出站有 HMAC）；TLS=loopback 例外+对外强制（未部署验证） | `server/app.ts`（enrollment/device-sessions/auth/onRequest loopback 钩子）、`server/storage.ts`（devices.secret 存 hash）；Android `transport/KeystoreCredentialVault.kt` | server.test.ts 4/4 + e2e 14/14（synthetic，verify.txt）；Android Keystore 代码存在但设备 NOT_RUN；生产 TLS 部署未验证 | 已吸收设计（⑦；02 §5 v2：配对码+Ed25519 公钥+逐帧 HMAC+nonce 去重为我方增量） | 独立实现（v2 逐帧 HMAC/ECDH 强于其线；借鉴⑦绑定纪律） |
| SR-11 | 指令参数安全：fileUrl/fileBase64 等外链仅白名单域（默认拒收） | 部分：schema 严格（禁额外字段/任意路径/脚本）；回调目的地=字面 loopback 白名单、默认 501、拒 DNS/私网/重定向/改端口（SSRF 默认拒）；**fileUrl 类参数面尚不存在**（首批仅两个合成 payload） | `server/developer/callbacks.ts`（目的地策略）、`contracts/wire.schema.json` + `negative-extra-path.json` | developer-api.test.ts：default SSRF denial、HMAC 篡改/时窗负例（synthetic 通过） | 设计中（G11 门禁已定） | 独立实现 + 借鉴⑤之"字面白名单默认拒"作为我方回调/fileUrl 校验范式 |
| SR-12 | 接管/熔断/审批/撤销事件全量审计留痕 | audit 表 + command_events 事件流，与状态迁移同事务（redacted audit：仅 metadata/referenceId，不落正文）；撤销=stop_request+`command.policy_stop_requested` 留痕；集成审计修正（批准未绑申请人/设备、状态事件漏记）已回归 | `server/app.ts`（audit()/recordTransition()）、`server/storage.ts`（audit/command_events/outbox 同事务） | server.test.ts + e2e 审计断言（synthetic 通过）；REGRESSION-GAPS 集成审计反例已修 | 已吸收设计（02 §5.1"结果/事件/审计/outbox 同事务"） | 独立实现（同事务范式已吸收；④五反例入我方审计用例） |
| SR-13 | 产品实现零 hook（frida/Xposed/注入类仅限 test/D1 lab） | **构建级实现**：lab/debug/release 三变体源集各自 VariantFactory；lab 专属 EndpointPolicy/FixtureCommandPolicy；变体隔离脚本+双端同测 | `android-client/app/src/{lab,debug,release}/.../policy/`、`android-client/tools/verify_variant_isolation.py`、`testDebug/testRelease` OrdinaryVariantIsolationTest、`.github/workflows/android.yml` | 首源 12cb1a3 CI：JVM test/lint 任务运行+静态 APK 审查通过，但"no retained JUnit XML was used to assert an exact test-count"（status.md，计数【未核对】） | 设计中（G12 门禁已定） | 借鉴设计⑧（变体源集隔离=SR-13/G12 的构建级实现方式）+ 独立实现 |
| SR-14 | 静态数据保护：敏感数据最小化/加密存储、逻辑删除的 WAL/备份残留处置文档化 | **未实现**（其自认）：SQLite 明文（"Synthetic payloads are stored in local SQLite plaintext"，README）；逻辑删除 only（deviceEraseState=pending、backupExpiryAt=null）；无保留期 GC；WAL/备份/离线副本"not claimed physically erased" | `server/storage.ts`（deletions 表）、`server/README.md`（Persistence/ADR-001 节） | TEST-REPORT："逻辑删除 endpoint 不证明擦除 WAL/备份/离线副本；本轮没有完整删除恢复验收" | 设计中（02 §6.1 已定最小化保留→加密路线） | 独立实现（两线同缺口，互为印证；其诚实披露的残留面清单可直接并入我方 02 §6.1 审查项） |

## 3. Codex 线 WTO-001~009 差距 → 我方归属映射（其差距编号原文见 docs/status.md，快照 c83423c）

| WTO | 其差距摘要（保留其口径） | 我方归属（需求/任务/文档） | 归属行动 |
|-----|--------------------------|---------------------------|----------|
| WTO-001（P1） | 配置 GET/PUT 路由标注 M1 未实现（其设计盘点 docs/design-baseline.json 内） | FR-40/42 + 02 §4 internal/admin（机器人 CRUD/配置面，T3.2） | 独立实现（我方 admin API 规划内；无需依赖其线） |
| WTO-002（P1） | download-grants/下载路由缺失（绑定授权+一次性/防重放+数据 scope 测试） | 02 §6.1 泄露面收敛（"导出需多主体审批+一次性 download-grant"）+ SR-14 | 借鉴其缺口界定，我方 admin 设计一次性 grant（多主体审批已吸收④）；独立实现 |
| WTO-003（P2） | release/更新路由显式 CAPABILITY_UNAVAILABLE；签名更新验证与回滚未做 | FR-44（自更新默认关+HTTPS+同签名校验）+ SR-07 | 独立实现（v0.1 不排期，边界已明示） |
| WTO-004（P0 for legacy adapter） | 数字目录正式命名空间映射与冲突拒绝测试缺失；"never map 101/102 or another bare numeric constant to an operation" | protocol/ 冻结映射表（audit/02 §A 为源）+ FR-01/34 | 借鉴其规则入我方 protocol/spec（v2 namespaced capabilityId、v1 数字映射仅存冻结表）；独立实现 |
| WTO-005（P1 by capability） | 67 项参考清单=62 项 schema-pending+2 合成 wire+3 控制子集，"不是 67 个已冻结/已实现指令" | docs/05 逐指令支持矩阵 + 01 FR 全表 | 方法论互补：我方以 05 矩阵逐项追踪（⬜→✅ 需真机证据）；吸收 capabilities.json 的 capabilityId 命名为 v2 能力标识 |
| WTO-006（P1） | AccessibilityService 宿主、权限引导、生命周期、事件过滤未实现（首 APK 无 service/receiver 注册） | T2.4（core:accessibility + 5 原子操作 + 节点树调试页）+ SR-13/G12 | 独立实现（真机无障碍为我方主线）；借鉴⑧变体隔离承载 lab 工具 |
| WTO-007（P1） | 授权的真机→自有后端连通与队列/ACK/撤销实测缺失 | T2.2/T2.3（协议+网关联调：真机 D1 收发指令）+ T2.5（RIG 通道） | 独立实现（S1/D1 链路为我方独有资产，06 §4④） |
| WTO-008（P1 before production callbacks） | 生产密钥 provider、HTTPS 目的地策略、保留期、运维监控未做（当前回调仅合成） | FR-35（回调分发）+ T3.2 | 独立实现（设计⑤已吸收，生产四项补齐列入 T3.2 验收） |
| WTO-009（Evidence maintenance） | 99 项历史 PRD NOT_RUN 保持原样+追加断言级当前证据；247 条交叉引用≠247 个特性 | docs/05 规则 + 04 §5.1 Release Gate | 已吸收（⑥：我方矩阵图例/证据规则与其同源；测试计数按当前源码重跑） |

## 4. 维护规则

1. **快照刷新**：Codex 线每次推进后，由主会话以新快照 hash 重读 status.md/coverage.csv/TEST-REPORT 三件套，仅更新本表受影响单元格；快照 hash 记录在文首，旧结论不删除而是在单元格内标注"（快照 X 时）"。
2. **单向引用**：本表只引用不复制——其边表/Schema 细节以 06 §3 方式归口到我方 `protocol/`，禁止在本表复述造成二次漂移。
3. **结论升级禁令**：任何单元格从 NOT_RUN/SYNTHETIC_E2E_TESTED 改为更强表述，必须附其新证据文件路径与原文引用；无证据即标【未核对】。
4. **新增 FR/SR**：先入 01 需求表（AUD 流程），再在本表补行；两线任一侧新增能力目录（CMD/capabilityId）同样如此。
