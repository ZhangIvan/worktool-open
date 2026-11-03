# admin/

v0.1 管理端（W-M4 最小面 → **W-P0 完整管理后台**）：Go 直接伺服的单页 HTML + 原生 JS，**零 npm、零构建链**（无 React/TS，docs/09 §C 登记项按本形态交付；FR-40 最小面升级为日常运维工具，FR-41/42 与 RBAC 五角色归 v0.2+/W-M5）。

UI：单文件 SPA（`index.html`），Hash 路由多视图 + 左侧导航（图标+文字）+ 顶部面包屑；深色主题（`#1e1e2e`）；JSON/日志等宽字体展示；CSS 全内嵌，**零外部资源**（无 CDN/字体/图片/统计，G3/G4）。

## 视图总览（11 视图）

| 视图 | 数据面 |
|---|---|
| 概览 Dashboard | `GET /api/v1/admin/overview`（在线设备/今日指令/错误率/待审批/排队/待投递 + 最近回调 10 条） |
| 设备管理 | `GET …/devices` + 配对码签发 + `POST …/devices/{id}/pause\|resume\|unbind` |
| 指令发送 | `POST /api/v1/admin/commands`（动态参数表单 + dryRun；L2 → 202 + approvalId 引导） |
| 指令历史 | `GET …/commands?state=&type=&target=&limit=&offset=`（分页/过滤/dryRun 标记）+ 详情 `GET …/commands/{commandId}`（载荷 + E01→E18 轨迹 + 结果 JSON） |
| 队列视图 | `GET …/queue` · 插队 `POST …/queue/{id}/priority` · 移除 `DELETE …/queue/{id}` · 清空 `DELETE …/queue?target=` |
| L2 审批 | `GET …/approvals` + `POST …/approvals/{id}/decision`（自批拒绝 403，FR-53） |
| 回调配置 | `/api/v1/admin/callbacks`（GET/POST/PUT/DELETE）+ `POST …/callbacks/test`（探测） |
| 回调日志 | `GET …/callbacks/logs?status=`（成功/失败/重试次数） |
| 指令执行日志 | `GET …/logs/executions?commandId=&target=`（时间线：E01→E18 状态流转轨迹） |
| 系统审计日志 | `GET …/logs/audit?kind=`（谁在什么时候做了什么，SR-12） |
| 设置 | guard 限流/白名单策略（`PUT …/settings/guard`）、熔断状态+人工复位（`POST …/breaker/reset`）、分级查询（`GET …/risk/{type}`）、L1 白名单 CRUD（`/api/v1/admin/whitelist`）、API Token（`GET …/tokens` 仅 hash 前 8 位 + 签发/吊销）、通讯加密开关（FR-43，`PUT …/settings/encryption`） |

## API 面（`server/internal/admin`，全部 Bearer admin token）

