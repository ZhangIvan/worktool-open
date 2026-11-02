# 系统架构 v2（WorkTool-Open）

- 版本：v2.2（2026-11-02，用户审阅意见落地）；协议事实依据：`local-private/audit/02` §A（WS 信封还原）

## 1. 总体拓扑

```
┌────────────────────────── 用户内网（默认无公网依赖） ──────────────────────────┐
│                                                                              │
│  [Android 真机 D1]            [服务器 S1 / 任意内网主机]                     │
│  ┌────────────────────┐  WSS  ┌──────────────────────────┐   REST(兼容)      │
│  │ WorkTool-Open App  │◄─────►│ worktool-open-server(Go) │◄────── 外部调用方  │
│  │ · accessibility-core│      │ · ws-gateway(心跳/指令/上报)│                  │
│  │ · task-engine(本地) │      │ · scheduler(cron/队列/重试) │  WebHook        │
│  │ · protocol-client   │      │ · config/file/update API   │◄──► QA 回调地址  │
│  │ · storage(加密)     │      │ · admin-api + SQLite/PG    │                  │
│  │ · ocr-fallback(P2)  │      └──────────┬─────────────────┘                  │
│  └─────────┬────────────┘                 │                                    │
│            │ 无障碍驱动                     │ compose 部署                        │
│  [企业微信客户端]◄──────────────────────── └─► [Admin Web(React)] 用户浏览器      │
└──────────────────────────────────────────────────────────────────────────────┘
        出网仅两种可选：① 用户显式配置的 LLM API（FR-36 本地直连）
                      ② 用户显式配置的自有公网服务端（SR-04 白名单）
```

## 2. 运行模式（关键设计）

| 模式 | 组成 | 适用 | 说明 |
|------|------|------|------|
| A 本地模式 | 仅 App | 个人/极简 | 关键词回复、新好友通过、本地 cron、（可选）直连自配 LLM；零服务端、零出网 |
| B 自托管模式 | App + server + admin | 团队/生产 | 全量 FR；API 兼容 apifox 面；回调/AI/多设备 |

两模式同一 App，配置切换；server 不可达时自动降级为 A（FR-46 暂存重发）。

## 3. 端侧模块（android/，Kotlin）

| 模块 | 职责 | 备注 |
|------|------|------|
| `app` | 壳工程、导航、权限引导、悬浮窗控制（FR-50） | 透明命名，无伪装（SR-10） |
| `core:accessibility` | 无障碍服务、节点定位规则引擎（规则外置配置）、原子操作库（点击/长按/输入/滑动/截图，共 5 项） | 上游范式重建；R1 缓解核心 |
| `core:protocol` | WS 客户端、信封编解码（v1 兼容/v2 安全）、心跳重连、AtLeastOnce 队列、离线暂存 | 单测夹具 = protocol/ 文档 |
| `core:task-engine` | 本地规则（关键词/好友通过/群邀请）+ 本地 cron | 模式 A 核心 |
| `core:storage` | Keystore 加密存储、日志脱敏管道 | SR-05/06 |
| `feature:*` | FR-01~FR-33 各指令执行器（按 201~234 对应） | 每指令一执行器+单测；FR-10/13 说明：链接走 UI 自动化，小程序受 R6 约束（WWAPI 剥离后评估替代） |
| `lib:ocr`（P2） | OCR 兜底 | 开源方案评估后定（R5） |
| `lib:updater`（P2） | 自更新客户端（HTTPS+签名校验，SR-07；默认关闭） | FR-44 端侧落位 |

## 4. 服务端模块（server/，Go 单二进制 —— ADR-02 维持有效）

