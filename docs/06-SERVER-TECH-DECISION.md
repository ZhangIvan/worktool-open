# 服务端技术决策对照（Server Tech Decision）v1.0

- 版本：v1.0（2026-11-02）
- 对比对象：**我方 Go 线**（分支 `zcode/main`，ADR-02：Go 单二进制 + SQLite 默认/Postgres 可选）↔ **Codex 线**（分支 `origin/worktool/synthetic-prototype`，**快照 c83423c**，2026-11-02 证据截止；Node 24.22.1（`engines >=24.22.1 <25`、`.node-version` 钉版）+ Fastify 5.12.7 + `node:sqlite` 内置 DatabaseSync + React19/Vite8 控制台）
- 取证方式：Codex 线文件一律 `git -C <仓库> show origin/worktool/synthetic-prototype:<path>` 只读，**不 checkout、不 merge**；本文所有引文保持其原始标注（SYNTHETIC_E2E_TESTED / NOT_RUN / NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY / BLOCKED），不得升级其结论
- 关联：决策依据 ADR-07/ADR-08（`00-MASTER-PLAN.md` §6）；任务化落地 `03-EXECUTION-PLAN.md` v2.2；逐 FR/SR 映射见 `07-EXISTING-CODE-MAPPING.md`

## 1. 定位声明（前提，不可被对照结论推翻）

1. **双线并行、各持栈**：`main` 线（Codex，Node/Fastify 合成原型）与 `zcode/main` 线（本助手，Go 服务端 + 完整规划）各自独立演进；用户 2026-11-02 裁决原文："我方继续用 Go，不要被 Codex 影响""取长补短，不是全面否定自己的"。
2. **取长补短 ≠ 换栈**：跨线复用只以"设计/schema 借鉴"方式进行（ADR-07）；本文 §3 吸收清单全部是**设计层输入**，落地为我方 `protocol/` 与 Go 模块（ADR-08），不引入 Node 运行时依赖。
3. **合流由用户以 PR 裁决**：任何一条对照结论都不构成单方面弃用任一栈的依据；合流时机与评估维度见 §5。
4. **证据边界**：Codex 线自认"合成本地原型"——两动作（message.send_text / file.export）SYNTHETIC_E2E_TESTED，Android/真实企微全部 NOT_RUN（其 `docs/status.md`、`artifacts/TEST-REPORT.md` 原文）；对照表"测试资产现状"按此口径评分，不视为生产可用性。

## 2. 技术栈对照表（Go 单二进制/SQLite vs Node24+Fastify5/SQLite）

