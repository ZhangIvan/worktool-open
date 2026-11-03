# 全量交付清单（Master Delivery Checklist）v1.0

- 版本：v1.0（2026-11-02）；目的：**零遗漏落地核对表**——每一项规划都要有归属阶段与状态，AUD-G 终审逐行勾选；任何"无归属"项=编排缺陷必须立即派阶段
- 阶段代号：W-M3（跑中）｜W-M3R（下一棒，已预写脚本）｜W-M4｜AUD-G｜主会话｜v0.2+（发布后 backlog）
- 状态：✅ 完成｜🔄 进行｜⏸ 待启｜N/A（本版不适用）

## A. 功能需求 FR（54 项全量）

### v0.1 范围（P0）
| FR | 内容 | 阶段 | 状态 |
|----|------|------|------|
| FR-01 | 新消息监听上报(101/102) | W-M3R·补执行器车道+真机 | ⏸ |
| FR-02 | 发送消息(203) | W-M3(T3.1)→W-M3R 真机 | 🔄（W-M4 G-1 复核 2026-11-03：干跑×5 PASS（FR-54 服务端同引擎代行报告，E05=无设备传输；证据 local-private/evidence/2026-11-03/wm4/203/）；真实发送仍无白名单测试目标（用户裁决①）→ 真实发送 NOT_RUN 保持 ⬜；端侧执行层已装配（T2.6 回退+执行回路），回路闭环性由 501/502 真实下发失败路径×5/类证明） |
| FR-09 | 建群(206) | W-M3(T3.1)→W-M3R | 🔄（W-M4 G-1 复核：干跑×5 PASS（plannedAction=创建群… riskClass=L1；证据 wm4/206/）；真实建群=真实副作用按红线 NOT_RUN；端侧执行器已装配未成功路径实测） |
| FR-10 | 改群/成员(207/225) | W-M3(T3.1)→W-M3R(dry-run) | 🔄（W-M4 G-1 复核：207 干跑×5 PASS（L1；证据 wm4/207/）；225 P0-S2 干跑×5 PASS（riskClass=L0，无 E03 下发边；证据 p0-s2/225/）；真实改群按红线 NOT_RUN） |
| FR-15 | 好友自动通过(212 规则) | W-M3(T3.1/T3.3)→W-M3R(dry-run) | 🔄（W-M4 G-1 复核：212 干跑提交×5 均 400/101002 设计负例一致（REST 面不注册，AUD-WM3-09；证据 wm4/212/）；端侧规则执行 NOT_RUN） |
| FR-28/29 | 群/好友查询(501/502) | W-M3R·补执行器车道 | ⏸（**会话 4 更新 2026-11-03 20:1x~20:31**：LabPairingActivity 一页式配对**首轮即中**（数字码零 IME 劫持面，会话 3 卡点破；hello_ack+执行回路启动实证）→ 持久会话内 501 真实下发**全链 313ms 首次闭环**（enqueue→dispatch→transport_ack→exec_start→exec_result，commands+command_events 权威列）但 exec_result=failed 201001（openContactsTab 首步）→ iter2 熔断 503/101009、60s 冷却>45s read deadline→会话亡（寿命 60.2s）→重试 queued 不投递→铁律 2 停 501，**0 成功 ⬜ 不翻**；201001 本次不可归因 App：执行窗与**外部并发自动化完全重叠**（pid8660 RIG 同窗遍历无障碍节点树、20:02 注入触点 PairingActivity、外部操作系统无障碍设置并于 20:26 前撤销本会话授权、18:01 外部孤儿 queued 501）；App 静态核验全过（flagRetrieveInteractiveWindows 在装机构型、dumpsys 框架窗表见企微主窗 active、真机树「通讯录」Tab=TextView 与选择器精确匹配、执行路径三文件装机后未变更）；铁律 1 终止+被动撤离（**未撤无障碍——授权系外部所撤**，f6a3335 规程遵守）。**待所有者裁决并发来源+重新授权后复跑**（s4_driver.py go 501；首轮失败即采「执行瞬间前台+绑定态」双采样定性；遇熔断改重新配对勿原地冷却）。证据 local-private/evidence/session4/；——**会话 3 更新 2026-11-03 17:5x**：最新 HEAD（a3b29e0 重连重建入口+interactiveRoots 观测面、1ec29fb SP-1、3fe3a2a 检测卡）重装 D1 后，配对流两败 docs/12 铁律 2 终局停——败 2 根因确凿=**MIUI 中文 IME 拼音劫持 `adb input text`**（`wss://192.0.2.10:8463`→`我是说：1//92。1685。。53：8463`，App 地址校验器正确拒绝并提示格式=正面证据，**驱动侧缺口非 App 缺陷**）；败 1=force-stop 后无障碍绑定未及重建→AppLaunchRoute 落 SETUP_GUIDE+系统设置页现场。**501/502 真实执行 0 次（被配对阻断）⬜ 不翻**；SP-1 自启/45s 空闲重连修复复测/观测面修复实证同被阻断 NOT_RUN；正面收获：配对页检测卡实见「✅ 无障碍服务 已开启 ✓」（3fe3a2a 验收点 PASS，所有者手动开启实时反映）；干跑续证 206/207/225/509/203 ×25 全 PASS 无 E03 下发边；撤离例程完成（所有者手动开的无障碍授权已按规程移除，下会话需重开）。下会话准入：驱动 IME 英文态/剪贴板通道修复后重走配对，一次成功即解锁 501/502×5+SP-1+45s 重连全链。证据 local-private/evidence/session3/；——前史（**P0-S2 更新 2026-11-03 15:1x**：执行器 437a8a4 装机后真实执行链首获端侧回执——每迭代重配对绕 45s 会话缺陷，下发→执行→RAW_CONFIRM 全链 3/4 迭代闭环，但执行器首步 openContactsTab NODE_NOT_FOUND(201001)×3（iter3=FR-53 熔断冷却）→ 0 次成功，铁律2 终局停 501、502 dodge；wm4「无登录态」定性已被证伪；根因两层：①MIUI settings-put 启用无障碍不触发 AMS 绑定（已修复）②绑定后 rootInActiveWindow 仍不见企微节点（a3b29e0 已改 interactiveRoots() 按包名挑窗，待实证）；证据 local-private/evidence/p0-s2/；另 W-M4 规格缺陷（45s 关流+重连拨死会话 URL→404，session_death_forensics.json）→ **重连 URL 已修（a3b29e0），45s 复测待会话**） |
| FR-33 | 上传本地文件(509) | W-M3R·补执行器车道（dry-run） | ⏸（W-M4 G-1 复核：干跑×5 PASS（plannedAction=上传本地文件/日志 riskClass=L0；证据 wm4/509/）；真实上传=真实副作用按红线 NOT_RUN） |
| FR-34 | apifox 兼容 REST | W-M3(T3.2 子集)→W-M4(全量冒烟) | 🔄（2026-11-03 driver：S1 实部署面 7 类指令干跑×5 + 真实下发/RAW_CONFIRM/幂等 409/404 全部实测通过） |
| FR-35 | 回调(签名/重试) | M2 已实现→W-M4 生产化(WTO-008 对齐自查) | ✅基线 |
| FR-38 | 定时任务 cron | W-M3(T3.2) | 🔄 |
| FR-39 | 关键词自动回复 | W-M3(T3.3) | 🔄 |
| FR-46 | 离线暂存重发 | M2 ✅→W-M3R 真机验证 | ✅基线（2026-11-03 driver 真机半链：断网 60s 窗口提交→服务端 queued 暂存→恢复后新会话 flushQueued 补投 E03、设备 UI 日志 seq=2 帧达；端侧出向 journal/904 补报未装配 NOT_RUN——证据 local-private/evidence/2026-11-03/driver/phaseD_fr46.json） |
| FR-52/53/54 | 接管/误发保护/干跑 | W-M3(三车道)→W-M3R 抽查 | 🔄（2026-11-03 driver：FR-54 干跑绝不下发设备（设备侧状态不变式留证）；FR-53 L1 分级/限速在 206/207 干跑序列中按 10/min/目标节奏实测放行；FR-52 端侧暂停门未涉及。W-M4 G-1 增证：FR-54 干跑=服务端同引擎代行（E05『无设备传输』）；FR-53 全局熔断实测翻开（真实执行连续 201001 失败→closed→open+half_open→open×2，audit_log id 246/355/364，503/101009 拦截后冷却放行；证据 wm4/server/session_death_forensics.json）） |

