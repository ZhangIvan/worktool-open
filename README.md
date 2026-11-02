# WorkTool-Open

> 开源的企业微信（WeCom）自动化机器人：**端侧无障碍执行 + 自托管调度后台**，数据默认不出你的内网。
>
> 本项目衍生自开源项目 [gallonyin/worktool](https://github.com/gallonyin/worktool)（Apache-2.0，仓库止于 v2.8.1 学习版），向原作者致谢。**非腾讯官方项目，与上游作者无关联。**

## 为什么有这个项目

上游开源版停更于 2023 年，其后的闭源版存在数据外送与可中间人攻击等缺陷（**内部审计资料由维护者本地保存，不入公开仓库**）。WorkTool-Open 以行为级重写方式重建全部能力，并做到：

- **数据主权**：默认零遥测、零云依赖；服务端你自己部署，机器人可完全离网运行（本地模式）
- **安全默认**：TLS 强制校验、无硬编码密钥、出网白名单、更新带签名校验（`docs/01` §2.1 SR-01~11）
- **开放**：协议、配置、部署全部开源（Apache-2.0），欢迎 issue / PR 共建

## 仓库结构

| 目录 | 内容 | 状态 |
|------|------|------|
| `android/` | Android 端（Kotlin，`cn.worktool.open`） | M1 待启动 |
| `server/` | 调度后台（Go 单二进制，SQLite/PG） | M1 待启动 |
| `admin/` | 管理 Web（React + TS） | M4 |
| `protocol/` | 通讯协议规格 + JSON Schema + 测试夹具 | M2 |
| `infra/` | 构建容器 / compose 部署 | M1 |
| `test/` | 真机验证与动态分析用例 | M2+ |
| `docs/` | 计划与规格（00~07）、签名维护说明 | ✅ v2.2 |

## 快速开始

项目处于 **M0→M1 阶段**（规划与审计已完成，实现启动中）。里程碑与任务卡见 [docs/00-MASTER-PLAN.md](docs/00-MASTER-PLAN.md) 与 [docs/03-EXECUTION-PLAN.md](docs/03-EXECUTION-PLAN.md)。

首个可用版本（v0.1.0）目标：App 连接自建 server，收发消息 / 建群 / 加好友 / 定时任务 / 关键词回复可用，apifox 兼容 REST 冒烟 100% 通过。

## 文档

- [00 总体计划](docs/00-MASTER-PLAN.md) · [01 需求规格](docs/01-REQUIREMENTS.md) · [02 系统架构](docs/02-ARCHITECTURE.md) · [03 执行调度](docs/03-EXECUTION-PLAN.md) · [04 审计规程](docs/04-AUDIT-CHECKLIST.md) · [05 支持矩阵](docs/05-SUPPORT-MATRIX.md) · [06 服务端技术决策](docs/06-SERVER-TECH-DECISION.md) · [07 既有代码映射](docs/07-EXISTING-CODE-MAPPING.md)

## 贡献

- Issue 请关联需求编号（FR-xx / SR-xx，见 `docs/01`）；PR 需通过 `docs/04` 质量门与 CI 门禁（G1~G13）
- 分支模型：`main` = 集成线（PR 流）；维护者另有固定维护分支（如 `zcode/main`），跨线合并一律走 PR，避免自动化协作互相冲突

## 合规与免责

- 本工具基于 Android 官方无障碍 API 实现自动化（技术路线开放、可审计，不修改/不注入/不破解目标应用）。**但"使用无障碍 API"是技术选择，不等于平台合规**：使用者须自行评估并遵守企业微信服务条款与所在地法律，账号风控/封禁风险由使用者自担（风险登记 R2）
- **产品实现零 hook**：产品代码不含 frida/Xposed/注入等动态篡改；逆向分析工具仅用于维护者内部的测试设备验证（与产品实现严格分离，门禁 G12）
- 禁止将本项目用于骚扰、欺诈、批量营销等任何侵害他人的用途

## License

Apache-2.0（含上游衍生义务，见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)）。第三方声明见 [docs/THIRD-PARTY-NOTICES.md](docs/THIRD-PARTY-NOTICES.md)。