| 维度 | 我方 Go 线（ADR-02） | Codex 线（快照 c83423c 实况） | 谁占优 | 影响 |
|------|---------------------|------------------------------|--------|------|
| 部署复杂度 | 静态单二进制 + compose；交叉编译一条命令；无运行时依赖 | 需钉版 Node24.22.1（`engines <25` 升级即摩擦）+ lockfile 安装 + 控制台 Vite 构建；容器化可拉平但镜像层级多一层运行时 | **Go** | 内网单机（S1/用户自托管）交付 scp 即用；Codex 线交付需固化 node 基础镜像版本 |
| 资源占用 | 无 VM 常驻，RSS 低（Go runtime 数 MB 级） | V8 常驻基线更高；`node:sqlite` 同步驱动在事件循环线程内执行，长事务会阻塞全部连接 | **Go** | 2C4G/50 机器人目标（NFR §2.2）两线均可达成，Go 余量更大；Codex 线需把重查询移出主循环 |
| 冷启动 | 毫秒级进程启动，崩溃恢复快 | 模块加载 + JIT 预热；且其设计**重启主动吊销全部浏览器会话**（server/README），重启代价语义更高 | **Go** | 滚动升级/崩溃恢复场景 Go 停机窗口小 |
| 并发模型 | goroutine 多核并行，长连接网关天然分流；channel 串行化关键路径 | 事件循环单线程：共享状态竞态心智简单（其端侧 Serial Executor 纪律可借鉴），但多核利用需 worker/cluster，WS+同步 SQLite 同线程存在阻塞点 | **Go**（我方场景=多设备长连接网关）；Node 的"单线程免锁"心智列为可借鉴项 | M3 网关（T2.3）按 goroutine/连接设计；Codex 线如扩规模需架构性改动 |
| 类型安全 | 编译期强类型；struct+code gen 即 schema | TS strict（`tsc --noEmit` 入 `npm run verify`）+ Ajv2020 运行时 JSON Schema + RFC8785 canonicalize，双层纪律成熟 | **平手**（Go 编译期更强；Codex 线"运行时 schema=事实源"文化恰是我方 protocol/ 夹具要补的一层） | 我方吸收其"schema 即测试夹具"做法：`protocol/` 文档直接驱动 Go 单测（01 §2.3 已定） |
| 测试资产现状（截至今日） | **0 项**——编码未开始（`server/`、`protocol/` 仅有骨架/README），资产=已通过审计的规划文档（00~05） | 最终 31 项聚合 + 开发者片 28 项本地验证【计数未核对：运行日志未随源码公开】+ 独立复核回归（REGRESSION-GAPS 五反例）；负例覆盖深（幂等变参/跨租户/CSRF/失活复检/撤销/重传收敛） | **Codex 线**（现状） | 其负例清单直接转为 我方 Go 测试大纲（吸收⑥，见 §3） |
| 生态依赖面 | Go modules 少依赖、无 install 脚本执行面、`go.sum` 可审计 | npm：16 个直依赖（fastify/ws/ajv/canonicalize/react/vite…）+ 传递面；其自律良好（锁版+`--ignore-scripts`+固定 SHA actions+dependency-audit 0 已知告警），但 npm 供应链历史攻击面大 | **Go** | 自托管用户依赖审计成本低；Codex 线做法（禁生命周期脚本）值得我方 CI 对照 |
| 团队并行性 | 三语言（Go 服务端/Kotlin 端/React 管理端），以 L1~L5 车道制（03 §1）+ JSON Schema 单一事实源对冲 | 全栈同构 TS：console 与 server 同语言、同测试框架（`node --test`）、同仓同 CI | **Codex 线略优**（Web 全栈同构） | 我方维持车道制不变；协议夹具跨语言复用是关键补偿，T2.1 优先级因此提高 |
| 风险 | ①从零实现协议/测试，进度风险集中；②WS 网关+调度部分需自研胶水 | ①Node 大版本钉死（`<25`）；②V8 内存/单线程阻塞；③npm 供应链；④其自认未实现面大：生产回调密钥（WTO-008）、配置路由（WTO-001）、download-grant（WTO-002）、真实执行全 NOT_RUN | **各有风险**（并列，不判胜负） | 双线对冲正是 ADR-07 的目的；各自风险登记入 00 §7 / 其 status.md |

## 3. 我方吸收清单（设计层输入，逐条注明来源；已任务化于 03 v2.2）

