# 协议 v1 — 待独立冻结的设计契约

所有样例是 synthetic；本版 schema 拒绝 mode=real。这是新协议，不是旧 APK wire 兼容承诺。67 历史项是需求追踪输入，不构成 67 可下发指令。首批只有 message.send_text / file.export 两个模拟动作；其他能力返回 CAPABILITY_UNAVAILABLE，不伪造成功。控制面包含配对、策略、取消、删除、更新包校验模拟；不得因此声称 Android 功能已验证。

## Transport / negotiation
HTTPS REST + WSS `/api/v1/device-sessions`。本地原型仅 loopback 可使用 HTTP/WS；外部绑定必须 TLS，禁止明文降级。设备凭据只 Authorization header，禁止 query、日志、URL、共享账号口令；浏览器使用同源 HttpOnly/SameSite=Strict session+CSRF token，WebSocket校验Origin。禁止浏览器用设备信道。握手认证后5秒内 hello；version取支持集合最高共同版本，本期仅1.0，否则关闭1002/UNSUPPORTED_VERSION。不以客户端自报tenantId赋权。服务端生成sessionId，返回hello_ack(选定version、绑定tenantId/deviceId/sessionId、policyVersion、serverTime、heartbeatIntervalSec=15、maxFrameBytes=65536)。hello/hello_ack不进入命令状态机。

全部应用消息符合wire.schema.json；禁额外字段。发送时间为UTC RFC3339，JSON UTF-8，frame上限64KiB。schema只能检查形状；权限/时效/digest/版本/租户/序号是业务守卫，必须另测。禁止远端用户提供动态schema。心跳45秒失联关闭会话，不等于任务失败。事件只metadata/referenceId，不把会话/文件正文写审计。

## Channel authorization
server→device: command / result_ack / config_notification。
device→server: accepted / started / execution_result / heartbeat / device_event。
transport_ack双方允许，仅确认指定eventId收到，不证明接受或执行。accepted必须落设备去重存储后；started须设备已持久化“将执行”证据后且在实际副作用前。设备结果先本地持久化，再上传；服务端事务提交结果+审计后result_ack，设备之后才可清理结果重传队列。无started回执但收到结果时，服务端须以事务补记合法begin（验证accepted已知）；如果连accepted也丢，凭完整设备持久证据重建而非直接跳状态，无法证明则execution_unknown待核。

seq每方向每session从1严格递增；重复eventId+同内容返回存储回执，不推进状态；同eventId异内容冲突并关闭。乱序seq拒绝并请求重连同步，不跨session延用seq。重连创建新sessionId，但稳定commandId及设备去重墓碑跨session保留。消息重传复用原eventId/seq；接收端必须先做重复查找，再判断seq回退；结果重发可在新session使用新eventId，但同commandId/attemptId/payloadDigest。

## Command identity / signing scope
API请求要求Idempotency-Key，键tenant+actor+endpoint+key唯一（有效窗口24h）；同键同规范请求返回原响应，变参409。commandId服务端产生，唯一tenant+device+commandId。payloadDigest=SHA256(RFC8785 canonical JSON of {capability,payload,expiresAt,policyVersion,requiresLocalConfirmation,deviceId})；必须实现同一规范，禁止JSON.stringify替代声明。首次接受与执行前均验证scope、有效期、policyVersion、绑定设备/租户、签名会话。不得在此设计中嵌固定AES或共享密钥。设备credential为独立随机256bit secret、服务端仅hash、可单设备轮换撤销；原型仅合成身份。生产配对采用短时一次性challenge+本地确认与签名公钥方案，需AndroidKeystore实测，不声称模拟token即硬件身份。

TTL首批允许30–300秒；允许时钟误差≤30秒，时钟回退/无法信任服务端时钟拒绝执行。设备离线不得启动新敏感动作；普通动作也须恢复在线重新授权后执行。墓碑保留至少7天（大于300秒TTL+24小时API幂等窗口+30秒偏差），不含正文；自动清理前必须确认没有unknown待核项。过期墓碑外的旧commandId不得被服务端重新调度，设备同步最小命令epoch下限拒旧；首批无离线无限留存队列。

