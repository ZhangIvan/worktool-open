# admin/

v0.1 管理端**最小面**（W-M4 任务卡）：Go 直接伺服的单页 HTML + 原生 JS，**零 npm、零构建链**（无 React/TS，docs/09 §C 登记项按本形态交付；FR-40 最小面，FR-41/42 与 RBAC 五角色归 v0.2+/W-M5）。

能力：配对码签发 / 设备在线状态 / 指令审计流（含 dryRun 标记）/ L2 审批按钮。

## API 面（`server/internal/admin`）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/admin/pairing-codes` | 签发配对码（6 位数字/TTL 10min/单次有效，spec §5.1.1）——**替代 DB 手工 INSERT 引导**；落 `pairing_codes` 表（仅 SHA-256 hash），由 `POST /api/v2/pair` 原路径原子消费 |
| GET | `/api/v1/admin/devices` | 设备列表 + 在线状态（`robotId` 为空 = 未绑定/被重配对清空，如实展示） |
| GET | `/api/v1/admin/commands?limit=&state=&target=` | 指令审计流（时间倒序，`limit` 1..500 默认 50；每条含 `dryRun` 标记，FR-54） |
| GET | `/api/v1/admin/approvals?status=` | 审批列表（默认 `pending`；审批按钮的数据面） |
| POST | `/api/v1/admin/approvals/{approvalId}/decision` | 审批决议：体 `{approver, decision:"approve"\|"reject", reason}`；approver 必须独立于申请人（自批拒绝 403，FR-53）；仅 pending 可决议（409）；决议入审计（SR-12） |
| GET | `/admin/` | 本目录 `index.html`（经 `Deps.UIDir` 挂载） |

> `GET /api/v1/admin/approvals` 是四端点之外的最小必要增量：UI 审批按钮需要发现 pending 审批（approvalId 不为外界所知）。已在车道结果中登记，CR 可复核。

## 鉴权（Bearer admin token）

- 复用 `api_tokens` 表（FR-34 兼容面同表）+ **admin scope**：表无独立 scope 列（schema 归 store 车道），以保留主体名 `robot_id='admin'` 编码——该值非合法 v2 deviceId（spec §5.1.2 pattern ≥8 字符），不与真实设备碰撞。保留名常量 = `sqlite.ReservedAdminScopeRobotID`（api/admin 两包共享，防语义漂移；CR ADM-1）。
- **仅存 SHA-256 hash**（spec §5.1.3 同纪律），原始令牌一次返回；admin 令牌不得用于 `/openapi/v1` 业务面——**服务端强制**（非告知）：兼容面签发（`IssueOpenAPIToken`）与鉴权（`authenticate`）两侧硬拒保留名 `admin`（对 `robotId=admin` 寻址，admin 令牌的 `t.RobotID` 与路径**恰好匹配**，须显式拒绝），反例测试见 `admin_test.go TestAdminTokenRejectedOnOpenAPIFace`。
- 失败一律 401 `INVALID_TOKEN`（不区分不存在/已吊销/非 admin，防探测分类）。

### 首个管理令牌（三选一）

1. **SQL 一次落 hash**（服务器本机执行；`<TOKEN>` 自选随机 256-bit hex）：
   ```sql
   INSERT INTO api_tokens(token_hash, robot_id, tenant, status, created_ms)
   VALUES (lower(hex(sha256('<TOKEN>'))), 'admin', 'default', 'active', cast(strftime('%s','now') as integer)*1000);
   ```
   注：SQLite 的 `sha256()` 需 3.43+ 且启用扩展；最稳是本机先算 `sha256hex(token)` 再代入，或用方式 2/3。
2. **`admin.Server.IssueAdminToken(ctx)`**（装配处/CLI 调用，原始值一次返回并打印）。
3. 等集成方在 `cmd/server` 增 `-issue-admin-token` 一次性引导 flag（见下「接线」）。

## UI 用法（`index.html`）

1. 浏览器打开 `https://<server>/admin/`（TLS 部署，SR-01/09）。
2. 顶部粘贴管理令牌 →「保存」（存 localStorage）。支持 `#token=<令牌>` 深链吸收（吸收后立即从地址栏抹除）；**不要分享带令牌的 URL**。
3. 配对码：点「签发配对码」→ 6 位码 + 倒计时展示，填入 App 配对面。
4. 审批：先填「决议人」（≠ 申请人，自批会被 403 拒绝）→ 每行「通过/拒绝」。
5. 设备/审计流可开 5s 自动刷新；页面零第三方资源（无 CDN/字体/统计，G3/G4 纪律）。

## 接线（集成方须知，W-M4 集成步骤）

本包路由默认**不挂载即不可达**。在 server 主装配（`cmd/server` 或 `internal/api` 装配处）：

```go
adm := admin.New(admin.Deps{
    DB: db, Pipeline: pipeline,
    UIDir: "<repo>/admin",                       // 伺服 /admin/ 静态页
    Online: func(target string) bool { ... },    // 可选：网关活连接探测
})
adm.Register(mux)                                // 或 mux.Handle("/api/v1/admin/", …)
```

- **在线状态**：`Online` 不注入则设备列表恒 `online:false`（诚实降级）。真实探测需网关车道为 `Hub` 增设只读快照（如 `OnlineTargets() map[string]bool`，返回 `v2ByDev`+`v1` 键集）后注入闭包——不建议用 `device_sessions.state='open'` 兜底（崩溃后陈旧行不清，误报在线）。
- 测试：`go test -C server ./internal/admin/`（httptest 四端点正反例：未授权 401、自批拒绝 403、重复决议 409、干跑标记、UI 伺服）。

## 边界与后续

- RBAC 五角色（owner/admin/operator/approver/auditor，docs/02 §4）、FR-41 队列管理/FR-42 回调配置、令牌生命周期管理（吊销面）→ v0.2+（W-M5）。
- 审计流仅元数据（不含指令正文），与回调出站收敛口径一致（docs/02 §6.1）。
