# 逐指令支持矩阵（Support Matrix）v1

- 版本：v1.4（2026-11-03，会话 4 回填：LabPairingActivity 一页式配对首轮即中（会话 3 IME 卡点破），持久会话内 501 真实下发全链 313ms 闭环（dispatch→exec→RAW_CONFIRM）但 201001×1+熔断会话亡×1=0 成功⬜ 不翻——执行窗与**外部并发自动化**完全重叠（授权被外部撤销/同窗节点遍历/注入触点），App 侧不可归因（静态核验全过），铁律 1 终止+被动撤离；v1.3（2026-11-03，会话 3 回填：配对流铁律 2 终局停（MIUI 中文 IME 劫持 `adb input text`→地址校验拒绝，驱动侧缺口非 App 缺陷），501/502 真实执行 0 次⬜ 不翻，修复 a3b29e0（重连重建入口/interactiveRoots 观测面）未获设备实证；干跑续证 206/207/225/509/203 ×25 全 PASS；配对页无障碍检测卡「已开启 ✓」实见（3fe3a2a 验收点 PASS）；v1.2（P0-S2 车道回填：执行器批次 437a8a4 已装配并首获端侧真实执行回执（RAW_CONFIRM），501 真实执行 3 败终局停（铁律2），0 次成功⬜ 不翻；v1.1（W-M3R driver 车道真机取证回填：基线列扩 5.0.13 实测列 + 各指令行证据备注）））
- 基线（用户确认 2026-11-02）：兼容企微 **4.1.8~4.1.39** 与 **5.0.0~5.0.10**；开发/验证默认基线 **5.0.x 最新稳定**；实测设备列 **5.0.13**（Synthetic-Phone-A12 / Android 12 / 企微 5.0.13 versionCode 84802，D1 RIG 通道，2026-11-03）。**加注（2026-11-03 双源深审）**：APK 内置版本映射声明兼容 **4.0.2~5.0.12**（e2/a.java:82 实证 44 档，含 4.0.x 全系与 5.0.11/5.0.12）——声明兼容范围据此扩界（docs/01 §2.2 已同步），本矩阵列结构维持现状
- 图例：✅=真机验证通过（≥5 次，回执错误码=0）｜🟡=可运行未全版本验证｜❌=明确不支持｜⬜=未验证（NOT_RUN，不得宣称支持）｜N/A=该版本无此功能
- 规则：**任何单元格未经真机证据不得从 ⬜ 变更为 ✅/🟡**（对齐 04 §5.1 Release Gate）；每列证据（设备/企微版本/次数/日志）存 `local-private/evidence/`
- 2026-11-03 真机取证总边界（证据 `local-private/evidence/2026-11-03/` 与 `local-private/evidence/p0-s2/`）：v1.1 时点边界=执行层未装配（配对面会话 hello_ack 即被 FrameVerifier 拒收）；**P0-S2 已更新**：执行器批次 437a8a4 装机后，配对→hello_ack→下发→端侧执行器真实运行→RAW_CONFIRM 回执→服务端 E10 全链贯通（501 迭代 3/4 闭环，UI 日志行为证）——卡点收窄到执行器观测面首步（openContactsTab NODE_NOT_FOUND 201001×3，见 501/502 行备注两层根因）。干跑（FR-54 服务端代行）与下发链（queued→flushQueued 补投→帧达设备被 seq_gap 拒收）此前已分别取证。⬜ 未经执行回执不得翻转。**会话 3 追加（证据 session3/）**：卡点前移至配对流（IME 劫持，铁律 2 终局停）；观测面修复（a3b29e0）与重连修复均待配对成功后实证；干跑续证 ×25（206/207/225/509/203）全 PASS 无下发边；配对页无障碍检测卡验收点 PASS（3fe3a2a 手动开启实时反映「已开启 ✓」）。

## P0 首批指令（v0.1.0）