### v0.2+（发布后 backlog，本表登记防丢）
FR-03~08、11~14、16~27、30、31（转发/微盘/小程序/待办/打卡等：v0.2~v1.0 按 01 优先级）｜FR-36 AI 回复（v0.2）｜FR-37 队列管理面（v0.2，核心已具备）｜FR-40~42（W-M4 覆盖 40 最小面，41/42 v0.2）｜FR-43 加密开关（实现 ✅，配置面 W-M4）｜FR-44 自更新（v0.2；Release Gate G7 本版 N/A 注记）｜FR-45 OCR（R5 评估未决，v1.0）｜FR-47~51（v0.2+）

## B. 安全需求 SR（14 项）
| SR | 阶段 | 状态 |
|----|------|------|
| SR-01~06、08~10、12、13 | M1/M2 已落地（CI 门禁+实现），W-M4 Release Gate 终验 | ✅基线 |
| SR-07 更新安全 | v0.2（FR-44 联动；G7 用例 N/A 注记） | ⏸ |
| SR-11 外链白名单 | W-M3(T3.2 白名单)→W-M4 用例 | 🔄 |
| SR-14 静态数据 | W-M4：SQLite 保留期/清理配置+明文披露 README | ⏸ |

## C. 任务卡与基础设施
| 项 | 阶段 | 状态 |
|----|------|------|
| T1.1~T1.3、T2.1~T2.4 | M1/M2 | ✅ |
| T2.5 RIG 首连 | 主会话 | ✅（RIG 10.8 root；设备 picasso/Android12/企微 5.0.13） |
| T3.1/3.2/3.3 | W-M3 | 🔄 |
| T3.4 协议 v2 收尾（901~904 控制帧 schema 冻结） | W-M4 | ⏸ |
| C-5 S1 docker build 冒烟 | W-M4 | ⏸（S1 先 prune，磁盘 31G） |
| C-7 Release 签名路径修复 | W-M4（含真实签名构建验证） | ⏸ |
| C-13 gradle wrapper | W-M4（S1 镜像内生成并提交） | ⏸ |
| infra/compose.yaml（server+TLS+可选 PG） | W-M4 | ⏸ |
| admin 最小面（React+TS：配对码/在线状态/指令审计/审批） | W-M4 | ⏸ |

