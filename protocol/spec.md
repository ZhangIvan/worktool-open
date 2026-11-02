# WorkTool-Open 通讯协议规格 v1.0（T2.1）

- 版本：1.0.0（2026-11-02，任务卡 T2.1 交付；docs/03 T2.1 / docs/02 §5 落地）
- 权威声明：本目录（`protocol/`）是协议**唯一权威**（ADR-08）。机器可读权威件：`schema/*.schema.json`（报文）、`state-transitions.csv`（状态机边集）、`state-policy.json`（终态与策略）。本文与机器件冲突时，以机器件为准并登记 AUD 复核。
- 字段语义来源：v1 兼容面 = 上游开源仓库 v2.8.1 还原（证据 `local-private/audit/02` §A，仅字段名/指令号/枚举值/行为语义入库，涉密值一律不录入）；v2 增量 = `docs/02-ARCHITECTURE.md` §5（2026-11-02 用户审阅意见精确规格）。
- 消费方：L1 `android core:protocol`（T2.2）、L2 `server internal/gateway|scheduler|api`（T2.3/T3.2/T3.4）。夹具与金标准向量见 `fixtures/`（§7 消费契约）。

## 1. 范围

| 项 | 内容 |
|----|------|
| In | WS 帧信封（v1 兼容 / v2 安全）、配对与凭据、逐帧鉴权、会话与投递语义、加密、执行状态机、REST 幂等键约定 |
| Out | REST 业务 API 路由细节（apifox 兼容面，T3.2 冻结于 server/）、回调出站签名（T3.2，范式 docs/06 借鉴⑤）、admin API |

## 2. 版本与兼容策略

- **v1 兼容信封**：与上游 v2.8.1 报文互通（服务端须能解上游形态、端侧须能发上游形态）；**鉴权薄弱是已知事实**（上游 robotId 即身份，audit/02 §A.2/E4），v1 面仅用于兼容与迁移，新客户端一律 v2。
- **v2 安全增量**：不改 v1 信封字段，扩展 `meta` 为对象并新增配对/凭据/逐帧鉴权/投递语义。`meta` 为对象 = v2 帧判别条件。
- 服务端必须**同时**服务 v1 兼容路径与 v2 安全会话；同一设备会话只能是一种版本（不混用）。

## 3. 通道与端点

| 通道 | 路径 | 心跳 | 失联判定 | 说明 |
|------|------|------|----------|------|
| v1 兼容 WS | `{host}/webserver/wework/{robotId}` | 5s（`{"type":11}`） | 发送失败即重连 | 上游还原路径；`{host}` 为用户显式配置（SR-04 白名单，禁默认值 ws:// 与裸 IP，SR-09/G9） |
| v2 安全会话 | `POST {base}/api/v2/device-sessions` → `WSS {base}/api/v2/device-sessions/{sessionId}` | 15s | 45s 关闭 | 新客户端首选（docs/02 §5） |
| 配对 | `POST {base}/api/v2/pair` | — | — | TLS 强制（SR-01/09） |

- v1 连接建立：onOpen 后客户端立即发送 `{"td": <Unix毫秒>}`（上游还原）；重连指数退避 5s→600s 上限无限循环（上游还原）。
- v1 确认幂等：`confirm()` 30 秒去重窗口（上游还原）；v2 确认语义由 seq/eventId 承载（§5.4）。
- v2 重连退避：同指数算法，起步 5s，上限默认 120s（可配）；重连后从 `lastReceivedSeq+1` 补发（§5.4.2）。
- 错误码 501013（重复登录/多端互踢）为我方服务端职责（上游客户端已定义该码，服务端未开源；docs/02 §8 鉴权差异行）。

## 4. v1 兼容信封（WeworkMessageListBean）

### 4.1 全字段表（schema：`schema/wire-v1-envelope.schema.json`）

