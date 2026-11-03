# server/

WorkTool-Open 调度后台（Go 单二进制；SQLite 默认 / Postgres 可选；compose 交付）。

- 模块规划：`docs/02-ARCHITECTURE.md` §4（gateway / scheduler / api（apifox 兼容 + 回调分发）/ admin / store）
- 任务卡：T1.3（骨架）、**T2.3（服务端核心，本版交付）**、T3.2（REST 兼容层+调度细化）、T3.4（协议 v2 收尾）
- 部署目标：192.0.2.10（`infra/` 下 compose；构建产物落 `dist/server/`）
- 协议权威：`../protocol/`（spec.md + schema + state-transitions.csv + 夹具；本目录不复制语义，仅嵌入 CSV/policy 副本并带同步测试防漂移）

## 运行

```bash
cd server
go run ./cmd/server                       # 默认 127.0.0.1:8080，/healthz /readyz
go run ./cmd/server -listen 0.0.0.0:8443 -tls-cert cert.pem -tls-key key.pem   # 对外绑定必须 TLS
go run ./cmd/server -db worktool.db \
  -callback-url http://127.0.0.1:9990/callback -callback-secret <HMAC密钥>      # 回调分发（默认禁外网）
```

配置来源优先级：默认值 < 环境变量 < 命令行 flag。

| 项 | flag | 环境变量 | 默认 |
|---|---|---|---|
| 监听地址 | `-listen` | `WT_LISTEN` | `127.0.0.1:8080` |
| TLS 证书/私钥 | `-tls-cert` / `-tls-key` | `WT_TLS_CERT` / `WT_TLS_KEY` | 未启用 |
| SQLite 库 | `-db` | `WT_DB` | `worktool.db` |
| 回调接收器 | `-callback-url` | `WT_CALLBACK_URL` | 空（禁投递） |
| 回调签名密钥 | `-callback-secret` | `WT_CALLBACK_SECRET` | 空 |
| 放行非 loopback 回调 | `-allow-nonloopback-callback` | `WT_ALLOW_NONLOOPBACK_CALLBACK` | false |

**SR-09（启动即拒绝）**：非 loopback 绑定且未启用 TLS → 监听前退出；回调 URL 非 loopback
且未显式放行 → 启动即拒绝（默认禁外网，仅 loopback 接收器）。

## T2.3 交付面

| 模块 | 内容 |
|---|---|
| `internal/wire` | v1/v2 信封编解码、JCS(RFC 8785)、逐帧 HMAC（C-9 输入式）、X25519+HKDF+AES-256-GCM；金标准向量 `protocol/fixtures/hmac-vector.json` 逐字节复算单测（关 AUD-03 C-4 L2 侧） |
| `internal/statemachine` | `state-transitions.csv` 表驱动（嵌入副本 + 权威件同步测试）；CSV 全边正反例 |
| `internal/store/sqlite` | 免 CGO SQLite（modernc.org/sqlite v1.34.4）：嵌入式迁移、幂等键/墓碑/outbox/审计/入向 eventId 去重、同事务接口 |
| `internal/scheduler` | 统一命令管线 E01~E18 全边落地：幂等键（同键变参 409）、FR-53 风险冻结、ACK TTL 扫描（E17/E18，C-10）、崩溃恢复（E06/E07/E08 → execution_unknown）、journal 对账（E12/E13/E14）、人工裁决（E15） |
| `internal/gateway` | v2 会话（hello/hello_ack、逐帧验签 4400~4405、seq/eventId、加密下发、903/904 控制）+ v1 兼容路径 `/webserver/wework/{robotId}`（td/裸心跳/确认/RAW_CONFIRM/上报）透传统一管线 |
| `internal/api` | `POST /api/v2/pair`（配对码单次有效、公钥注册、deviceToken 仅存 SHA-256 hash）+ 会话端点挂载 + 回调 outbox worker（HMAC 签名、重试 1/2/4s、第 4 次死信、loopback 限制） |

### 端点

- `POST /api/v2/pair`——配对（400 格式 / 403 `PAIRING_CODE_INVALID` / 201 签发）
- `POST /api/v2/device-sessions`——建会话；服务端凭据失忆（重启）时须 `renegotiate{ts,nonce,signature}=Ed25519(deviceId|ts|nonce)`（spec §5.5.1，兼作重启恢复）
- `WS /api/v2/device-sessions/{sessionId}`——v2 安全会话（首帧 hello）
- `WS /webserver/wework/{robotId}`——v1 兼容（弱鉴权已知，仅迁移面）
- `POST /openapi/v1/robots/{robotId}/commands`——apifox 兼容面（FR-34）与扩展组：配置/回调 CRUD/历史/群/队列（`openapi_rest.go`；鉴权→60QPM 限流→写操作幂等键；队列清理/指定取消经 L2 审批门 REV-SRV-05）
- `POST /fileUpload/upload?robotId=`——文件上传（multipart part 名 `file`，≤50MiB；b2 §1 契约，REV-SRV-02 补齐）→ `data:{fileId,url,size,sha256}`
- `GET /files/{fileId}`——上传文件取回（218 fileUrl 闭环取数面；能力 URL：fileId 为 128-bit UUID，鉴权口径登记于 05 矩阵）
- `POST /robot/robotInfo/update?robotId=`——机器人消息回调配置（QA 面 openCallback/replyAll/callbackUrl，b2 §2.1，REV-SRV-02 补齐；分发接线属网关/事件车道 dodDeferred）
- `/healthz` / `/readyz`

### 凭据存储语义（spec §5.1.3 的落地口径）

`deviceToken` 原始值仅存在于：①配对/重协商响应（一次返回）②进程内存 LiveCred 注册表。
数据库仅存 `SHA-256(deviceToken)`。服务端重启 → 内存凭据失忆 → 设备以配对注册的
Ed25519 公钥重协商换发新凭据（tokenVersion+1），旧 token 即失效。

### 回调签名

```
X-WT-Timestamp: <unixMilli>
X-WT-Signature: hex(HMAC-SHA256(secret, "<unixMilli>." + body))
```
载荷仅元数据（commandId/errorCode/finalState/successList 等），不 echo 指令正文。

## 依赖纪律

仅两个纯 Go 第三方库（版本钉死 go.mod；新增须过 docs/04 门禁复核）：
`modernc.org/sqlite v1.34.4`、`github.com/gorilla/websocket v1.4.2`。

## 测试

```bash
go test ./...        # 单测 + loopback WS 集成（Go WS 客户端打真 HTTP/WS 栈）
```

覆盖：金标准向量复算（含 tamper_seq 反例）、CSV 驱动状态机全边正反例、崩溃恢复语义、
幂等键窗口、风险冻结、配对否定用例（格式/无效/过期/已用/公钥）、逐帧否定用例
（错 HMAC→4401、篡改 seq→4401、nonce 重放→4402、ts 超差→4403、tokenVersion→4404、
真实签名 seq 回退→4405+903、eventId 冲突→4405）、v1 兼容全链、回调重试/死信/签名。

**未含**（如实标注）：D1 真机联调（T2.3 DoD 原文的真机部分）——本地以 Go WS 客户端
loopback 集成测试替代，真机验证待 T2.2 端侧就绪后联调。

## 边界与后续

- REST v1 / apifox 兼容原生支持、调度细化（cron/有界重试）、admin RBAC：T3.2/T3.x。
- 901/902 凭据轮换/撤销广播、hello_ack schema 化：T3.4。
- Postgres 可选后端：T3.x（store 接口已按同事务边界设计）。