| # | 吸收内容 | 我方落点 | 来源文件（Codex 线，快照 c83423c） |
|---|---------|---------|-----------------------------------|
| ① | **表驱动状态机**：唯一允许事件边集 + 终态策略外置，非法组合 409 ILLEGAL_TRANSITION；状态迁移与 command_events/审计同事务 | `protocol/state-transitions.csv` + `state-policy.json`（02 §5.1 已定为唯一权威；计划文档禁止复述边表防漂移）；Go 实现按"边集查表 + 事件留痕"范式（`internal/scheduler`） | `contracts/state-transitions.csv`（32 条边，含 reconcile_unstarted_* 三边）、`contracts/state-policy.json`、`contracts/state-pair-matrix.csv`；实现参照 `server/app.ts` 的 `transition()/allowedEdges` |
| ② | **幂等键窗口**：`tenant+actor+endpoint+key` 唯一，24h 窗口；同键同规范请求返回原响应、变参 409（IDEMPOTENCY_CONFLICT） | 02 §5 投递语义（已写入）；`internal/api` 全部 POST 创建类端点强制 Idempotency-Key（T3.2） | `contracts/CONTRACT.md`（Command identity 节）、`contracts/error-codes.json`、`server/storage.ts` idempotency 表（PK=tenant,actor,operation,key+digest+expires）；教训：其 TEST-REPORT 自认"导出申请 POST 尚无幂等去重"→ 我方规则扩到**所有** POST |
| ③ | **墓碑 ≥7 天 + execution_unknown 证据化对账**：7 天 > 300s TTL + 24h 幂等窗 + 30s 时钟偏差；墓碑不含正文、清理前确认无 unknown 待核；unknown 只凭持久证据 reconcile，禁止超时推断成败、禁止自动重试 | 02 §5.1 三原则（已写入）；`internal/scheduler` UNKNOWN 对账任务（周期扫描+重连触发） | `contracts/CONTRACT.md`（TTL 节、First-slice reliability amendment）、`contracts/state-transitions.csv`（reconcile_* 六边）、`server/app.ts`（重启将 delivered/accepted/running → execution_unknown）、`server/storage.ts` tombstones 表 |
| ④ | **RBAC 五角色与 approver 分权**：owner/admin/operator/approver/auditor；owner 只读不继承业务权；operator 对象级（仅本人命令）；approver 独立且必须当前活跃；创建+派发+执行前三级复检主体活跃性 | `internal/admin` RBAC（02 §4 已列五角色）；FR-53 L2 独立 approver 直接复用（01 FR-53 备注"复用既有审批角色"） | `server/app.ts`（Role 类型、activeOperator/exportAllowed 派发复检）、`server/storage.ts`（users.role CHECK 五值、exports approver≠actor）、`artifacts/REGRESSION-GAPS.md`（五反例）、`server/README.md`（Security review corrections 节） |
| ⑤ | **回调 outbox 与结果同事务 + HMAC 签名**：结果变更在**同一 SQLite 事务**内 enqueue 不可变回调事件；HMAC-SHA256 over `eventId.timestamp.rawBody`，±30s 时间窗，重试 1/2/4s、第 4 次死信；2s 总墙钟上限 + 64KiB body 上限 + 响应流即毁；事件 ID 接收方去重 | `internal/api` 回调分发（FR-35；T3.2 DoD"错误签名拒收、失败重试 ≤3 次"）；出站目的地策略借鉴其"字面白名单、默认拒"（与 SR-11/G11 同构） | `server/developer/plugin.ts`（同事务 enqueue）、`server/developer/callbacks.ts`（HMAC/重试/上限）、`docs/developer-api-slice.md`（路由/头/死信规格） |
| ⑥ | **证据诚实标注规范**：SYNTHETIC_E2E_TESTED / NOT_RUN / NOT_IMPLEMENTED_AS_LEGACY_CAPABILITY / BLOCKED 词汇；"修正前测试计数不得当修正后证据"；版本化证据表（源码 hash↔证据↔局限） | docs/05 矩阵图例（⬜=NOT_RUN 未经真机证据不得转 ✅）与 04 §8 Release Gate 同源规则；推广为我方测试报告模板（测试数必须按当前源码 revision 重跑） | `docs/status.md`（Versioned validation evidence 表）、`artifacts/implementation-coverage.csv`（evidence 列逐行标注）、`artifacts/TEST-REPORT.md`（"未通过、未运行与未实现"节、"初始23PASS不得取代最终31项"） |
| ⑦ | **loopback 默认 + 对外强制 TLS 禁明文降级**：本地原型仅 loopback 可用 HTTP/WS；外部绑定必须 TLS；请求钩子直接拒绝非 127.0.0.1 来源 | `cmd/server` 默认绑定（02 §4 已列）；SR-09 实现级增强：单测断言"非 loopback 明文绑定启动即拒绝"（写入 T1.3/T3.2 验收） | `contracts/CONTRACT.md`（Transport 节首句）、`server/app.ts`（onRequest 钩子 127.0.0.1/::1 校验）、`server/README.md`（仅绑 127.0.0.1:3100） |
| ⑧ | **lab/debug/release 变体源集隔离**：每变体独立 VariantFactory + lab 专属 EndpointPolicy/FixtureCommandPolicy；脚本机械校验变体隔离；双端（testDebug/testRelease）同测防泄漏 | 我方 SR-13/G12 的**构建级实现方式**：android/ 采用同构 sourceSets（lab 变体仅存在 D1 动态验证链路、release 最紧、debug 居中）；G12 门禁增补"变体隔离脚本"机械检查项 | `android-client/app/src/{lab,debug,release}/.../policy/VariantFactory.kt`、`src/lab/.../LabEndpointPolicy.kt`、`LabFixtureCommandPolicy.kt`、`android-client/tools/verify_variant_isolation.py`、`testDebug/testRelease` 的 OrdinaryVariantIsolationTest |

## 4. 反向输出清单（我方可供 Codex 线参考；均已在库/规划中，无需其重新逆向）

