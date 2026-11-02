# WorkTool-Open 总体计划（Master Plan）v2.2

- 版本：v2.2（2026-11-02，用户审阅意见落地：双线取长补短 / 安全运营 FR-52~54+SR-12~14 / 协议 v2 精确规格 / 支持矩阵与发布验收 / 敏感资料出库）
- 状态：**通过（AUD-01 关闭确认 2026-11-02）**——A×2/B×11/C×9 修复后复审通过；关闭复审提出的 6 项 C 级残留（C4 尾巴 + R1~R5）已当场修复；审计台账见 `04-AUDIT-CHECKLIST.md` §6
- 上游：gallonyin/worktool（Apache-2.0，止于 v2.8.1/2023-11，学习用历史版本）
- 定位：**社区共建的开源企微自动化机器人**（Android 无障碍端侧执行 + 自建调度后台），数据默认不出用户内网
- 本文档是唯一权威计划来源；证据见 `local-private/audit/01~03`，需求见 `01-REQUIREMENTS.md`，架构见 `02-ARCHITECTURE.md`，执行调度见 `03-EXECUTION-PLAN.md`，复核规程见 `04-AUDIT-CHECKLIST.md`

## 1. 目标与原则

| # | 原则 | 含义 |
|---|------|------|
| P1 | 端侧能力为王 | 自动化执行能力基本为端侧无障碍实现（AUD-02 证实；例外：F10 小程序/F13 链接原走企微 WWAPI SDK，见 R6）；端侧是资产核心 |
| P2 | 数据主权 | 默认全链路内网/自托管；出网流量白名单制（默认零遥测）；后台与设备可完全离网运行（API 兼容模式除外） |
| P3 | 事实驱动 | 每条需求/架构决策引用证据（audit 报告 file:line 或 apifox 文档）；无证据的标【待确认】 |
| P4 | 基础设施守恒 | 重活（编译/后端跑测）放服务器 192.0.2.10，本机仅编辑与轻分析；产物一律先落 `dist/{app,server,docs}/` |
| P5 | 合规共存 | 遵守 Apache-2.0（NOTICE+修改声明）；用户自担 IM 平台风控与账号合规责任；项目明示非腾讯官方 |
| P6 | 安全默认 | 修复上游全部 P0/P1（见 §7）；密钥零硬编码；新 APK 出厂禁用全部统计/崩溃上报 SDK |
| P7 | 涉密资料零上云 | 审计/逆向产物仅存本地 `local-private/`（不入库、不上传 GitHub，2026-11-02 用户指令）；密钥材料仅存 `secrets/`；CI 门禁 G13 强制拦截 |

## 2. 命名、仓库与制品

| 项 | 决策 | 说明 |
|---|---|---|
| 项目名 | **WorkTool-Open** | README 首段声明非腾讯官方、上游致谢 |
| 域名/标识 | 开源版**不含** ymdyes.cn 任何标识 | 仅为记录历史；不复用其视觉/UI |
| 仓库布局 | 单仓库：`android/`、`server/`、`admin/`、`protocol/`、`infra/`、`test/`、`docs/`、`dist/`(ignored) | issue/PR 聚拢，降低共建门槛 |
| 分支协作 | `main` = 集成线（Codex PR 流）；**`zcode/main` = 本助手固定维护线（Go 服务端 + 我方完整规划）**，直接推送、不触碰 main；跨线合并与取长补短由用户以 PR 裁决 | 双线并行、互不否决、各自评估（2026-11-02） |
| 发布平台 | **GitHub Releases 优先** | 构建物 APK+server 镜像+compose+校验和；Gitee 镜像可选（带宽） |
| 本机构建 | 不设为必需 | 无本地 Android SDK 属常态；有则支持 |
| CI | GitHub Actions 双工作流：`android.yml`（JDK17+Gradle 构建/单测）+ `server.yml`（Go vet+单测；golangci-lint/镜像构建待 M2） | CI 即"无 SDK 机器"的构建路径验证 |
| 服务目录（S1） | `/srv/worktool-open/{builder,server,artifacts,scripts,logs}` | 见 §5 基础设施；服务器上容器化，本机零重依赖 |

## 3. 基础设施（应对本机资源限制）