## D. 文档与对外可用（G-5）
| 项 | 阶段 | 状态 |
|----|------|------|
| 00~08 规划/章程 | 已入库（08 台账随阶段更新） | ✅ |
| 09 本清单 | 已入库，各阶段完成即回填 | ✅ |
| README 快速开始（外部用户三步：compose 起 server→装 APK→配对） | W-M4 | ⏸ |
| SECURITY.md、CONTRIBUTING.md、CHANGELOG.md | W-M4 | ⏸ |
| THIRD-PARTY-NOTICES 实化（实际依赖清单） | W-M4 | ⏸ |
| 05 支持矩阵：基线扩 5.0.13 实测列；P0 行真机取证回填 | W-M3R | ⏸ |
| docs/06/07 双线映射刷新（对 Codex 线最新快照） | AUD-G 前置 | ⏸ |
| Release 附件（APK/server/compose/checksums/矩阵） | AUD-G→v0.1.0 | ⏸ |
| 证据归档 local-private/evidence/{date}/（真机截图/日志，永不入库） | W-M3R | ⏸ |

## E. 发布门（04 §5.1 Release Gate 逐项 → 阶段）
G1~G13 全绿=W-M4｜依赖审计三线（govulncheck/Gradle/npm）=W-M4｜gitleaks=W-M4｜mini 渗透（未授权/伪造签名/重放/fileUrl 越域/更新包错签名 N/A 注记）=W-M4｜断网隐私矩阵=W-M4（借 runbook 阶段5）｜FR-52/53/54 抽查=W-M3R｜台账 A/B 清零=每阶段｜05 矩阵 NOT_RUN 不得标支持=W-M3R+AUD-G