| # | 输出项 | 内容与位置 | 对 Codex 线的对应缺口 |
|---|--------|-----------|----------------------|
| ① | **v1/apifox 兼容通道与生态** | FR-34：我方 Go 服务端原生 v1 兼容（`WeworkMessageListBean` 信封、`/webserver/wework/{robotId}`、201~234/501~509 数字目录、RAW_CONFIRM 回执、0/10xxx/20xxx/50xxx 错误码）；字段还原表=`local-private/audit/02` §A（结论已沉淀入 protocol/ 规划） | 其明确"不做 legacy API 兼容"（developer-api-slice 首句）；若需存量 apifox 生态接入面，可直接对接我方 v1 通道而非重造 |
| ② | **纯本地模式 A** | 02 §2：零服务端运行（关键词回复/新好友通过/本地 cron/可选自有 LLM 直连），server 不可达自动降级 | 其 device 侧无独立运行模式，设备离线即无动作（其 CONTRACT："设备离线不得启动新敏感动作"） |
| ③ | **Go 单二进制部署体验** | ADR-02：交叉编译 + compose + 健康检查；内网用户 scp 即用；运维面=1 个进程 1 个 SQLite 文件 | 其交付需 Node 运行时钉版管理 |
| ④ | **S1 构建机 / D1 真机（RIG）验证链路** | 00 §3/§5、T1.4/T2.5：构建容器 + registry 分发 + RIG 安装/日志拉取的完整真机证据通道 | 其 WTO-006（无障碍宿主未启用）/WTO-007（真机连接/队列/ACK/撤销未测）正缺此类通道；其 status.md 自认 Android 侧"Device NOT_RUN" |
| ⑤ | **逐指令企微版本支持矩阵方法论** | docs/05：✅/🟡/❌/⬜ 图例 × 企微版本列（4.1.8~5.0.10 基线），证据存 `local-private/evidence/`，"未经真机证据不得从 ⬜ 变 ✅" | 其 WTO-005（67 项=62 schema-pending）可用此矩阵范式逐项推进 delivery_phase，避免"目录保留≠已实现"的口径混淆 |
| ⑥ | **闭源版行为级分析资产** | `local-private/audit/`（APK 静态分析/逆向行为 delta/动态 runbook 23 端点用例；涉密不出本地，P7）——公开结论已沉淀为 01 FR 表（FR-45~51 delta 项）、05 矩阵、R5/R6 边界 | 其做真机适配时引用我方公开矩阵与需求表即可，无需重复逆向；其 67 项目录与我方 FR 表的对照见 docs/07 |

## 5. 合流时机与评估维度

- **时机**：v0.1 双线**各自出包后**（我方=M4/W52 Release v0.1.0；Codex 线=按其里程碑出可运行包），由**用户以 PR 裁决**是否/如何合流；此前双线仅通过 docs/06、docs/07 维持语义对照，不做代码级合并。
- **评估维度**（建议用户届时按此三轴打分）：
  1. **协议一致性**：`contracts/wire.schema.json + state-transitions.csv` ↔ 我方 `protocol/`（v1 信封 + v2 会话）的语义 diff——状态机边集可机器 diff 两份 CSV；错误码表（error-codes.json ↔ 我方 0/10xxx/20xxx/50xxx）映射完备度；v1 兼容面是否只由我方单侧承载。
  2. **测试并集**：两线测试资产能否互为套件——我方 protocol/ 夹具喂给其 Ajv 校验器、其负例清单（REGRESSION-GAPS 五反例 + TEST-REPORT 负例）转为我方 Go 用例；真机证据只认 D1 链路（我方独有）；测试计数以各自当前源码 revision 重跑为准（吸收⑥）。
  3. **运维成本**：运行时依赖（Node24 钉版 vs 静态二进制）、镜像体积/内存实测、升级路径（其重启吊销浏览器会话的语义成本）、2C4G 单机 50 机器人容量实测。
- **默认倾向**：不合流也能各自发布（双产品面），合流仅当三轴中至少两轴显著收益；任何合流方案都必须保留 v1/apifox 兼容通道（FR-34 生态承诺）。

## 6. 事实底稿（本次对照实际读取的 Codex 线文件）

`contracts/CONTRACT.md`、`contracts/capabilities.json`、`contracts/state-transitions.csv`、`contracts/state-pair-matrix.csv`、`contracts/state-policy.json`、`contracts/error-codes.json`、`artifacts/implementation-coverage.csv`、`artifacts/REGRESSION-GAPS.md`、`artifacts/TEST-REPORT.md`、`docs/status.md`（WTO-001~009）、`docs/developer-api-slice.md`、`android-client/INTERFACES.md`、`server/app.ts`（头部+核心路由）、`server/README.md`、`server/storage.ts`（DDL）、`package.json`、`.node-version`、`docs/ci.md`。我方：`docs/00~05`、`protocol/README.md`。