| 资源 | 用途 | 位置 | 排期 |
|---|---|---|---|
| 本机（Windows） | 编辑、轻量分析、gh/git 客户端 | 本项目目录 | 全程 |
| **192.0.2.10** | ① 构建服务器：Docker in Docker 或 `adb:latest` 容器挂载宿主 docker.sock 构建 Android；② 后端开发/联调/staging；③ 容器注册表（registry:2）分发镜像到 D1 | `/srv/worktool-open/` | M1 起用 |
| **192.0.2.20** | 唯一 ground truth 测试真机（经 RIG 通道安装/日志） | 内网 | M2 起用 |

分工细节：
- **构建容器**：基于 `cimg/android` 或自建镜像（JDK17 + Android SDK/cmdline-tools + Gradle 8.x），源码以宿主目录挂载，产物写 `dist/app/`。容器内跑 `gradle :app:assembleDebug`。无 GUI 需求。
- **后端**：Go 静态编译，容器极轻；staging 实例 + 可选 Postgres（默认 SQLite 文件）。
- **本机不安装** Android SDK/模拟器镜像；本机角色仅客户端（git/gh/ssh/scp）。
- 若 S1 不可用时的降级：Android 构建退回 GitHub Actions（已具备）；后端开发退回本机轻量 Go+Docker（Go 工具链极小，属可接受例外）。

## 4. 里程碑

| 阶段 | 内容 | DoD（完成即有证据） | 目标周 |
|---|---|---|---|
| M0 审计与规划 | 三路静态分析、协议还原、规划 v2、独立复核 | audit/01~03 + 本文档 + AUD-01 通过 | **本周（已完成）** |
| M1 仓库与构建链 | monorepo 骨架、CI 双流水线、S1 构建容器就绪、`hello` APK 端到端出包 | CI 或 S1 任一路径产出 APK+sha256（S1 为优先路径而非必要条件）；Actions 绿徽章 | W47 |
| M2 通讯层与端侧核心 | 协议库（信封/加密可关/心跳/重连/AtLeastOnce）+ 无障碍核心骨架（服务声明、节点树调试页、5 个原子操作） | 真机 D1：APP 连上自建 server，下发 5 指令成功执行；协议单测 ≥80% 分支 | W48-49 |
| M3 业务功能与调度 | F1~F20 中的 P0 项（FR-01/02/09/10/15）+ server 调度（REST 兼容 + 队列/幂等/重试 + 回调分发 FR-35） + cron 定时 | 兼容模式：用 apifox 文档报文对 M3 server 冒烟通过 | W50-51 |
| M4 管理界面与发布 | admin Web（React/TS）+ docker-compose + TLS/鉴权 + **Release v0.1.0**（APK/镜像/文档） | compose 一键起；Release 附件含 checksums；协议文档公开 | W52 |
| M5 强化与动态验证 | 动态 runbook 执行（audit/03）、隐私矩阵、渗透自查、多企微版本适配 | 端点白名单测试 100% 通过；高危问题清零；隐私报告发布 | W53+ |

## 5. 服务器 S1 使用规范

- 目录约定：`/srv/worktool-open/builder`（构建容器与缓存）、`/server`（staging）、`/artifacts`（APK/镜像 tar 与 sha256）、`/scripts`（运维脚本）、`/logs`。
- 一切服务容器化（docker compose 管理），本机零重依赖；所有脚本入库 `infra/` 目录，服务器只执行入库脚本（可审计、可复现）。
- 镜像分发：`S1:5000`（registry:2，内网）→ `D1` 拉取（如需在设备侧旁路加载组件，另行评估）。
- 前置条件状态（2026-11-02 已核验）：SSH `friend@192.0.2.10` 免密可达（Ubuntu, kernel 7.0, Docker 29.6.2）；待确认：磁盘配额、compose 版本、是否可开 S1:5000 内网 registry。

## 6. 关键技术决策记录（ADR 摘要）