| 指令 | 能力 | FR | v0.1 计划 | 5.0.10（基线） | 5.0.13（实测设备） | 5.0.0~5.0.9 | 4.1.39~4.1.8 | 备注 |
|------|------|----|----------|----------------|-------------------|-------------|----------------|------|
| 101/102 | 新消息读取上报 | FR-01 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | 离线暂存重发（FR-46）联测：2026-11-03 真机半链已证——断网 60s 窗口内提交→服务端 queued 暂存→恢复后新会话 flushQueued 补投 E03（设备 UI 日志 seq=2 帧达）；端侧出向 journal/904 补报未装配 NOT_RUN（证据 fr46） |
| 203 | 发送消息（含@） | FR-02 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | 误发分级 L0；2026-11-03：apifox 干跑×5 全链 succeeded（errCode=0，风险级 L0，服务端代行绝不下发设备）+ 同幂等键重放 replay=true/事件数不变（不重执行）+ 变参 409/101007；真实发送按红线未执行（device_targets.json 未配置→默认 dry-run）；端侧执行 NOT_RUN |
| 206 | 创建外部群 | FR-09 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | L1：白名单+确认；2026-11-03：apifox 干跑×5 succeeded（报告 riskClass=L1/plannedAction=创建群，L1 限速节奏内）；端侧执行 NOT_RUN |
| 207/225 | 修改群信息/成员（拉踢） | FR-10 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | L1；2026-11-03：207 干跑×5 succeeded（riskClass=L1）；225 P0-S2 干跑×5 succeeded（riskClass=L0/plannedAction=修改群成员备注/标签，无 E03 下发边）；端侧执行 NOT_RUN |
| 212 | 新好友自动通过（端侧规则） | FR-15 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | 本地模式可用；2026-11-03：apifox REST 面刻意不注册（AUD-WM3-09 端侧本地动作，REST 不收）——提交 5 次均 400/101002 UNKNOWN_COMMAND（设计负例，fail-closed 一致）；端侧规则执行 NOT_RUN |
| 501/502 | 群/好友列表查询 | FR-28/29 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | Schema 校验+干跑×5/5 succeeded（P0-S2 复跑再 ×5 全 PASS）；执行器已装配（437a8a4）并首获真实执行回执（P0-S2，2026-11-03）：每迭代重配对绕 W-M4 45s 会话缺陷，下发→执行→RAW_CONFIRM 全链 3/4 迭代闭环，但执行器首步 openContactsTab NODE_NOT_FOUND(201001) ×3（iter1/2/4，iter3=FR-53 熔断 503/101009 如实冷却）→ **0 次成功，⬜ 不翻（docs/12 铁律2 终局停 501；502 同流程未尝试，502/dod.json）**；已证伪 wm4「无登录态」定性（会话内截图=企微 5.0.13 已登录且「通讯录」Tab 在 uiautomator dump 可见）；根因两层：①MIUI 下 settings-put 启用无障碍不触发 AMS 绑定（P0-S2 已定位并修复：accessibility_enabled 0→1 往返后 Bound services 生效，capabilities=161 含 CAN_RETRIEVE_WINDOW_CONTENT）②绑定生效后 rootInActiveWindow 仍观测不到企微主窗节点（**a3b29e0 已改 interactiveRoots() 按包名挑窗，待实证**）→ 人工系统设置授权已开（会话 3 检测卡「已开启 ✓」实证）；**会话 3（2026-11-03 17:4x，证据 local-private/evidence/session3/）**：最新 HEAD 重装后配对流两败铁律 2 终局停——败2 根因确凿=MIUI 中文 IME 拼音劫持 `adb input text`（`wss://192.0.2.10:8463`→`我是说：1//92。1685。。53：8463`，App 校验器正确拒绝并提示格式=正面证据），败1=force-stop 后绑定未及重建→SETUP_GUIDE 路由现场；**501/502 真实执行 0 次（被配对阻断，SP-1/45s 重连修复复测同 NOT_RUN——两个修复未获设备实证）**；下会话准入：IME 英文态/剪贴板通道修复驱动后重走配对一次成功即解锁全链；**会话 4（2026-11-03 20:1x~20:31，证据 local-private/evidence/session4/）**：LabPairingActivity 一页式配对**首轮即中**（数字码零 IME 劫持面，会话 3 卡点破）→ 持久会话内 501 真实下发**全链 313ms 闭环**（enqueue→dispatch→transport_ack→exec_start→exec_result，command_events 权威列）但 exec_result=failed 201001（openContactsTab 首步）→ iter2 触发 FR-53 熔断、60s 冷却>45s read deadline 会话亡→重试 queued 不投递→铁律 2 停 501；**⬜ 不翻（0 成功）**；201001 本次不可归因 App：执行窗（20:18:05.9~06.3）与外部并发自动化完全重叠（pid8660 RIG 同毫秒级遍历无障碍节点树、20:02 注入触点 PairingActivity、外部于 20:02/20:25 两度操作系统无障碍设置并于 20:26 前撤销本会话授权、18:01 遗留外部孤儿 queued 501）；App 侧静态核验全过（flagRetrieveInteractiveWindows 在装机构型/dumpsys 框架窗表见企微主窗 active/真机树「通讯录」Tab=TextView 与选择器精确匹配/执行路径三文件装机后未变更）；会话按铁律 1 终止+被动撤离（未撤无障碍——授权系外部所撤）；**待所有者裁决并发来源+重新授权后复跑（首轮失败即采「执行瞬间前台+绑定态」双采样定性）**；证据 local-private/evidence/p0-s2/ + session3/ + session4/ |
| 509 | 上传本地文件/日志 | FR-33 | ✅计划 | ⬜ | ⬜ | ⬜ | ⬜ | 语义=GET_LOCAL_FILE 上传（audit/02 F30），非"列表"；2026-11-03：apifox 干跑×5 succeeded（L0）；端侧执行 NOT_RUN |

## L1/L2 危险与扩展指令（v0.2+）

