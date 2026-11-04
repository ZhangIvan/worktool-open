# 更新日志（Changelog）

本项目[语义化版本](https://semver.org/)管理；发布节奏见 [docs/00](docs/00-MASTER-PLAN.md)。
未发布条目按里程碑车道滚动登记；每条面向使用者（开发者/部署者）视角描述"变了什么"。

## [Unreleased]

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
    journal 重放不再排在全历史之后（W1 5.5min 派发延迟根因）（033b7a8）。
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