| 字段 | 类型 | 必含 | 语义 | 来源 |
|------|------|------|------|------|
| `socketType` | int | ✅ | 0=心跳 1=消息确认 2=消息列表（指令或上报）3=指令执行结果确认（RAW_CONFIRM） | audit/02 §A.2 |
| `messageId` | string | ❌ | `yyyy-MM-dd#HH:mm:ss#<UUID>`（上游生成时把日期与时间之间的空格替换为 `#`） | audit/02 §A.2 |
| `meta` | string | ❌ | 透传自定义信息（v1 为字符串；v2 扩展为对象，见 §5.2） | audit/02 §A.2 |
| `apiSend` | int | ❌ | 0=后台消息 1=API 指令调用 | audit/02 §A.2 |
| `list` | WeworkMessageBean[] | 条件 | `encryptType=0` 时明文载荷 | audit/02 §A.2 |
| `encryptedList` | string(hex) | 条件 | 加密后 list 的 hex；`encryptType=1` 上游 AES/CBC/PKCS7 | audit/02 §A.2 |
| `encryptType` | int | ❌ | 0=不加密 1=AES/CBC/PKCS7（**默认禁用**：上游硬编码 key/iv，值不录入本目录；本项目实现默认关闭，仅保留解码兼容，SR-02/G2）2=AES-256-GCM（v2，§5.3） | audit/02 §A.2 + docs/02 §5 |

### 4.2 指令/消息类型全表（`WeworkMessageBean.type`）

| 类别 | type | 名称 | 方向 | 备注 |
|---|---|---|---|---|
| 心跳 | 11 | HEART_BEAT | 双向 | v1 裸形态 `{"type":11}` 与信封形态（socketType=0）均须接受 |
| 上报 | 101 | TYPE_RECEIVE_MESSAGE_LIST | 端→云 | 聊天消息列表上报 |
| 上报 | 102 | TYPE_CONSOLE_TOAST | 云→端 | 交互提示 |
| 操作 | 201 | STOP_AND_GO_HOME | 云→端 | 停止任务回首页 |
| 操作 | 202 | LOOP_RECEIVE_NEW_MESSAGE | 端自发/云→端 | 主循环开关 |
| 操作 | 203 | SEND_MESSAGE | 云→端 | 发文本（支持 at/atList） |
| 操作 | 204 | REPLY_MESSAGE | 云→端 | 引用回复 |
| 操作 | 205 | RELAY_MESSAGE | 云→端 | 转发 |
| 操作 | 206 | INIT_GROUP | 云→端 | 建群（拉人/公告/备注/模板） |
| 操作 | 207 | UPDATE_GROUP | 云→端 | 改群配置 |
| 操作 | 208 | PUSH_MICRO_DISK_IMAGE | 云→端 | 推微盘图片 |
| 操作 | 209 | PUSH_MICRO_DISK_FILE | 云→端 | 推微盘文件 |
| 操作 | 210 | PUSH_MICROPROGRAM | 云→端 | 推小程序（R6：WWAPI 剥离后评估替代） |
| 操作 | 211 | PUSH_OFFICE | 云→端 | 推腾讯文档 |
| 操作 | 212 | PASS_ALL_FRIEND_REQUEST | 云→端 | 上游开源版空实现 |
| 操作 | 213 | ADD_FRIEND_BY_PHONE | 云→端 | 手机号加好友 |
| 操作 | 214 | SHOW_GROUP_INFO | 云→端 | 展示群信息 |
| 操作 | 215/216/217 | INIT/UPDATE/DELETED_ANTI_HARASSMENT_RULE | 云→端 | 上游仅常量定义，未见调用 |
| 操作 | 218 | PUSH_FILE | 云→端 | fileUrl/fileBase64/fileType（G11 外链白名单） |
| 操作 | 219 | DISMISS_GROUP | 云→端 | 解散群 |
| 操作 | 220 | ADD_FRIEND_BY_GROUP | 云→端 | 群内加好友 |
| 操作 | 221 | ADD_NEED_DEAL | 云→端 | 添加待办 |
| 操作 | 222 | CLOCK_IN | 云→端 | 上下班打卡 |
| 操作 | 223 | SWITCH_CORP | 云→端 | 切换企业 |
| 操作 | 224 | PUSH_LINK | 云→端 | 推链接（R6 同 210） |
| 操作 | 225 | MODIFY_GROUP_MEMBER_INFO | 云→端 | 改群成员备注/标签 |
| 操作 | 226 | RECALL_MESSAGE | 云→端 | 撤回消息 |
| 操作 | 227 | RELAY_MULTI_MESSAGE | 云→端 | 批量转发 |
| 操作 | 228 | RELAY_MERGE_MESSAGE | 云→端 | 合并转发 |
| 操作 | 229 | SEND_MULTI_MESSAGE | 云→端 | 批量发送 |
| 操作 | 230 | SEND_MERGE_MESSAGE | 云→端 | 合并发送 |
| 操作 | 231/232 | VOICE_CALL / VIDEO_CALL | 云→端 | 上游仅常量，未见分发；我方语义=自动拒接（FR-51）待 T3.x 冻结 |
| 操作 | 233 | SCAN_QR_CODE | 云→端 | 扫一扫（传二维码图） |
| 操作 | 234 | DELETE_CONTACT | 云→端 | 删除联系人 |
| 日志 | 301/302 | ROBOT_LOG / ROBOT_ERROR_LOG | 端→云 | 运行日志上报（v2 须过脱敏管道 SR-06） |
| 日志 | 303 | ROBOT_CONTROLLER_TEST | 云→端 | 接口测试 |
| 日志 | 304 | ROBOT_QUEUE_CLEAR | 云→端 | 清空待执行指令队列 |
| 获取 | 501 | GET_GROUP_INFO | 端执行→云 | |
| 获取 | 502 | GET_FRIEND_INFO | 端执行→云 | |
| 获取 | 503 | GET_MY_INFO | 端执行→云 | |
| 获取 | 504 | GET_GROUP_QRCODE | 端执行→云 | 群二维码回调 |
| 获取 | 505 | GET_RECENT_LIST | 端执行→云 | 最近会话 |
| 获取 | 506 | GET_CORP_LIST | 端执行→云 | 企业列表 |
| 获取 | 507 | GET_ALL_FRIEND_INFO | 端执行→云 | 全部好友 |
| 获取 | 508 | GET_ALL_GROUP_INFO | 端执行→云 | 全部群 |
| 获取 | 509 | GET_LOCAL_FILE | 端执行→云 | 上传本地文件（默认当前日志） |
| **v2 控制类** | 901 | CREDENTIAL_ROTATE | 云→端 | 凭据轮换（§5.5.1） |
| **v2 控制类** | 902 | CREDENTIAL_REVOKED | 云→端 | 撤销广播（§5.5.2） |
| **v2 控制类** | 903 | RESYNC_REQUEST | 双向 | seq 乱序/冲突后重新同步（§5.4.4） |
| **v2 控制类** | 904 | JOURNAL_REPLAY | 端→云 | 离线 journal 补报（§5.4.5） |