| 指令 | 能力 | FR | 风险级 | v0.1 计划 | 状态 |
|------|------|----|--------|----------|------|
| 229/230 | 批量发送/合并发送 | FR-06 | **L1** | ❌ v0.2 | 限速+白名单+去重窗（FR-53）；apifox"批量发送指令"接口对应编号【待确认】 |
| 219 | 解散群 | FR-11 | **L2** | ❌ v0.2 | 独立 approver 确认 |
| 234 | 删除联系人 | FR-20 | **L2** | ❌ v0.2 | 独立 approver 确认；"236 单向删除"编号已证实（doc-9564531 §236：删除单向微信客户，≤50/次、部分成功 201102+successList/failList） |
| 304 | 清空客户端指令 | FR-37 | **L2** | ❌ v0.2 | 清空需确认；apifox"清除指定/插队/调用查询"对应编号【待确认】 |
| 204/205/227/228 | 回复/转发类 | FR-03~05 | L0 | ❌ v0.2 | — |
| 213/220 | 加好友（手机号/群内） | FR-16/17 | L1 | ❌ v0.2 | — |
| 208/209/210/211/218/224 | 微盘/小程序/文档/文件/链接 | FR-12~14 | L0/L1 | ❌ v1.0 | 小程序受 R6 约束 |
| 226 | 撤回消息 | FR-07 | L1 | ❌ v1.0 | — |
| 221/222/223/233 | 待办/打卡/切企业/扫码 | FR-24~27 | L0 | ❌ v1.0 | — |
| 301/302 | 运行日志上报 | FR-49 | — | ❌ v0.2 | 默认脱敏可关 |
| 235 | 点击聊天推荐条（收款/企业名片/推荐客服/商品图册） | — | **L2**（未登记 fail-closed） | ❌ v0.2 | **缓期登记（REV-SRV-03，2026-11-03）**：官方指令页已定案（doc-9564531 §235），排期 W-M6/B3——实现批次前 openapi 注册表不收（提交即 101002），收时须同步 scheduler 分级表（FR-53 裁决③） |
| 243 | 二维码图片 URL 入群（个微扫码入群流） | FR-56 | **L2**（未登记 fail-closed） | ❌ v0.2 | **缓期登记（REV-SRV-03，2026-11-03）**：官方指令页已定案（doc-9564531 §243），依赖 FR-56 WeChat 规则包，排期 W-M6——注册表排除集防漂移断言 TestOpenAPIRegistryExclusionSetLocked 锁定 |
| 305/306 | 清除指定指令 / 队列查询 | FR-37 | **L2**(305)/L0(306) | ⬜（REST 等价面已交付） | **REST 承接登记（REV-SRV-03/05，2026-11-03）**：305 经 `DELETE /openapi/v1/robots/{id}/queue/{commandId}`、306 经 `GET …/queue` 等价交付（不作为可提交指令类型注册）；305 语义经 L2 审批门（与 304 同门，未携审批 202 登记 → 决议后携 `X-WT-Approval-Id` 执行）。**管理面豁免**：`/api/v1/admin/queue*` 同语义操作不设审批门——admin scope 即授权决议面且落审计（SR-12），差异显式登记于此。真机端到端随批次证据回填 |

## 事件与平台能力

| 能力 | FR | 5.0.10 | 备注 |
|------|----|--------|------|
| 会议信息回调（MeetingInfo/Member） | FR-35 | ⬜ | 闭源版 delta |
| 图片接收（录屏路） | FR-08 | ⬜ | FileObserver 默认禁用（E11） |
| OCR 兜底 | FR-45 | ⬜ | R5 评估未决 |
| 音视频自动拒接 | FR-51 | ⬜ | — |
| 文件上传 `POST /fileUpload/upload`（B2 交付=自建文件 API） | FR-34/37 | ⬜（服务端面已交付） | **已交付（REV-SRV-02，2026-11-03）**：multipart part 名 `file`、≤50MiB、sha256 校验、Bearer+幂等键；取回 `GET /files/{fileId}`（能力 URL：fileId 为 128-bit UUID 不可枚举，设备端无 HTTP Bearer 凭据故不带鉴权——需收紧的部署置于反代层）；`url` 可被 218 fileUrl 闭环引用。真机端到端随 B2 批次证据回填 |
| 机器人消息回调配置 `POST /robot/robotInfo/update`（b2 §2.1 QA 面） | FR-35 | ⬜（服务端面已交付） | **已交付（REV-SRV-02，2026-11-03）**：openCallback(0/1)/replyAll 必填、callbackUrl 选填（SR-09 loopback 纪律同回调绑定）；配置落 `openapi_robot_settings`。**dodDeferred**：openCallback=1 时新消息推送到 callbackUrl 的实际分发属网关/事件车道（与回调组 type 5/6 同口径） |

> 未列出的其余历史指令（对应 FR-21~23/30~32 等）随版本滚动补入本表；历史全集以 `local-private/audit/02` D 表与 apifox 文档为准，双线共用编号映射见 `docs/07-EXISTING-CODE-MAPPING.md`。