## F. 阶段 dodDeferred 登记（AUD-05 要求，防丢）
| 项 | 归属 | 状态 |
|----|------|------|
| T3.1/T3.3 真机执行 DoD（FakeNode 只覆盖 JVM 侧） | W-M3R 真机取证 | ⏸ |
| T3.3 断网场景验收（fallback 阈值/去重上报真机链路） | W-M3R | ⏸ |
| W-M3 内部 CR 13 项残留 | ✅ 已由主会话收口评审闭环（仅 B1 真实存在，已修+测试） | ✅ |
| e2e 双路径垫片入口统一（收口 C5） | W-M4 | ⏸ |
| **Ed25519 API24~32 回退提供者（产品缺陷：Android 7~12 无法生成配对必填密钥）** | W-M4（T2.6，dwfq-f63d4173-1 裁决；本阶段 lab 注入密钥过渡） | ⏸ |
| **hello_ack 格式拒收（Go 产/python 收/Kotlin 拒=三方不一致→规格缺陷，901~904 控制帧 schema 未冻结）**：设备执行层因此未装配、G-1 阻塞 | **W-M4 第一优先级（T3.4）：schema 冻结+三实现对齐+设备执行回路接线 → 复跑取证翻矩阵** | ⏸ |
| 证据卫生三件（AUD-06 F2 command_events 重采 / F3 截图按 magic-bytes 命名 / F4 送验 APK 归档） | W-M4（取证脚本固化） | ⏸ |
| **用户裁决（2026-11-03）**：①203 白名单测试目标暂无 → 真实发送取证保持 dry-run，待用户提供后一轮补；②自动降亮度=需要 → FR-55（W-M6）；③个人微信双端=需要 → FR-56（W-M5 起每批双规则包，矩阵加 App 维度，R7 默认关） | FR-55/56 已入 01/10 | ✅ 已登记 |
| **双源深审结果登记（2026-11-03）**：APK 功能面重扫（apk-resweep，15 项建议）+ apifox 全量爬取（apifox-crawl，60/60 页，P0×8/P1×17）——处置已落 docs/01（FR-57/58 新增、FR-01/08/31/34~37/40/46/48/55/56 修订勘误）、docs/03（T3.5 事件回调分发）、docs/05（兼容基线 4.0.2~5.0.12 加注）、docs/10（CMD-243 增补 68 条、事件类回调 B3 任务卡、【确认】项全部落定、路线图观察项） | 报告存 `local-private/parity/`（apk-resweep.md、apifox-crawl.md）；错误码表并入 protocol/ 留 W-M4 收尾后下一棒 | ✅ 已登记 |
| **R2 双方吸收复核（2026-11-03，docs/11）**：证据四分法固定（本地快照/公开 8fd/另一侧 181b5784/未核验声明分层）；吸收对侧 backend-go+PG-lease、Node 回归资产、黄金向量（协议 1.2.0↔1.1 映射先行，PC0.1-r1 按候选）；新任务卡 T4.1 暂停三分法（PauseManager 统一零副作用已静态定位）/T4.2 执行台账故障注入/T4.3 PG 迁移验证（lease-fencing·commit-unknown）/T4.4 选择器零匹配拒绝（child0 回退已静态定位 SelectorEngine.kt:46）/T4.5 guard 对象化默认拒收/T4.6 后置效果验证+剪贴板卫生/T4.7 协议版本映射/T4.8 目录分族；04 新增 §9 完成分层规则 | docs/11 已入库；T4.x 代码动工**待所有者批准**（建议并入 W-M5 开工包） | ⏸ 待批 |
| **P5 预演发现（2026-11-03）：G11 fileUrl 服务端强制树内零命中**——SR-11 外链域名白名单仅有文档/用例，server 无实现 → **Release Gate 阻断级缺口**（静态推导，未复现） | 并入 W-M5 server-b2 车道（T4.5 同批落地 fileUrl 白名单五路拒绝）+ AUD-G 前复验 | ⏸ 阻断项 |

## G. 编排衔接规则（防断链）
1. 每阶段 workflow 完成通知 → 主会话 24h 内：四门核收 → 台账/本清单回填 → **立即以 path 发射下一阶段脚本**（W-M3R/W-M4 脚本预先写好存 `.zcode/workflow-drafts/`）；
2. 任一阶段失败：抢救 report → Amend 修复续跑，禁止推倒重来；
3. 本清单每阶段结束由主会话全量 diff 一次，新发现"无归属项"当场派阶段并记 AUD；
4. AUD-G 终审以本清单逐行勾选为验收底稿，未全勾不放 v0.1.0。