- 9xx 为 WorkTool-Open 扩展段：上游已还原类型仅 11~509，90x 无碰撞；新增控制类型必须先在本表登记。
- 图片上报旁路（上游还原，不走 WS）：`{receivedName, groupName, groupRemark, image(base64), robotId, roomType, atMe:false, textType:2}` POST 到服务端配置的回调 URL。v2 策略：回调出站仅 metadata/referenceId，不 echo 内容（docs/02 §6.1）。

### 4.3 关键子结构与错误码

- `roomType`：0=未知 1=外部群 2=外部联系人 3=内部群 4=内部联系人。
- `textType`：0=未知 1=文本 2=图片 3=语音 4=名片 5=视频 6=定位 7=小程序 8=链接 9=文件 10=警告 11=腾讯文档 12=群接龙 13=合并聊天记录 14=群收集表 15=引用回复 16=内部群机器人通知 17=视频号视频 18=视频号直播。
- `SubMessageBean{sender(0 他人/1 自己/2 系统), textType, itemMessageList[], nameList[], imageRepeat, imageSize, image}`；`ItemMessageBean{feature(0 消息头上方系统信息/2 消息内容), text}`；`Friend{phone, name, markName, markCorp, markExtra, tagList[], newFriend, leavingMsg}`。
- 执行结果回执（socketType=3 载荷，schema：`schema/exec-callback.schema.json`）：`ExecCallbackBean{rawMsg, errorCode, errorReason, runTime, timeCost, successList, failList, metaJson}`；`rawMsg` 为 v1 兼容回显，v2 SHOULD 省略（泄露面收敛）。
- 错误码族：`0`=成功；`10xxx`=格式/权限错误（已知 101012 单指令异常重试后仍失败）；`20xxx`=执行错误（已知 201011 建群失败、201108 文件存储异常）；`50xxx`=服务端错误（已知 501011 robotId 错、501012 robotKey 错、501013 重复登录）。已知值为上游已还原子集，完整表以 apifox 对照补齐（§9）。