| 模块 | 职责 | 对应 FR | 借鉴（Codex 线设计，见 docs/06/07） |
|------|------|---------|-----------------------------------|
| `cmd/server` | 入口（compose 交付，TLS 默认，明文仅内网显式开） | SR-08/09 | loopback 默认、对外绑定强制 TLS |
| `internal/gateway` | WS 网关：会话协商、双向 seq/eventId、心跳、指令下发、结果回收 | FR-01/34 | hello 协商与 seq/去重语义 |
| `internal/scheduler` | cron、指令队列（幂等键/墓碑/execution_unknown 对账/审批/熔断） | FR-37/38/52/53 | state-transitions 表驱动边集、approver 分权 |
| `internal/api` | REST（v1/apifox 兼容**原生支持**）+ 回调分发（HMAC 签名+有界重试+死信） | FR-34/35 | 回调 outbox 与结果同事务 |
| `internal/admin` | 机器人 CRUD、在线状态、日志/审计查询、回调配置 | FR-40~42 | RBAC 五角色（owner/admin/operator/approver/auditor） |
| `internal/store` | SQLite（默认）/Postgres（可选）；结果/事件/审计/outbox 同事务 | — | 事务边界规范 |

## 5. 协议（权威 = 我方 `protocol/`，ADR-08；设计吸收 Codex 线 contracts 成果）

- 通道：`{host}/webserver/wework/{robotId}`（v1 兼容路径）+ v2 安全会话 `/api/v2/device-sessions`（新客户端首选）；v1 心跳 5s，v2 会话心跳 15s / 失联 45s 关闭（参数吸收自 contracts 线）。
- v1 兼容信封：`WeworkMessageListBean{socketType, messageId, meta, apiSend, list|encryptedList, encryptType}`（字段与 201~234/501~509 映射输入 = local-private/audit/02 §A 还原表）。
- **v2 安全增量（精确规格，2026-11-02 用户要求补齐）**，不改 v1 字段、扩展 `meta`：
  - **配对与鉴权**：①控制台生成一次性配对码（6 位，TTL 10 分钟，单次有效）；②设备持配对码 + AndroidKeystore 签名公钥（Ed25519）请求 `POST /pair`，服务端注册公钥并返回 `deviceToken`（随机 256-bit，服务端仅存 hash，端侧 Keystore 加密存储）；③此后每帧 `meta{ts, nonce, hmac}`，`hmac = HMAC-SHA256(deviceToken, commandId|eventId|ts|nonce|payloadDigest)`，`payloadDigest = SHA-256(RFC 8785 规范 JSON)`；④时钟容差 ±30s，nonce 双向 5 分钟去重；⑤凭据轮换默认 30 天（重协商复用签名公钥）；撤销 = `tokenVersion+1` + 广播 REVOKED，离线设备重连时版本校验失败强制重配对。
  - **投递语义**（吸收 contracts 线）：双向每会话 seq 严格递增 + eventId 去重（重复同内容回执、异内容冲突关闭）；API 幂等键 `tenant+actor+operation+key`（24h 窗口，同键变参 409；键构成对齐 contracts 线 storage 主键语义）；设备墓碑 ≥7 天（> TTL+幂等窗+时钟偏差）；execution_unknown 只凭持久证据对账（5.1）。
  - **加密**：`encryptType=2` 时 AES-256-GCM；配对报文中设备**同时提交 X25519 临时公钥**（与 Ed25519 签名公钥分列两字段），会话密钥 = ECDH(X25519) + HKDF-SHA256 派生（修复上游硬编码 CBC，SR-02；字段随 T2.1 schema 冻结）。
- 规格载体：`protocol/`（spec.md + JSON Schema + state-transitions.csv + 夹具，T2.1 交付）；范式吸收自 contracts 线但**独立成文**，避免跨线引用漂移。

### 5.1 执行状态机与结果未知恢复（用户审阅意见，2026-11-02）
- 状态机唯一权威 = 我方 `protocol/state-transitions.csv`（表驱动边集，范式吸收自 contracts 线；禁止在计划文档复述边表造成漂移）；终态策略随 schema 冻结。
- execution_unknown 恢复三原则：①只凭持久证据对账（设备 journal / reconcile_unstarted 类边），禁止超时推断成败；②非幂等指令永不自动重试；③L1/L2 危险指令存在 unknown 待核时，冻结同类新指令（与 FR-53 联动）。
- 设备侧：执行门禁与去重持久化先于副作用；崩溃恢复 = 端侧 SQLite journal 重放 + 最小命令 epoch 拒旧。
- 服务端：UNKNOWN 对账任务（周期扫描 + 重连触发）；结果/事件/审计/outbox 同事务；死信转人工。