## 状态与竞态
state-transitions.csv 为唯一允许事件边；其他组合409。state-policy.json定义终态。撤销与取消是请求/审计事件，不是成功结果。已下发命令deadline、失联、进程崩溃后若无“未执行”持久证据进入execution_unknown；不得标expired后重试。running遇取消/策略撤销仅发停止请求，直到设备给出效果证明。产生部分效果返回partially_succeeded并展示不可撤销部分。terminal收到相同结果幂等；相反结果保留冲突审计、人工复核，不静默覆盖。execution_unknown仅可依据证据reconcile，不产生新的执行。新发独立命令必须重新确认可能重复的副作用。

权限变更提交后禁止新调度；连接态撤销通知5秒为待测SLO，不是保证。离线无法远程即时擦除、撤回已发送消息或已下载文件。设备本地急停优先在下一安全点检查，不能撤销外部系统已生效动作。服务端状态+事件+审计+outbox必须同事务，提交前不得WS发送；设备执行门禁/去重持久化在副作用前，但外部UI动作与本地DB不可能同事务，因此恰好一次不承诺。

## Errors / versioning
HTTP错误统一 `{error:{code,message,requestId,retryable,details}}`，details仅字段名不得echo secret/content。错误码见error-codes.json；跨租户对象查询返回404不透露存在。429含Retry-After；503自动重试仅可幂等请求，绝不重执行unknown。WS错误包含同样code/requestId且不改变状态。字段/枚举破坏变更必须提升major；未知能力/事件拒绝，不能动态执行脚本/任意文件路径。v1.0更新不隐藏兼容降级。

## Review refinement R1 (normative)
`stop_request` server→device is a specific authenticated control message bound to commandId/attemptId/reason/policyVersion; transport_ack only means received. It never itself changes command status. Device persists stop intent and returns an execution_result with proven not_started/not_applied→cancelled, partial effects→partially_succeeded, completed→succeeded, uncertain→execution_unknown. Local permission/confirmation/policy rejection before begin uses execution_result status=rejected/effectState=not_started and durable evidenceRefs, permitted from delivered/accepted; after begin use failure/partial/unknown rules, never re-label as rejection. Server timeout/session-loss may mark running execution_unknown without a device-signed success receipt; this is uncertainty evidence, not proof of an effect. Result evidenceRefs is a bounded list (≤16) of device-local synthetic durable journal references; no raw content or arbitrary URL. For multi-target future batches, separate per-target typed schema is required before enabling that capability; first two single-action simulations do not emit partial unless fault injection creates a modeled partial local artifact, visibly labeled synthetic. Integers must be JS-safe ≤9007199254740991. IDs/digests/timestamps use strict end anchors rejecting trailing newline.

### Permission revoke vs device credential revoke
Capability permission revocation increments policyVersion, blocks new dispatch and sends stop_request on the still-authenticated device session; that restricted session may upload existing results but may not begin a new command. Full device credential revocation invalidates the session and closes it; stop notification is only best effort before closing, never a prerequisite for revocation. If a trusted result cannot subsequently be obtained, already-dispatched commands remain execution_unknown; they are not declared cancelled merely because the socket closed. Re-enrollment cannot silently replay them. An old revoked credential must not obtain a fresh session or submit new events; recovery of old durable evidence requires an explicit authenticated reconciliation workflow, not a revocation bypass.

### Deterministic test clock
Examples with 2030-01-01 timestamps run only with an explicitly injected synthetic clock at2030-01-01T00:00:00Z; the wire example uses2026-11-02T00:00:00Z with its own frozen clock. These are not requests to execute at wall-clock time. Runtime must validate expiresAt-serverTime within30–300 seconds; valid JSON shape alone does not authorize future-expiry examples. Production code must not expose a request parameter that changes server time.

## First-slice reliability amendment: authenticated unstarted reconciliation
After session loss marks a delivered/accepted command execution_unknown, a reconnecting authenticated device may retransmit its durable rejected/cancelled/expired result via named reconcile_unstarted_* edges. Exact tenant/device/command/attempt binding, effectState=not_started and nonempty journal_* evidenceRefs are mandatory. Server must query durable command_events: any known begin/running, applied/partially-applied effect or prior terminal evidence forbids these edges, even if current state is unknown. Existing stored result remains immutable: exact duplicates ACK, changed result rejects. Result, command event, audit, inbox and result ACK outbox share one transaction. No fresh execution or cancellation merely inferred from disconnect. This is a trusted-device journal claim, not independently proven physical absence of effects.