### 4.4 载荷字段冻结状态

| 状态 | 字段 |
|------|------|
| 已还原（可依赖） | `type/roomType/textType/at/atList/extraText/fileUrl/fileBase64/fileType/friend/subMessageList(itemMessageList,nameList,imageRepeat,imageSize,image)` 及 §4.3 子结构 |
| 待冻结【T3.2 apifox 冒烟时确认】 | 指令正文与目标会话字段名（候选 `receivedContent`/`targetRoom`，夹具暂用候选名并在 `fixtures/README.md` 标注）；`runTime` 精确格式；successList/failList 元素结构 |

## 5. v2 安全增量（docs/02 §5 全量落地）

### 5.0 落地映射（docs/02 §5 条目 → 本文章节）

| docs/02 §5 条目 | 落地 |
|---|---|
| 配对码 6 位/TTL 10min/单次有效 | §5.1.1 + `schema/pair-request.schema.json` |
| Ed25519 签名公钥 + X25519 临时公钥分列两字段 | §5.1.2 + pair-request schema 两字段 |
| deviceToken 256-bit/服务端仅存 hash/端侧 Keystore | §5.1.3 + `schema/pair-response.schema.json` |
| 逐帧 `meta{ts,nonce,hmac}` 输入式 | §5.2（冻结） |
| payloadDigest = SHA-256(RFC 8785) | §5.2.2（冻结） |
| 时钟容差 ±30s / nonce 双向 5min | §5.2.3 |
| 30 天轮换（重协商复用签名公钥）/tokenVersion 撤销/REVOKED 广播/重连校验失败强制重配对 | §5.5 |
| seq 严格递增 + eventId 去重（同内容回执/异内容关闭） | §5.4 |
| 幂等键 `tenant+actor+operation+key`（24h，变参 409） | §5.6.1 + `state-policy.json` |
| 墓碑 ≥7 天 | §5.6.2 + `state-policy.json` |
| execution_unknown 三原则 | §5.6.3 + `state-transitions.csv` |
| encryptType=2 AES-256-GCM；会话密钥 ECDH(X25519)+HKDF-SHA256 | §5.3 |
| 心跳 v1 5s / v2 15s/45s | §3 |

### 5.1 配对与凭据

#### 5.1.1 配对码
控制台生成：6 位数字，TTL 10 分钟，**单次有效**（一次成功配对即作废；过期作废）。生成与消费均在服务端，配对码不落端侧持久存储。

#### 5.1.2 配对请求（schema：`pair-request.schema.json`）
`POST /api/v2/pair`（TLS 强制），体字段：

| 字段 | 必含 | 约束 |
|---|---|---|
| `pairingCode` | ✅ | `^[0-9]{6}$` |
| `deviceId` | ✅ | `^[A-Za-z0-9._-]{8,64}$`，首次启动生成并持久化 |
| `deviceName` | ❌ | ≤64 字符，展示用 |
| `ed25519PublicKey` | ✅ | 32 字节 hex（`^[0-9a-f]{64}$`），AndroidKeystore 生成的**签名公钥**；服务端注册 |
| `x25519EphemeralPublicKey` | ✅ | 32 字节 hex，每次配对/重协商新生成的**临时公钥**，与签名公钥**分列两字段**（docs/02 §5 冻结） |

拒绝条件：配对码无效/过期/已用（403 `PAIRING_CODE_INVALID`）、公钥格式非法（400）。重复 `deviceId` 携新配对码 = 重新绑定（覆盖注册公钥，旧 deviceToken 立即失效）。

#### 5.1.3 配对响应（schema：`pair-response.schema.json`）
| 字段 | 语义 |
|---|---|
| `deviceToken` | 随机 256-bit hex；**逐帧 HMAC 密钥即其原始字节**；服务端仅存 SHA-256 hash；端侧 Keystore 加密存储（SR-05/G5） |
| `tokenVersion` | 初始 1 |
| `sessionId` | 本次协商会话 ID，HKDF salt 输入 |
| `serverX25519PublicKey` | 服务端 X25519 临时公钥（每次配对/重协商新生成） |
| `heartbeatIntervalSeconds`=15 / `sessionTimeoutSeconds`=45 | docs/02 §5 冻结值 |
| `rotationIntervalDays` | 默认 30（≥1 可配） |
| `wsPath` | `/api/v2/device-sessions/{sessionId}` |