### 5.2 离线/在线任务冲突（用户审阅意见，2026-11-02）
- 事实源分工：定时调度属服务端；端侧仅事件触发规则（关键词/好友通过）+ 模式A本地 cron（仅当"server 不可达超阈值 或 纯本地模式"时启用，配置 `fallbackLocalScheduling`，默认关）。
- 优先级：**用户接管 > 服务端指令 > 本地规则**；暂停态（FR-52）下在线指令同样只入队。
- 重连合并：端侧已执行事件以 `source=local` 上报，服务端按 (ruleId, eventHash) 去重；跨 session 去重靠 commandId 墓碑；乱序 seq 拒绝并触发重新同步；API 幂等键窗口内同键重试安全。

## 6. 安全架构（威胁→对策）

| 威胁 | 对策 | 需求 |
|------|------|------|
| 中间人（上游 P0：trust-all + 明文 + 裸 IP） | TLS 强制+系统校验；移除明文回退默认值 | SR-01/04/09 |
| 伪造指令/回调 | deviceToken+HMAC+时间戳；回调出站带签名 | SR-08 |
| 服务端被攻破 | 端侧仅执行白名单指令类型；敏感配置端侧加密存储 | SR-05 |
| 恶意更新（上游 P0：服务端下发 APK 直装） | 更新默认关；HTTPS+签名校验后才提权安装 | SR-07 |
| 数据外泄 | 零遥测、白名单出网、日志脱敏、消息不落服务端磁盘（仅队列转发，可配保留期） | SR-03/04/06 |
| 账号风控 | 速率默认限制+随机延迟；文档风险告知 | R2 |

### 6.1 数据安全架构（用户指令"服务架构与数据安全架构做好审计处理"，随 AUD-02 复核）

| 域 | 策略（我方 Go 线执行） | 参考现状（Codex 线，取长补短输入） |
|----|------|------------------|
| 传输 | 全链路 TLS（loopback 原型例外）；凭据仅 Authorization header，禁 query/URL/日志 | ✅ 已实现（contracts 约束） |
| 静态存储 | 敏感正文最小化保留（保留期可配，默认 30 天）→ 加密存储路线；逻辑删除 ≠ 擦除（WAL/备份残留处置文档化） | ⚠️ 原型 SQLite 明文（其 README 自认）；SR-14 跟踪 |
| 凭据生命周期 | 服务端仅存 hash；端侧 AndroidKeystore；30 天轮换；撤销即时失效 | ✅ hash/Keystore 已实现；轮换为本版补充项 2 |
| 泄露面收敛 | 回调仅 metadata/referenceId；错误 details 不 echo 内容；导出需多主体审批 + 一次性 download-grant | 部分：审批已实现；download-grant 未实现（WTO-002） |
| 遥测与日志 | 零遥测（SR-03）；服务端日志脱敏（SR-06） | ✅ 默认无遥测；日志脱敏需随部署固化 |

## 7. 部署（S1 为例）
- `infra/compose.yaml`：server（TLS 卷挂载）+ 可选 postgres + 可选 registry。
- 构建：CI 或 S1 构建容器 → `dist/app/*.apk`、`dist/server/*.tar` + sha256（P4）。
- 设备：D1 经 RIG 安装 APK；server 地址扫码/手动配置进入模式 B。

## 8. 与上游/闭源版差异（架构级）
| 维度 | 上游 2.8.1 | 用户闭源版 3.9.x | 本项目 v2 |
|------|-----------|------------------|-----------|
| 服务端 | 厂商云（闭源） | 厂商云+容灾 IP | 开源自托管 |
| 鉴权 | robotId 明文 | +账号/token（登录厂商） | 配对码+deviceToken+HMAC |
| 加密 | 硬编码 AES-CBC | 同+开关 | ECDH+AES-GCM，默认可关 |
| 遥测 | Umeng/TalkingData | Umeng 默认开 | 零遥测 |
| 更新 | 服务端下发 APK | 同 | 关闭默认+签名校验 |
