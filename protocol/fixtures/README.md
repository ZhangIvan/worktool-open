# protocol/fixtures/ 测试夹具（T2.1）

原则：**文档 = 测试数据**。本目录全部 JSON 可被 `python json` 解析；正例通过对应 schema 校验，`negative/` 反例**必须**被对应 schema 拒绝。L1（android core:protocol, T2.2）与 L2（server gateway, T2.3）单测以仓库根相对路径直接引用。

## 正例清单

| 夹具 | schema | 用途 |
|---|---|---|
| `wire-v1-envelope.heartbeat.json` | `../schema/wire-v1-envelope.schema.json` | v1 信封化心跳（socketType=0）。注：上游观测到的最小形态为裸 `{"type":11}`（spec §3/§4.2），网关实现须两种都接受——最小形态无对应 schema（有意，属 v1 历史行为） |
| `wire-v1-envelope.command-203.json` | 同上 | v1 下行指令 203（apiSend=1） |
| `wire-v1-envelope.report-101.json` | 同上 | v1 上行消息上报 101（含 subMessageList/itemMessageList） |
| `pair-request.valid.json` | `../schema/pair-request.schema.json` | v2 配对请求（公钥为伪造测试值） |
| `pair-response.valid.json` | `../schema/pair-response.schema.json` | v2 配对响应（deviceToken 为伪造测试值） |
| `session-hello.valid.json` | `../schema/session-hello.schema.json` | v2 会话首帧 hello（meta.hmac 为真计算值，向量见 hmac-vector.json） |
| `command-envelope.plain.json` | `../schema/command-envelope.schema.json` | v2 明文指令下发（203，tokenVersion=1，meta.hmac 真值） |
| `command-envelope.encrypted.json` | 同上 | v2 加密指令（encryptType=2，tokenVersion=2，密文为伪造占位、hmac 按密文串真值计算） |
| `command-envelope.heartbeat.json` | 同上 | v2 心跳（空载荷：payloadDigest=SHA-256(空)） |
| `command-envelope.exec-result.json` | 同上 | v2 指令结果回执（socketType=3，回填 commandId） |
| `exec-callback.success.json` | `../schema/exec-callback.schema.json` | ExecCallbackBean 单体（errorCode=0） |
| `session-hello-ack.go-actual.json` | `../schema/session-hello-ack.schema.json` | **T3.4 冻结基准**：Go 网关实际产出的 hello_ack（2026-11-03 本地实服务原样抓帧；token=本地一次性测试实例签发，TEST-ONLY）。不变式：serverTime==meta.ts、resumeFromSeq==meta.seq==lastReceivedSeq+1（此处 lastReceivedSeq=0）。Kotlin『接受 Go 实际 hello_ack 字节』DoD 用例输入（token 以字面量内嵌 L1 测试，见 spec §5.7） |
| `control-payload.901-rotate.json` | `../schema/control-payload.schema.json` | 901 载荷（tokenVersion=2；密钥为伪造测试值；生产承载须 encryptType=2） |
| `control-payload.902-revoked.json` | 同上 | 902 载荷（撤销后版本在 meta.tokenVersion，载荷仅 type） |
| `control-payload.903-resync.json` | 同上 | 903 载荷（Go sendResync 现产形态基准） |
| `control-payload.904-replay.json` | 同上 | 904 端→云 journal 补报（entries 形状 = Go scheduler.JournalEntry） |
| `control-payload.904-request.json` | 同上 | 904 云→端补报请求（Go sendJournalRequest 现产形态基准） |
| `hmac-vector.json` | —（数据文件） | **金标准向量**：deviceTokenTest（伪造）+ 每帧 seq/inputString/payloadDigest/期望 hmac（输入式 `commandId\|eventId\|seq\|ts\|nonce\|payloadDigest`，AUD-03 C-9）；`negative[]` 为篡改 seq 必败反例。L1/L2 实现须逐字节复算一致 |
| `admin-command-examples.json` | —（数据文件） | 管理台指令目录 52 条 example 的**跨语言交换夹具**（2026-11-05 契约冻结车道，docs/16 §指令字段契约冻结）：单一事实源=server/internal/admin/catalog.go（字节级同源，同步由 Go TestCatalogExamplesFixtureInSync 锁定）；消费=Kotlin CatalogExampleContractTest（每条 example 经端侧 IntentNormalizer 断言可消费）。全部为测试合成值 |

## 反例清单（必须被拒绝）

| 反例 | 期望 |
|---|---|
| `negative/pair-request.invalid-code.json` | pair-request schema 拒绝（pairingCode 非 6 位数字） |
| `negative/command-envelope.missing-hmac.json` | command-envelope schema 拒绝（meta 缺 hmac → G8 鉴权失败路径） |
| `negative/command-envelope.encrypt-mismatch.json` | command-envelope schema 拒绝（encryptType=2 却携带明文 list 且缺 encryptedList） |
| `negative/session-hello-ack.bad-frozen.json` | session-hello-ack schema 拒绝（heartbeatIntervalSeconds=30/sessionTimeoutSeconds=90 违反 const 15/45 冻结值） |
| `negative/session-hello-ack.envelope-wrapped.json` | session-hello-ack schema 拒绝（resumeFromSeq=0 <1 且 meta.commandId 非空——hello_ack 恒空 commandId） |
| `negative/control-payload.901-bad-token.json` | control-payload schema 拒绝（deviceToken 非 hex64；tokenVersion=1 <2） |
| `negative/control-payload.903-extra-field.json` | control-payload schema 拒绝（903 additionalProperties:false） |
| `negative/control-payload.904-empty.json` | control-payload schema 拒绝（904 既无 entries（补报）也无 request（请求），两种合法变体均不满足） |

语义级反例（schema 无法表达，单测必须覆盖，G8）：错 HMAC（4401）、**篡改 seq**（HMAC 失配 4401，金标准反例见 hmac-vector.json `negative[tamper_seq]`，AUD-03 C-9）、nonce 重放（4402）、ts 超差 ±30s（4403）、tokenVersion 落后（4404）、eventId 同 ID 异内容（4405）、seq 回退（真实签名乱序→4405+903 重同步）。

## 冻结状态标注

- `targetRoom` / `receivedContent` 为**候选字段名**（spec §4.4 待 T3.2 apifox 冒烟冻结），冻结前不得作为 v1 互通依据；夹具中用于载荷示例。2026-11-05 契约冻结车道：管理台提交面与端侧主读字段已冻结为 upstream 契约名（203=titleList、206 名单=selectList、207/225 目标=groupName，见 docs/16 与 catalog.go 决策注释）；本目录两个 203 金标夹具（HMAC 向量字节）仍保留候选名 `targetRoom` 作为 transport 层载荷示例，端侧以迁移期只读别名兼容，别名撤销随 T3.2。
- 所有密钥类值（deviceTokenTest、ed25519PublicKey、x25519EphemeralPublicKey、encryptedList 密文）均为**伪造测试值**，非任何真实系统凭据。
