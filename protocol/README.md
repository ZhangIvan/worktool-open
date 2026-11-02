# protocol/

通讯协议规格（WorkTool-Open 协议唯一权威，ADR-08）。

- 原则：**文档 = 测试数据**（夹具直接被 android/server 单测引用，防止实现与文档漂移）
- v1 兼容信封与指令表还原自上游开源仓库（证据 `local-private/audit/02` §A，仅字段名/指令号/枚举入库，涉密值不录入）；v2 增量（配对码/deviceToken/HMAC/ECDH-AES-GCM/投递语义）= `docs/02-ARCHITECTURE.md` §5 全量落地
- 任务卡：T2.1（本目录 v1.0.0）、T3.4（v2 双端实现）

## 产物清单（T2.1）

| 文件 | 内容 |
|---|---|
| `spec.md` | 规格正文：v1 全字段表 + 指令全表 + v2 精确规格（HMAC 输入式/ECDH+HKDF 派生/±30s/nonce 5min/30 天轮换/撤销/seq+eventId/幂等键/墓碑/execution_unknown） |
| `schema/wire-v1-envelope.schema.json` | v1 兼容信封（draft-07） |
| `schema/command-envelope.schema.json` | v2 帧信封（逐帧鉴权 meta）（draft-07） |
| `schema/pair-request.schema.json` | v2 配对请求（draft-07） |
| `schema/pair-response.schema.json` | v2 配对响应（draft-07） |
| `schema/session-hello.schema.json` | v2 会话 hello（draft-07） |
| `schema/exec-callback.schema.json` | 指令执行结果回执载荷（draft-07） |
| `state-transitions.csv` | 执行状态机**唯一权威边集**（种子 16 条） |
| `state-policy.json` | 终态判定与策略参数（幂等/墓碑/unknown 三原则参数） |
| `fixtures/` | 正例（每 schema ≥1）+ `negative/` 反例 + `hmac-vector.json` 金标准向量（消费契约见 `fixtures/README.md`） |
