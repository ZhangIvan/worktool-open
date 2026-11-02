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
| `hmac-vector.json` | —（数据文件） | **金标准向量**：deviceTokenTest（伪造）+ 每帧 inputString/payloadDigest/期望 hmac。L1/L2 实现须逐字节复算一致 |

## 反例清单（必须被拒绝）

| 反例 | 期望 |
|---|---|
| `negative/pair-request.invalid-code.json` | pair-request schema 拒绝（pairingCode 非 6 位数字） |
| `negative/command-envelope.missing-hmac.json` | command-envelope schema 拒绝（meta 缺 hmac → G8 鉴权失败路径） |
| `negative/command-envelope.encrypt-mismatch.json` | command-envelope schema 拒绝（encryptType=2 却携带明文 list 且缺 encryptedList） |

语义级反例（schema 无法表达，单测必须覆盖，G8）：错 HMAC（4401）、nonce 重放（4402）、ts 超差 ±30s（4403）、tokenVersion 落后（4404）、eventId 同 ID 异内容（4405）、seq 回退（4405+903 重同步）。

## 冻结状态标注

- `targetRoom` / `receivedContent` 为**候选字段名**（spec §4.4 待 T3.2 apifox 冒烟冻结），冻结前不得作为 v1 互通依据；夹具中用于载荷示例。
- 所有密钥类值（deviceTokenTest、ed25519PublicKey、x25519EphemeralPublicKey、encryptedList 密文）均为**伪造测试值**，非任何真实系统凭据。