### 沿袭（v0.1 最小面，行为不变）
| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/admin/pairing-codes` | 签发配对码（6 位/TTL 10min/单次有效，spec §5.1.1；仅存 SHA-256 hash） |
| GET | `/api/v1/admin/devices` | 设备列表 + 在线状态 + 最近会话时间（`lastSessionMs`；型号含于 `deviceName`，Android/企微版本配对面未上报，如实标 —） |
| GET | `/api/v1/admin/commands?limit=&offset=&state=&type=&target=` | 指令审计流（时间倒序、分页、total 计数；含 dryRun 标记） |
| GET | `/api/v1/admin/approvals?status=` | 审批列表 |
| POST | `/api/v1/admin/approvals/{approvalId}/decision` | 审批决议（approver 独立、仅 pending、决议入审计） |
| GET/POST/DELETE | `/api/v1/admin/robots/{robotId}/openapi-tokens` | 兼容面令牌生命周期（签发/列表/吊销旋转；**CR ADM-1**：签发体 `{}`/tenant 留空回落 `default`，仅体解析失败 400） |
| GET | `/admin/` | 本目录 `index.html`（经 `Deps.UIDir` 挂载） |

### 新增（W-P0 完整管理后台，`console.go`）
| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/admin/overview` | 概览聚合（今日=服务端 UTC 当天；错误率分母仅真实执行终态，干跑不计） |
| POST | `/api/v1/admin/devices/{deviceId}/{action}` | `pause`/`resume`/`unbind`（见下「语义边界」） |
| POST | `/api/v1/admin/commands` | 指令提交（统一管线 `scheduler.Submit`，非旁路；干跑同步产出报告 FR-54；L2 无审批 → 202 + approvalId）。**CR ADM-2**：`payload` 缺 `type` 字段时服务端注入 `type`（UI 无参数指令默认载荷 `{}` 不再 E02 必败；显式 type 不一致仍 400）。**CR ADM-4**：可选 `idempotencyKey`——调用方生成并重试复用 → 同键同参重放返回原 commandId（与兼容面同一条统一管线的幂等维度对齐）；留空则服务端生成 UUID 并在响应回显 `idempotencyKey` |
| GET | `/api/v1/admin/commands/{commandId}` | 详情：载荷 + command_events 轨迹 + outbox 结果 JSON（载荷/结果仅详情面展示，列表仍仅元数据） |
| GET/DELETE | `/api/v1/admin/queue` | 队列视图 / 清空目标队列（`?target=`；被清指令逐条落 101018 回执，上游 304 语义）。**L2 审批豁免（REV-SRV-05）**：admin scope 即授权决议面（requireAdmin）且落审计——兼容面同语义操作须走 L2 审批门（`api` 包 `requireQueueOpApproval`），本面豁免已在 docs/05 矩阵显式登记 |
| DELETE | `/api/v1/admin/queue/{commandId}` | 移除单条（仅 queued；落 101018 回执，上游 305 语义）。审批豁免登记同上 |
| POST | `/api/v1/admin/queue/{commandId}/priority` | 插队（队列序键抬至队首前，原序键留痕审计） |
| GET/POST/PUT/DELETE | `/api/v1/admin/callbacks` | 回调绑定 CRUD（物理表 = 兼容面 `openapi_callbacks` 单一事实源；绑定硬校验：目标 200 + application/json、默认仅 loopback，SR-09） |
| POST | `/api/v1/admin/callbacks/test` | 探测回调地址（不落库） |
| GET | `/api/v1/admin/callbacks/logs?status=` | 回调投递日志（pending/delivered/dead + 尝试次数/最近错误） |
| GET | `/api/v1/admin/logs/executions?commandId=&target=` | 指令执行日志时间线（E01→E18） |
| GET | `/api/v1/admin/logs/audit?kind=` | 系统审计日志 |
| GET | `/api/v1/admin/settings` | guard/熔断/加密开关/计数汇总 |
| PUT | `/api/v1/admin/settings/guard` | L1 限速/白名单默认策略（即时 `SetGuardConfig` + 持久化 `admin_settings` 表） |
| PUT | `/api/v1/admin/settings/encryption` | FR-43 通讯加密开关（默认关；持久化 + 审计） |
| POST | `/api/v1/admin/breaker/reset` | 熔断人工复位（复位留审计） |
| GET | `/api/v1/admin/risk/{type}` | 实时分级查询（单一事实源 `scheduler.ClassifyRisk`；未登记 L2 fail-closed） |
| GET/POST/DELETE | `/api/v1/admin/whitelist` | L1 接收人白名单 CRUD（scheduler 导出面直通，配置即强制） |
| GET | `/api/v1/admin/tokens` | 全部令牌只读投影（hash 前 8 位 + scope/租户/状态；原始值永不出现在响应） |

> 自治 schema：`admin_settings`（KV：guard 配置、加密开关）与 `openapi_callbacks`（与 api 包同一物理表，两侧 `CREATE IF NOT EXISTS` 幂等共建）——经 store 公共原语建表，不动 store 包（api 包 openapi_callbacks 同先例）。

## 鉴权（Bearer admin token）

- 复用 `api_tokens` 表 + **admin scope**：保留主体名 `robot_id='admin'`（`sqlite.ReservedAdminScopeRobotID`，非合法 v2 deviceId，不与真实设备碰撞；CR ADM-1：兼容面签发/鉴权两侧硬拒保留名）。
- **仅存 SHA-256 hash**（原始令牌一次返回）；失败一律 401 `INVALID_TOKEN`（防探测分类）。

### 首个管理令牌（三选一）

1. **SQL 一次落 hash**（服务器本机执行；`<TOKEN>` 自选随机 256-bit hex）：
   ```sql
   INSERT INTO api_tokens(token_hash, robot_id, tenant, status, created_ms)
   VALUES (lower(hex(sha256('<TOKEN>'))), 'admin', 'default', 'active', cast(strftime('%s','now') as integer)*1000);
   ```
2. **`admin.Server.IssueAdminToken(ctx)`**（装配处/CLI 调用，原始值一次返回并打印）。
3. 等集成方在 `cmd/server` 增 `-issue-admin-token` 一次性引导 flag（见下「接线」）。