### 5.2 逐帧鉴权（冻结）

#### 5.2.1 meta 对象（v2 帧判别：`meta` 为对象）
```
meta = { ver:2, ts, nonce, hmac, seq, eventId, commandId, tokenVersion }
```
（完整约束见 `command-envelope.schema.json#/definitions/frameMeta`；全部字段必含，`additionalProperties:false`。）

#### 5.2.2 HMAC 输入式（冻结，docs/02 §5 原式）
```
hmac = HMAC-SHA256(key = deviceToken 原始字节,
                   msg = UTF-8( commandId "|" eventId "|" ts "|" nonce "|" payloadDigest ) )
payloadDigest:
  encryptType=0 → hex(SHA-256( JCS(RFC 8785 规范 JSON) of `list` ))；list 缺失或空数组按空字节串计
  encryptType=2 → hex(SHA-256( UTF-8 bytes of `encryptedList` 字符串原样 ))  ← 先验签后解密
```
- 非 16 进制输出一律小写 hex（64 字符）。
- 金标准向量（含 inputString 全文与期望 hmac）= `fixtures/hmac-vector.json`；空载荷 SHA-256 常量：`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。

#### 5.2.3 时钟与 nonce
- **±30s**：`|ts − 接收方now| > 30s` → 拒帧并关闭会话（关闭码 4403 `clock_skew`）。
- **nonce 双向 5 分钟去重**：每方向独立维护（发送侧生成唯一性由 128-bit 随机保证；接收侧 5 分钟窗口内重复 → 4402 `nonce_replay`）。

#### 5.2.4 校验顺序（实现必须一致，便于测试）
格式校验 → tokenVersion → ts（±30s）→ nonce（5min 窗口）→ hmac → seq/eventId →（若 encryptType=2）解密。任一步失败即拒帧；鉴权类失败关闭会话。

#### 5.2.5 WS 关闭码（v2 会话）
| 码 | 含义 |
|---|---|
| 4400 | 帧格式/schema 非法 |
| 4401 | 鉴权失败（HMAC 不符/无凭据） |
| 4402 | nonce 重放 |
| 4403 | 时钟偏差超差 |
| 4404 | tokenVersion 校验失败（→ §5.5 重协商或强制重配对） |
| 4405 | seq 乱序或 eventId 冲突（→ 903 重新同步） |

### 5.3 加密（encryptType=2）

- 算法：**AES-256-GCM**（修复上游硬编码 CBC，SR-02）。
- 会话密钥派生（冻结）：
```
shared = X25519(设备配对临时私钥, serverX25519PublicKey)
K_session = HKDF-SHA256(ikm=shared, salt=UTF-8(sessionId), info="worktool-open/v2/session", L=32)
```
- 每次配对/重协商（§5.5）产生新 X25519 对 → 新 K_session（前向隔离以协商周期为界）。
- GCM nonce（12 字节，冻结）：`bytes[0:12] of SHA-256(UTF-8(eventId))`（帧唯一、确定性、免传输）。
- GCM AAD（冻结）：`UTF-8("ver=2|seq=" + seq + "|eventId=" + eventId + "|ts=" + ts)`。
- `encryptedList` 编码：`hex( 密文 || 16字节tag )`，承载与 v1 `list` 相同的 JSON 数组明文。
- 解密失败/验签失败 = 帧整体拒绝（不得部分接受）。

### 5.4 会话与投递语义

#### 5.4.1 建连与 hello（schema：`session-hello.schema.json`）
设备 `POST /api/v2/device-sessions`（携带 deviceId；凭据续期场景见 §5.5.1）取得 `wsPath` 后建连，**首帧 hello**：
```
{ type:"hello", deviceId, tokenVersion, lastEpoch, lastReceivedSeq,
  capabilities:{clientVersion, encryptTypeMax, commands[]},
  meta:{ ver:2, ts, nonce, hmac, seq:1, eventId, commandId:"", tokenVersion } }
