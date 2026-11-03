# 更新日志（Changelog）

本项目[语义化版本](https://semver.org/)管理；发布节奏见 [docs/00](docs/00-MASTER-PLAN.md)。
未发布条目按里程碑车道滚动登记；每条面向使用者（开发者/部署者）视角描述"变了什么"。

## [Unreleased]

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