## UI 用法（`index.html`）

1. 浏览器打开 `https://<server>/admin/`（TLS 部署，SR-01/09）。
2. 顶栏粘贴管理令牌 →「保存」（localStorage）；支持 `#token=<令牌>` 深链吸收（吸收后立即从地址栏抹除）。
3. 指令发送：选类型（目录带 L0/L1/L2 分级）→ 动态参数/JSON → 可勾干跑；L2 提交返回审批号，到「L2 审批」决议后携号重交。提交按钮响应返回前禁用 + 自动幂等键（失败重试同键重放不重复入队，CR ADM-4）；无参数类型默认载荷 `{}` 经 UI/服务端双层注入 `type`（CR ADM-2）。
4. 指令历史/日志各视图支持过滤分页与 5s 自动刷新（勾选即启、取消即停，CR ADM-3 修复反复勾选叠加定时器）；指令详情含结果 JSON 与 E01→E18 时间线。
5. 设置 → API Token：tenant 留空 = 默认租户 `default`（CR ADM-1：不再因空租户 400）。

## 语义边界（如实登记，不夸大）

- **设备暂停**：`devices.status='paused'` 的强制执行点 = **管理面提交门**（`POST …/admin/commands` 对 paused 设备 409 `DEVICE_PAUSED`，v1:robotId 寻址同拦）。网关连接层/兼容面路径不检查该状态——连接级强制待网关车道接线（dodDeferred）。
- **设备解绑**：`robot_id` 置空（v1 兼容面寻址与回调路由随之失效；与重配对清空同语义）。
- **型号/Android 版本/企微版本**：配对面（`POST /api/v2/pair`）不上报这些字段，devices 表亦无此列——UI 如实标「未上报」，不代填不猜测。`lastSessionMs` = 最近一次会话痕迹（≈最近 hello），非逐帧心跳。
- **回调启用状态**：绑定即启用、删除即停用（`openapi_callbacks` 无独立 enabled 列；加列会造成兼容面无视该列的"假停用"，不做）。
- **FR-43 加密开关**：持久化管理面意图（默认关）+ 审计；握手协商的强制执行属网关车道（dodDeferred）。
- **熔断参数**：状态实时 + 人工复位可用；参数调整 `scheduler.Breaker` 未导出入口（展示默认值，dodDeferred）。
- **审计流列表仅元数据**（不含指令正文，docs/02 §6.1）；载荷与结果 JSON 仅在授权 admin 的单条详情面展示。

## 接线（集成方须知）

本包路由默认**不挂载即不可达**。在 server 主装配（`cmd/server` 或 `internal/api` 装配处）：

```go
adm := admin.New(admin.Deps{
    DB: db, Pipeline: pipeline,
    UIDir: "<repo>/admin",                       // 伺服 /admin/ 静态页
    Online: func(target string) bool { ... },    // 可选：网关活连接探测
    AllowNonLoopbackCallbacks: false,            // 默认仅 loopback 回调（SR-09）
})
adm.Register(mux)
_ = adm.ApplyPersistedSettings(ctx)              // 启动重放 guard/加密开关设置（可选，建议）
```

- **cmd/server 尚未挂载本包**（W-M4 以来的集成步骤，归装配/集成棒）：挂载前管理面不可达（默认不暴露，攻击面纪律）。
- **在线状态**：`Online` 不注入则恒 `online:false`（诚实降级）。真实探测需网关车道为 `Hub` 增设只读快照（如 `OnlineTargets()`）后注入闭包——不建议用 `device_sessions.state='open'` 兜底（崩溃后陈旧行不清，误报在线）。
- 测试：`go test -C server ./internal/admin/`（httptest：四端点正反例 + 概览聚合/设备暂停门/提交审批流/详情轨迹/队列操作/回调 CRUD/日志查询/设置面 + UI 伺服与零外部资源断言）。

## 边界与后续

- RBAC 五角色（owner/admin/operator/approver/auditor，docs/02 §4）→ v0.2+（W-M5）。
- 概览聚合端点归 Lane 1（服务端聚合）联动口径：本包 `overview` 为管理面自持聚合（QueryDump 只读投影，不落第二事实源）；若 Lane 1 另立聚合端点，CR 合并时以单一事实源对齐。
- 网关车道接线点：连接层设备 paused 强制 / FR-43 加密协商强制 / Hub 在线快照注入。