```
- `lastEpoch`：端侧 journal 已应用的最小命令 epoch——服务端不得下发更小 epoch（端侧拒旧，docs/02 §5.1）。
- `lastReceivedSeq`：端侧持久确认的最高入向 seq——服务端从此值 +1 补发（不丢不重）。
- hello 为空载荷帧（payloadDigest=空串哈希）。
- 服务端应答 `hello_ack`：`{type:"hello_ack", serverTime, resumeFromSeq, heartbeatIntervalSeconds, sessionTimeoutSeconds, meta{…}}`（形状在此冻结，schema 随 T3.4 实现）。

#### 5.4.2 seq（每会话每方向严格递增，从 1 起）
- 乱序/回退 → 拒帧 + 关闭 4405 + 发起 903 重新同步（服务端自 `lastReceivedSeq+1` 重放未确认事件）。
- 重连不重置计数语义：新连接 seq 重新从 1 起，但补发内容以持久确认点为准（journal/outbox）。

#### 5.4.3 eventId 去重
- 重复 `eventId`：**同 payloadDigest**（同内容重复）→ 幂等回执原确认（不得重复副作用）；**异 payloadDigest**（同 ID 异内容）→ 冲突，关闭 4405。

#### 5.4.4 重新同步（903）
触发：seq 乱序、eventId 冲突、会话恢复后区间不连续。动作：关闭当前流 → 以持久确认点为基线重放（服务端）或重发 journal（端侧 904）。

#### 5.4.5 离线合并（docs/02 §5.2）
- 端侧已执行事件补报：`source:"local"`；服务端按 `(ruleId, eventHash)` 去重。
- 跨 session 去重：`commandId` 墓碑（§5.6.2）。
- 端侧崩溃恢复 = SQLite journal 重放 + 最小命令 epoch 拒旧（docs/02 §5.1）。

### 5.5 凭据轮换与撤销

#### 5.5.1 轮换（默认 30 天，`rotationIntervalDays`）
1. 服务端到期下发 `type=901 CREDENTIAL_ROTATE`（**必须 encryptType=2**，走当前已鉴权加密会话）：新 `deviceToken`、`tokenVersion+1`、新 X25519 公钥对（服务端新临时公钥随帧下发，设备重协商新生成临时密钥对）。
2. 设备以新凭据发首帧（`meta.tokenVersion`=新值，HMAC 用新 token）确认切换。
3. 切换确认后旧 deviceToken 立即失效；未确认前双值并验（切换窗口内接受新旧之一，但 nonce/ts 窗口各自独立）。
4. **离线错过轮换**：重连时 hello `tokenVersion` 落后 → 4404 → 设备走**重协商**：`POST /api/v2/device-sessions` 携带 `deviceId + Ed25519 签名(deviceId|ts|nonce)`（**复用配对时注册的签名公钥**，无需新配对码）；验证通过签发新凭据。

#### 5.5.2 撤销
管理员撤销：`tokenVersion+1` + 在线会话广播 `type=902 CREDENTIAL_REVOKED` 并关闭；离线设备重连时版本校验失败（4404）且**无有效重协商资格**（撤销类版本差）→ **强制重新配对**（新配对码走 §5.1）。撤销事件入审计（SR-12）。

### 5.6 投递语义（服务端侧）

#### 5.6.1 API 幂等键（docs/02 §5）
- 键构成：**`tenant + actor + operation + key`**（四元组唯一；口径以 docs/02 §5 / AUD-02 C5 为准）。
- 窗口 24h：同键同规范参数 → 返回原响应（重试安全）；同键**变参** → `409 IDEMPOTENCY_CONFLICT`。
- 范围：**全部 POST 创建类端点**（docs/06 借鉴②教训）。参数见 `state-policy.json#idempotency`。

#### 5.6.2 设备墓碑
- 保留 **≥7 天**（7d > 300s 投递 TTL + 24h 幂等窗 + 30s 时钟偏差，`state-policy.json#tombstone`）。
- 内容仅元数据（commandId/eventId/终态/时间戳），**不含消息正文**；清理前置条件：该 commandId 无 pending `execution_unknown`。

#### 5.6.3 execution_unknown 三原则（docs/02 §5.1 原文落地）
1. **只凭持久证据对账**（设备 journal / `reconcile_unstarted` 类边），**禁止超时推断成败**；
2. **非幂等指令永不自动重试**；
3. **L1/L2 危险指令存在 unknown 待核时，冻结同类新指令**（FR-53 联动）。
- 进入边：E06/E07/E08；离出边：E12（证据·成功）/E13（unstarted）/E14（证据·失败）/E15（人工裁决，审计留痕）。

