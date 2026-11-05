# 更新日志（Changelog）

本项目[语义化版本](https://semver.org/)管理；发布节奏见 [docs/00](docs/00-MASTER-PLAN.md)。
未发布条目按里程碑车道滚动登记；每条面向使用者（开发者/部署者）视角描述"变了什么"。

## [Unreleased]

### 指令字段契约冻结（2026-11-05 契约冻结车道）

- **管理台目录 52 条 example × 端侧执行器载荷解析路径契约冻结**：逐条审计出 22 条
  漂移并全部处置（决策理由逐条登记 catalog.go 注释）——
  - catalog example/required 修 19 条：202（loop.on→loopEnabled）、204/205（补
    receivedName/originalContent 引用定位键）、205/227/228（弃 originTitle 幻影字段，
    upstream 契约=titleList 源会话+messageList 原文定位键+nameList 接收人）、
    208/209（fileUrl→objectName，微盘推送不下载）、218（补 objectName+fileType）、
    210/211/224（补执行器必填 objectName/extraText）、223（corpName→objectName）、
    229/230（titleList/receivedContent 错位→weworkMessageList+nameList）、
    238（receivedContent=JSON 数组字符串值形态）、511/514（补 objectName 必填标注，
    514 弃 messageId 幻影字段）；
  - 端侧执行器迁移 4 指令（catalog/upstream 同名优先）：203 目标 targetRoom→
    **titleList 主读**（多元素显式拒绝，单目标语义；候选别名迁移期兼容）、206 成员
    groupMembers→**selectList 主读**（修复目录 selectList 提交被端侧静默忽略的
    拉人名单丢失缺陷）、207/225 目标 targetRoom→**groupName 主读**；
  - 机器锁定：跨语言交换夹具 protocol/fixtures/admin-command-examples.json
    （与 catalog 字节级同源，`TestCatalogExamplesFixtureInSync` 锁同步）+ Kotlin
    `CatalogExampleContractTest`（52 条 example 经 `IntentNormalizer` 全量可消费 +
    第二批表单八指令逐字段深断言）。**管理台批量表单第二批（204/205/218/227/228/
    229/230/238）自此可按 catalog 消费**（docs/16 §指令字段契约冻结）。

### 513/目录/矩阵口径对齐（2026-11-05 矩阵口径对齐车道）

- **支持矩阵补 513 查询设备资源行（⬜ 不翻如实）+ 目录 StatusVerified 与矩阵翻格门口径对齐**（docs/05 v1.10）：矩阵行内登记双缺口——①数据本体 query_result 事件回调契约面从未实测捕获（session10 s10f 回调监听落盘 0 字节；双轨契约见 local-private/parity/b3/events.md §五）；②结果帧时序收敛未复验（session11 实测滞留 ~5.5min=W1，H1/H2 已修根因；session20 复测 90s 窗 timeout，窗短于最坏滞留且设备构建血统未记录）。回执终态面真机 succeeded ec=0 ×2（session10/11）如实行内引用。目录侧 catalog.go 注释同步：513 的 verified=回执终态面、非全义；`TestCatalogStatusLifecycle` 冻结的 verified 集 {501,502,513} 不变。

### v0.1.0-rc2（2026-11-04，发布候选——选择器真机校准批 + 管理台 v3 设计稿对齐整改）

> 候选版内容汇总（未打 tag）。端侧执行面口径不变：支持矩阵仅 501/502 真机翻格 ✅；
> 本批新增 detect-only 指令前置条件（企微须人工前台）与 NOT_CALIBRATED 选择器现状
> 补记，见 docs/05 v1.9。

#### 新增

- **选择器 contentDesc 匹配支持**：`MatchSpec` 新增 content-desc 匹配维度，规则解析器
  同步支持（外置 RuleConfig 与内置 DEFAULTS 两面一致）——企微部分控件（搜索、聊天
  「更多」等）为纯 desc 无 text/resource-id 可锚，此前无法稳定命中（e539dd8）。
- **管理台 v3 设计稿对齐整改**：按 16 张设计稿 PNG + v3 规格重建——五工作区
  （工作台/设备/任务/集成/设置与审计）16 路由（含旧路由重定向）；六步任务创建向导
  （选设备→选动作→填参→干跑→提交→详情）；任务详情 E01→E19 时间线/结论/rawMsg；
  cron 管理、L2 双令牌审批、回调按机器人分组、审计日志叙事化；既有 API 逻辑
  （token/confirmBox/stale/幂等）全保留；零外部资源（sanitize 0 命中）；130 例 UI
  测试全绿（test/admin_ui_v3.spec.mjs）（44a16bb 落地 + 4cf9f0d 收口）。

#### 修复

- **核心选择器按 D1 真机 dump 校准（证据 session21）**：会话列表 ibi、聊天标题 o6j、
  输入框 iu5、消息文本 ith、搜索 m0f、通讯录 l_8/hlo、群名行 hn1→lm8（兄弟链）、
  首页搜索 desc 等 DEFAULTS 全部按真机 dump 实测重校，fixture 同步；**群设置入口改
  经「更多」按钮**（真机实证标题点击为 no-op，保留标题兜底）；JVM 门禁
  accessibility+executor+app 320/320 绿（e539dd8）。
- **detect-only 前台检测（所有者安全指令）**：端侧禁用企微自动拉起——
  `ensureTargetForeground` 只检测不操作（rootInActiveWindow/isActive 窗口判定），
  企微不在前台时指令如实失败（201010）并提示「请手动将企微切到前台」；全部
  startActivity 拉起逻辑移除，消除自动拉起触发企微风控的风险（44a16bb）。
- **501 会话定位滚动兜底硬化（rc1 遗留项核查车道收口）**：`CommonSteps.openConversation`
  滚动搜索补**正反向双向**（正向 8 页耗尽后反向上滚再搜 8 页——残留屏态下目标行在
  视口上方，正向滚永不命中；真机会话 10 首发 201002 即此类环境性失败）与**列表容器
  定向**（优先取「可滚动且含会话项」容器，防先序更前的横幅/页签等无关容器劫持滚动，
  无会话项时退回旧口径）；原版「搜索框兜底」仍未实现——正反向滚尽后如实
  NODE_NOT_FOUND（继续登记）；TDD 3 场景红→绿（CommonStepsOpenConversationTest），
  501/502 既有执行器测试零回归。
- **rc 门禁两阻断项（rc 评审发现，均实证）**：①管理台 CSP 此前 default-src 'none'
  未配 connect-src，同源 fetch 全被浏览器拦截（v3 数据面真实浏览器不可用，FETCH_BLOCKED
  对照 401 控制页验证）——补 `connect-src 'self'` 一行（script/style 锁紧不变）；
  ②pause_skip_dispatch 审计对积压暂停设备每秒泵周期逐条写入（24h TTL 下约
  8 分钟灌满 500 行审计窗）——改为每设备 1h 时间窗去重后再 InsertAudit（ac07590）。

#### 安全与卫生

- 设备日志 zip 出库：运行期产物 zip 曾被误入 git 跟踪，已 untrack（历史不重写），
  .gitignore 防回流（c802312）。

#### NOT_RUN 与已知边界（如实登记；2026-11-05 rc1 遗留核查车道逐项刷新）

- **设备链路修复（linkfix 车道）= 已交付并真机验收**（初稿误登 NOT_RUN，2026-11-05
  滚动补记更正）：客户端三处根因修复——①EndpointPolicy 裸 IP 例外进程内易失（新进程
  本地拒拨零痕迹）；②服务端重启凭据失忆 401 后重建无自愈（补 Ed25519 重协商自愈）；
  ③重建路径异常全吞无日志。epoch=0 拒旧三处修复——入站门禁缺 epoch 改「不拒旧仅去重」、
  socketType=3 回执帧单向消费防乒乓、出站 RAW_CONFIRM 回填会话 epoch（5ac3781）。
  验收（独立复核员实测）：真机 online=true（tokenVersion=2）、只读 502 ×2
  queued→succeeded（errorCode=0）、服务端重启后 401→重协商自愈实测走通；
  JVM/Go 门禁全绿（InboundGateEpochTest 5、RebuildRenegotiateTest 5、
  DeviceSessionLoopGateTest 15、Go 12 包 ok）。
- **PairingClient.renegotiate 形状不匹配 = 已修复**（face47f）：平铺
  `{deviceId,ts,nonce,ed25519Signature}` 改嵌套 `renegotiate{ts,nonce,signature}`
  对齐服务端冻结契约（spec §5.5.1），双 JVM 断言更新绿；生产自愈走 V2Session 内联
  （真机实证 tokenVersion=2），独立 API 零生产调用方，不受影响。
- **511 会议详情族选择器仍 NOT_CALIBRATED**：日历会议列表滚动容器、详情预定时间/
  实际时间/组织人值文本共 4 项占位（`WeworkScreenRules.kt` 逐项标注），真机未命中
  如实 NODE_NOT_FOUND；515「搜索到已退出群聊」分支 JVM 绿仅为分支证据——均待
  具日历入口的企业形态真机 dump 校准（docs/05 v1.7 补记 + v1.8 入口形态差异：
  当前测试企业工作台无「日历」入口，该形态下 511 首步 NODE_NOT_FOUND 属
  fail-closed 预期）。
- **rc1 遗留项逐项现状（2026-11-05 核查，能力面零变化）**：
  - **501 openConversation 滚动兜底硬化 = 本批补齐双向+容器定向**：W-M6 会话 10b
    已交付正向 8 页滚动搜索；本批补正反向双向（8+8 页）与列表容器定向（先序无关
    可滚动容器不劫持，`CommonStepsOpenConversationTest` 3 场景红→绿）；**仍开放**=
    原版「搜索框兜底」未实现（正反向滚尽后如实 NODE_NOT_FOUND）+ 真机复验未做
    （本轮纯代码+测试，不宣称真机闭环）。
  - **MIUI BAL 201010 = 口径已更正（改文案不改行为）**：44a16bb detect-only 后
    「拉起失败/服务上下文被 BAL 拦」路径已不存在——rc1 条目与 docs/05 501/502 行
    行内表述统一为「**前台不可得如实失败**（须人工将企微切到前台）」。
  - **Ed25519 装配/真机取证 deferred = 已实质闭环（升级登记）**：产品调用方已落地
    ——配对面 `PairingActivity`（三级解析 + Keystore 加密种子持久化）、会话面
    `SessionRuntime` 重协商签名器（同源解析）；真机取证完成——D1（Android 12
    JCA 无 Ed25519）即走纯 JVM 回退路径，session 21 实证配对 tokenVersion=2 +
    服务端重启 401→重协商自愈（5ac3781，独立复核员）。注：取证经 lab 配对面
    （与产品面同解析函数、种子店镜像同源），产品 PairingActivity 面未单独真机复验。
  - **端侧执行面 NOT_RUN = 仍有效**：矩阵仍仅 501/502 真机 ✅（docs/05 v1.10），
    本批零能力变化，其余指令行维持 ⬜ 不宣称。
  - **目录/矩阵口径差异 = 已收口（2026-11-05 矩阵口径对齐车道滚动更正，原
    「仍有效」表述作废）**：docs/05 v1.10 补 513 行（⬜ 不翻如实）并在证据口径节
    登记目录 StatusVerified 与矩阵翻格门差异（513=回执终态面 verified，数据本体
    契约面待补）——对外仍以矩阵为准，差异由静默变为显式登记。
  - **已知 flaky `TestOpenAPIQueueGroup` = 仍有效**（Windows 时钟粒度幂等键碰撞，
    先于 rc1 存在）。

#### 收口后批（2026-11-05，rc2 条目落定后的冲刺收尾产出；按实际提交登记）

> rc2 小节写入（face47f）之后、本文件首个 rc 后续批次入库的实际提交，逐条对齐 `git log`；
> 契约冻结批详见本文件顶部 [Unreleased]「指令字段契约冻结」节（9d40409，不重复展开）。

- **运维/可观测批（9629254）**：会话级日志（v2 网关会话维度留痕）+ outbox/已关会话
  retention 清扫（store 周期清理，容量上限防膨胀）；main 装配 + 活性与清扫回归测试。
- **rc1 遗留核查批（ec38e32）**：501 会话定位滚动兜底补**正反向双向**（正向 8 页耗尽后
  反向上滚再搜）与**列表容器定向**（无关可滚动容器不劫持）；201010 文案与 detect-only
  口径全仓同步；新增 `CommonStepsOpenConversationTest` 3 场景。
- **契约冻结批（9d40409）**：管理台目录 52 条 example × 端侧载荷解析 22 漂移清零，
  机器锁定双夹具——第二批表单（204/205/218/227/228/229/230/238）自此可按 catalog 消费
  （顶部专节详述）。
- **审计登记（233a2a0）**：511 日历入口形态差异登记（`WeworkScreenRules.kt` 注记 +
  docs/05 加行）——当前测试企业工作台无「日历」入口，该形态 511 首步 NODE_NOT_FOUND
  属 fail-closed 预期。
- **测试卫生（7aa12f7）**：已登记 flaky `TestOpenAPIQueueGroup` 修复——openapi 夹具
  幂等键改单调递增（Windows 粗时钟下 UnixNano 碰撞致同键变参 409 误报），`-count=10`
  10/10 绿；上节「仍有效」条目自此关闭。

### v0.1.0-rc1（2026-11-04，发布候选——六车道交付批 + 审计修复批收口）

> 候选版内容汇总（未打 tag，发布门 docs/04 §5.1 未全绿）。端侧执行面如实口径：
> 支持矩阵仅 501/502 真机翻格 ✅，其余指令 ⬜ NOT_RUN，详见本节末「NOT_RUN 与已知边界」。

#### 新增

- **审批身份（approver token scope，P0-3 结构性收口）**：决议面独立签发 approver
  令牌，与管理令牌分持（启动时各打印一次、库中仅存 SHA-256 hash）；admin 令牌在
  决议面 403 `APPROVER_SCOPE_REQUIRED`、approver 令牌在提交面 403
  `ADMIN_SCOPE_REQUIRED`——两侧 scoped 身份关闭自由文本自审旁路；管理台双令牌
  UI + 4 项 DoD 回归测试（ce128c3）。
- **52 指令目录注册表 + status 能力履历**：`GET /api/v1/admin/catalog` 52 条
  （编号面=openapi 注册表，中文场景名/风险级/必填字段/最小示例/分组）；每条目带
  status 履历标记（registered→implemented→wired→verified）与 rawMsg 通道注记 +
  withSchema 矩阵；7 项机检不变量（编号集合=registry、example 过 prevalidate、
  risk=ClassifyRisk 等）锁漂移；顺带修正 6 处风险级展示漂移（223 L1→L0、
  236/238/239/240/244 L0→L2）（006c046/ee6a9be/ce128c3）。
- **cron 管理面**：定时任务 CRUD+preview+enable+delete；riskClass 伪装提交拒绝、
  GuardError 显式映射；管理台 cron 页（ce128c3）。
- **/readyz 活性探针**：store ping 之上增加 dispatch/callback/cron 三个后台循环
  活性检测（3×周期无推进→503），活性（liveness）与就绪（readiness）分开报告
  （ce128c3，`server/internal/api/api.go:226`）。
- **回调闭环（rawMsg 契约 + 配额 + 签名取回）**：rawMsg 端到端透传契约（wire 解码
  层曾丢字段——025712b；查询结果经 RAW_CONFIRM rawMsg 携数据本体、成功时省略
  successList——5a04820；错误/缺省分离与双机器人隔离回归——f8c84d1）；clientlog
  上传 200MiB/天配额（元数据表 + os.Stat 实证核验）；`/files` 取回可选 HMAC 签名
  访问（`-callback-secret` 未配置时签名路径 fail-closed 403 不降级、响应 no-store，
  7 分支回归）（ce128c3）。
- **管理台 v3 重建**：五工作区 + 六步核心流程（常规路径不暴露 JSON/UUID）+ 每页
  五态 + 浅色科技主题；120 例 UI 测试套件绿；回调列表加载失败显式重试而非假空态
  （review 修复，70f5d38 前置 + ce128c3）。

#### 修复

- **E19 排队 TTL**：设备离线积压指令超 24h（默认）自动 queued→cancelled
  （`queue_ttl_expired`），边入权威 CSV + state-policy；TTL 取消幂等并补发
  exec_result 101018 回执（对齐 admin queue-remove 语义与 CSV 承诺）；提交响应携
  deviceOnline、控制台离线横幅与队列年龄徽章（843be63 + 033b7a8-B4）。
- **白名单规范字段提取（P0-1）**：白名单门此前只识别 4 个旧收件人字段——规范字段
  titleList（203/204/205/221/226/227/229/238…）、selectList（206/225）、嵌套
  friend.name/phone（213/220/234/236）对门不可见，默认拒绝下仍可入册；现对全部带
  收件人指令检查白名单（不再只限 L1，203 误发防护主目标首次入保护），不可识别形态
  fail-close；负例回归 `TestL1WhitelistCanonicalFields`。同批 **P0-2 守护策略启动
  恢复**：持久化守护策略此前无启动调用点（重启后静默失效而查询 API 假成功），现
  服务前置恢复+校验，失败拒绝启动（b1e3ef4）。
- **设备身份归并**：配对按 Ed25519 身份公钥归并——同机重装/标识变更换 deviceId 时
  复用既有设备行（robot 绑定与 epoch 水位保留），响应返回既有 deviceId；管理台新增
  删除动作清理不同密钥时代遗留行；回归 `TestPairIdentityMergeByEd25519` /
  `TestAdminDeviceDelete`（006c046）。
- **安卓恢复链与 H1/H2**：
  - H1：AtLeastOnceQueue 发送成功即置 DONE 并保留 32 条 DONE、不再全表重扫——
    journal 重放不再排在全历史之后（W1=513 结果帧上行滞留 ~5.5min 的根因；
    2026-11-05 口径更正：W1 症状为端侧出站帧滞留待达服务端，非服务端派发延迟；
    收敛复验仍开放，见 docs/05 v1.10 513 行）（033b7a8）。
  - H2：设备 watchdog 将心跳成功发送计为链路活性（max(lastServerActivity,
    lastSendOk)）——服务端按规格不回心跳，此前仅认收帧导致健康会话每 45~60s 被
    close(4400) 重连（TLS 握手+会话行+全量 journal 重放/分钟）（c2f0bb6）。
  - 恢复链：设置持久化恢复链统一（boot/自愈/MainActivity 三入口合一）；修复
    skip-expired OFF 路径以陈旧 lastLoadedGates 覆盖用户刚写值（review 发现，
    reviewer 静态分析验证）（ce128c3）。

#### NOT_RUN 与已知边界（如实登记）

- **端侧执行面 NOT_RUN**：支持矩阵仅 501/502 真机翻格 ✅（502 3/3、501 3/4
  succeeded，ec=0），其余指令行全部 ⬜ NOT_RUN——未经真机执行回执的能力不宣称
  支持（docs/05 图例与规则）。
- **目录/矩阵口径差异**：catalog 源注释标 501/502/513=verified
  （`server/internal/admin/catalog.go:32`），矩阵侧 513 无行、真机证据未回填——
  对外口径以矩阵为准，两处待对齐。
- **511/515 分支**：详情族选择器 NOT_CALIBRATED 占位，JVM 绿仅为分支证据，待 D1
  真机 dump 校准，不虚构真机成功（docs/05 头部 v1.6 加注）。
- **501 已知边界**：openConversation 仅扫可视树，列表滚动位置致目标行不可见时
  201001（原版有滚动+搜索双兜底——硬化项待办）；应用后台时 Activity 上下文失活→
  服务上下文被 MIUI BAL 拦（201010），产品 SP-2 前台服务归位后消解。
  【2026-11-05 rc1 遗留核查滚动更正】①滚动兜底已交付（W-M6 会话 10b 正向 8 页；
  本批补正反向双向 8+8 页 + 列表容器定向，`CommonStepsOpenConversationTest` 红→绿），
  仅剩搜索框兜底未实现（滚尽后如实 NODE_NOT_FOUND，继续登记）；②2010 现口径 =
  detect-only 前台不可得如实失败（44a16bb 已移除全部自动拉起逻辑，本条原
  「BAL 拦截/SP-2 消解」表述作废——企微须人工切前台后指令才可执行）。
- **Ed25519 产品级回退**：装配与真机取证 deferred（见上文 W-M4 条目登记，rc1 不
  宣称闭环）。
- **已知 flaky**：`TestOpenAPIQueueGroup`（Windows 时钟粒度幂等键碰撞，先于本批
  存在）（ce128c3 提交说明）。

### W-M4（发布工程，进行中）

#### 新增

- **compose 一键部署**（`infra/compose.yaml` + `infra/server.Dockerfile`）：server（TLS 卷 +
  healthcheck `wget /healthz`）+ 可选 Postgres（`--profile pg`，服务端 PG 后端落地前的预留位）；
  S1 实机验证 `up -d --build` → 容器 `healthy`、`/healthz`/`/readyz` 探活通过（受限网络可用
  `WT_GOPROXY` 覆盖模块代理）。
- **对外文档五件套**：README 快速开始三步（compose 起 → 装 APK → 配对）、SECURITY.md（漏洞
  报告私有渠道 + 安全设计要点表）、CONTRIBUTING.md（质量门/红线/分支模型）、CHANGELOG.md、
  THIRD-PARTY-NOTICES 实化（Android/Go 全部实际依赖逐项版本+license，POM 与模块 zip 判定）。
- **取证卫生工具** `test/collect_evidence.py`：command_events 按真实列名重采（F2）、截图按
  magic bytes 校正扩展名（F3）、送验 APK 连 sha256 归档（F4）。
- **Ed25519 产品级回退提供者（T2.6，裁决 dwfq-f63d4173-1 定点豁免）**：
  `Ed25519Keys` 三级解析（JCA → lab 注入 → 纯 JVM 回退）+ `net.i2p.crypto:eddsa:0.3.0`
  钉版门禁（版本+sha256+零传递断言，`pinEd25519Fallback`）；双路径 RFC 8032 官方向量 +
  JCA/回退互认测试全绿。**装配与取证如实 deferred（CR T2.6-R2）**：全仓尚无产品调用方
  （lab 配对面仍走 lab 注入路径，产品配对面未落地），`D1` 真机「配对+回退路径重协商」
  取证未做——归后续卡（产品配对面装配 + 真机取证），T2.6 不宣称闭环；依赖面已入
  THIRD-PARTY-NOTICES（CC0 1.0 Universal，CR T34-03/T2.6-R1/REL-1）。
- **Gradle wrapper 入库**（`android/gradlew`、`gradle/wrapper/`，8.7 钉版）：新克隆可直接
  `./gradlew`；S1 构建容器内生成并验证。wrapper 分发 zip 补 `distributionSha256Sum`
  内容校验（与 `infra/builder/Dockerfile` GRADLE_SHA256 同值；CR REL-3），wrapper jar 的
  sha256 见 `android/README.md` 审计登记。

#### 修复

- **v2 幂等回执改为「同 eventId 重建帧」（CR T34-01）**：端侧重复下发回执不再原样重发旧帧
  文本（会被冻结入站规则拒收：nonce 5min→4402 / ts±30s→4403 / seq 严格递增→4405+903），
  改为同 eventId + 载荷逐值一致（同 payloadDigest）+ 新 nonce/ts/seq + 重算 HMAC——对齐
  python 规范客户端重放形态；AtLeastOnce 队列重连重放（flushPending）同口径重建（加密帧
  无安全重发形态，保持原样并登记为规格层待议项）。同时修复出向 seq 计数器复位点
  （hello 之后复位导致 hello 后首帧 seq=1 与 hello 冲突/重连 hello 带旧计数，均 4405）。
- **openapi 兼容面硬拒保留名 robot_id='admin'（CR ADM-1）**：签发（`IssueOpenAPIToken`）
  与鉴权（`authenticate`）两侧拒绝，防为名叫 admin 的 v1 robot 静默铸出第二个 admin-scope
  令牌；保留名提为跨包常量 `sqlite.ReservedAdminScopeRobotID`；反例测试
  `TestAdminTokenRejectedOnOpenAPIFace`。W-M5 RBAC 落 scope 列后收编。
- **e2e 903/904 控制帧 schema 断言改活体载荷（CR T34-02/REL-2）**：`ctl903.schema_conform`
  与 `ctl904.request_schema_conform` 此前校验硬编码字面量（恒真）；现传入实际收到的载荷
  元素（含 encryptType=2 解密后的明文）并叠加与冻结基准的 deep-equal，Go 现产形态漂移即红。
- **Release 签名路径（C-7）**：`WORKTOOL_STORE_FILE` 绝对路径按原样、相对路径按仓库根解析，
  文件缺失配置期响亮失败；CI 以 `WORKTOOL_KEYSTORE_B64` base64 secret 落盘到 runner 临时目录
  （不入库/不回显/不上传 artifact）。文档同步 `docs/ops/SIGNING.md`。
- **e2e 入口统一（AUD-05 C5）**：删除 `server/test/e2e/` 调用垫片，唯一入口 = 仓库根
  `python test/e2e/run_cross_e2e.py`。

#### 内部

- S1 构建冒烟（T1.4/C-5 关闭）：builder 镜像实机构建，cmdline-tools 与 Gradle zip 校验和
  实跑验证 `OK`；hello APK 产出 + sha256 清单。
- `infra/README.md` 重写：compose/冒烟实录/产物校验。

### W-M3R（真机装配与矩阵填充，2026-11-03）

- 真机管道贯通至**帧层**：配对/hello 校验/30×干跑/幂等重放/断网 60s 补投全部实证
  （S1 TLS 实部署 + D1 lab 变体）；
- 执行层被 hello_ack 格式拒收阻塞（901~904 控制帧 schema 未冻结，任务 T3.4）——支持矩阵
  诚实保持未验证，不宣称支持；
- 补执行器 101/501/502/509 框架位 + 170 项测试；Ed25519 lab 注入过渡（产品级回退 = T2.6）。

### W-M3（业务功能三车道，2026-11-02/03）

- P0 执行器框架（feature:executor）、scheduler+apifox 兼容层、本地任务引擎（FR-38/39/46）；
- 跨语言一致性 E2E 套件（72/72）：配对→hello→干跑→结果→墓碑重放→TTL 对账；
- FR-53 风险分级裁决落地（payload-dynamic=SSOT，未列出=fail-closed L2）。

### M2（通讯与端侧核心，2026-11-02）

- `protocol/spec.md` v1.1.0：v1 兼容信封 + v2 安全增量（配对/逐帧 HMAC（seq 纳入）/GCM）+
  状态机（E17/E18 对账边）+ 金标准向量；JSON Schema + 夹具冻结为测试接口；
- server：v2 会话网关、统一命令管线（幂等键/墓碑/崩溃恢复）、SQLite 存储（免 CGO）、
  回调 outbox（HMAC 签名/重试/死信）；
- android：core:protocol（纯 JVM 可单测）+ 协议-android 落地适配 + 无障碍骨架。

### M1（仓库与构建链，2026-11-02）

- monorepo 骨架 + Apache-2.0 LICENSE/NOTICE（上游 gallonyin/worktool 致谢与衍生声明）；
- android 壳工程（Kotlin+AGP8+JDK17，`cn.worktool.open`）+ server 骨架（SR-09 启动即拒绝）+
  CI 门禁（G2/G3/G12/G13-lite）；
- S1 构建容器方案（`infra/builder/`，版本与 sha256 钉死）。

## 发布计划

- **v0.1.0**（目标）：App 连自建 server，收发消息/建群/加好友/定时任务/关键词回复可用，
  apifox 兼容 REST 冒烟 100%；发布门 docs/04 §5.1 全绿后打 tag 出 Release（APK+server+
  compose+checksums）。