| ID | 决策 | 理由 |
|---|---|---|
| ADR-01 | 端侧 Kotlin + 无障碍服务（对齐上游范式），不用 Xposed/Root | 免 root、兼容性广、不修改/不注入目标应用（实现边界窄于 Hook 路线）；平台条款合规由用户自担（R2），"无障碍"≠合规 |
| ADR-02 | 后端 Go（单二进制，SQLite 默认 / Postgres 可选）—— **维持有效**（用户 2026-11-02 澄清：我方继续用 Go，不受 Codex 线影响） | 资源占用小、内网单机友好、无 JVM 常驻 |
| ADR-03 | 协议信封向后兼容（socketType/messageId/apiSend…），**加密默认关闭**且 key 可配 | 兼容存量生态（apifox 文档生态按此调用）；修复上游"硬编码 key"问题 |
| ADR-04 | 新版包名 `cn.worktool.open`，正式签名密钥库由用户生成保管 | 上游包名 `com.tomgs.worktool` 保留给上游；密钥不外泄 |
| ADR-05 | 移除全部 Umeng/TalkingData/WWAPI 专有 SDK | P0 隐私（AUD-01 端点 13-17 遥测 + 18-20 企微 SDK）；OCR 开源替代评估见 R5 |
| ADR-06 | 管理端 admin Web（React+TS），compose 交付 | 用户自托管友好 |
| ADR-07 | **双线并行、各自保持技术栈**：我方 Go / Codex 线 Node+Fastify；**取长补短、不合流吞并**——跨线复用只以"设计/schema 借鉴"方式进行，合流由用户以 PR 裁决 | 用户 2026-11-02："继续使用 go，不要被 codex 影响""取长补短，不是全面否定自己的"；对照与吸收清单见 docs/06 |
| ADR-08 | 我方协议权威 = `protocol/`（v1 兼容信封 + v2 安全增量，02 §5 精确规格）；**吸收** contracts/ 线规格成果（状态机边表/幂等键窗口/墓碑/execution_unknown 对账/RBAC 分权/签名回调）作为设计输入 | 取长补短：借设计、不换栈；两线映射见 docs/07 |

## 7. 风险登记（全表）

| ID | 风险 | 等级 | 缓解 |
|---|---|---|---|
| R1 | 企微新版 UI 变化导致无障碍失效 | 高 | 版本适配矩阵 + 节点选择器配置化 + 回归真机 D1 |
| R2 | 账号风控/封禁 | 高 | 文档明示；速率限制默认值；用户自担 |
| R3 | 服务端指令注入/越权 | 中 | 鉴权+输入校验+审计日志；M5 渗透自查 |
| R4 | 上游 Apache-2.0 义务与商标（项目名含 worktool 字样） | 中 | NOTICE+修改声明；命名按"描述性使用+显著致谢"论证并列入法务复核，不通过则更名（04 §5 门禁项） |
| R5 | OCR 兜底开源替代不确定（上游为闭源 ML Kit 管线） | 中 | P2 前完成候选评估（PaddleOCR-mobile/MNN-OCR 等：中文精度/模型体积/离线性/维护度）；未决前 FR-45 不排期 |
| R6 | FR-10/13 小程序/链接推送原依赖企微 WWAPI SDK（剥离后无现成路径） | 中 | 链接有 UI fallback（AUD-02）；小程序评估纯 UI 自动化替代或用户自接企微开放平台；v0.1 明示支持边界 |
| R7 | 个人微信自动化（FR-56）平台风控显著高于企微，封号风险高 | 高 | 默认关闭+显式开启含风险确认流；README 合规节明示；用户自担（同 R2）；矩阵微信列独立取证不与企微混标 |

## 8. 治理与共建

- 分支：`main` 受保护，feature → PR（模板含"需求ID/证据/测试"三必填）→ 至少一人复核 → squash 合并。
- ISSUE 模板：bug / feature（须关联 REQ 编号）/ 安全（私密通道）。CODEOWNERS 按目录划分。
- 语义化版本 + CHANGELOG；发布走 GitHub Release 并附 `checksums.txt`。

## 9. 用户提供项状态（更新 2026-11-02）

1. GitHub 仓库 —— **已提供** `ZhangIvan/worktool-open`；本助手固定维护线 `zcode/main`（不触碰 main / Codex PR 流）。
2. S1 SSH —— **已打通**（免密；Docker 29.6.2）；磁盘配额/registry 待确认。
3. D1 RIG —— **65000 已开放**；凭据/证书待首次连接确认。
4. 签名 keystore —— **已生成并文档化**：`secrets/worktool-open.jks`（不入库），维护说明 `docs/ops/SIGNING.md`。
5. 企微基线 —— **已确认**：兼容 4.1.8~4.1.39 与 5.0.0~5.0.10；开发/验证默认 5.0.x 最新稳定（矩阵见 docs/05）。
6. 敏感资料 —— **政策已生效**：原 `docs/audit/` 已迁至 `local-private/audit`（本地保留、永不入库）；后续涉密文件一律入 `local-private/`，CI G13 拦截。