## 6. 执行状态机

- **唯一权威边集 = `state-transitions.csv`**（种子 16 条，表驱动；docs/02 §5.1：计划文档禁止复述边表防漂移，本文不复述）。终态判定与策略参数 = `state-policy.json`。
- 状态集：`created → queued → dispatched → acked → running → succeeded | failed | cancelled`，加 `execution_unknown`（非终态，必须被对账离出）。
- 终态：`succeeded / failed / cancelled`（无出边；再执行须新 commandId）。
- 非法迁移 = `409 ILLEGAL_TRANSITION`，迁移与 command_events/审计同事务（docs/06 借鉴①）。
- 不变量：① 每条边触发必须留痕（command_events）；② 结果/事件/审计/回调 outbox 同事务（docs/02 §5.1）；③ `transport_ack ≠ 执行许可`（E05 注）；④ 端侧执行门禁与去重持久化**先于**副作用（E09 guard）。

## 7. 夹具与测试向量（消费契约）

- 目录：`fixtures/`（正例，逐 schema ≥1）与 `fixtures/negative/`（**必须被拒绝**的反例）。清单与用途见 `fixtures/README.md`。
- **金标准向量**：`fixtures/hmac-vector.json`（TEST-ONLY 伪造 token；L1 Kotlin / L2 Go 实现须逐字节复算通过——含 inputString 原文与期望 hmac/digest）。
- 消费契约：夹具文件名与字段**冻结**为测试数据接口；变更须提 AUD 并同步 L1/L2 单测。候选字段（§4.4 待冻结项）在 README 中显式标注，冻结前不得作为互通依据。
- 路径稳定：`protocol/schema/*.schema.json`、`protocol/state-transitions.csv`、`protocol/state-policy.json`、`protocol/fixtures/**`（单测以相对仓库根路径引用）。

## 8. 安全基线映射

| 基线 | 关联 | 本规格落点 |
|---|---|---|
| TLS 强制/系统校验/无明文默认 | SR-01/04/09, G9 | §3（{host} 用户显式配置，禁默认 ws:// 与裸 IP） |
| 无硬编码密钥 | SR-02, G2 | §4.1 encryptType=1 默认禁用；§5.2/5.3 每设备凭据与派生密钥 |
| 鉴权（无 token/错 HMAC/重放全拒） | SR-08, G8 | §5.2.3/5.2.4/5.2.5（4401/4402/4403/4404/4405） |
| 存储加密 | SR-05, G5 | §5.1.3 deviceToken 端侧 Keystore、服务端仅存 hash |
| 出网白名单/外链拒收 | SR-04/11, G11 | §4.2 type=218/233 fileUrl 白名单（默认拒） |
| 遥测零容忍 | SR-03, G3 | 本目录禁止出现第三方统计 SDK 字样（CI G3-lite 强制） |
| 涉密零入库 | 政策 P7, G13 | 上游密钥常量值不录入本目录（§4.1 注） |

## 9. 待确认清单（不阻塞 T2.2/T2.3 开工）

| # | 项 | 影响 | 兑现点 |
|---|---|---|---|
| Q1 | 指令正文/目标会话字段名（候选 receivedContent/targetRoom） | v1 兼容面载荷互通 | T3.2 apifox 冒烟 |
| Q2 | 错误码完整表（已知值见 §4.3） | 结果回执映射 | T3.2 |
| Q3 | hello_ack / 901~904 控制帧 schema 化 | v2 实现期校验 | T3.4 |
| Q4 | 231/232 我方语义（自动拒接 FR-51 vs 禁用） | FR-51 落位 | T3.1 |

## 附录 A：HMAC 工作示例（节选，全量见 fixtures/hmac-vector.json）

```
deviceToken(TEST) = <见 fixtures/hmac-vector.json，伪造值>
frame: fixtures/command-envelope.plain.json
  inputString = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f|7d6e5f4a-3b2c-4d5e-8f9a-0b1c2d3e4f5a|<ts>|5c1f9a7b3e2d4c6f8a0b2d4e6f8a0c2e|<payloadDigest>"
  hmac         = HMAC-SHA256(deviceToken, UTF-8(inputString))   ← 与夹具 meta.hmac 逐字节一致
```
