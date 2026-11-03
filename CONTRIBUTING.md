# 贡献指南（Contributing）

- 版本：v1.0（2026-11-03，W-M4）

欢迎 issue / PR。本仓库是审计驱动的工程仓库（计划 → 实现 → 独立评审 → 审计），贡献按同一规程走。

## 开始之前

- 读 [README](README.md) 与 [docs/00 总体计划](docs/00-MASTER-PLAN.md) 了解项目形态；
- 能力边界以 [docs/01 需求规格](docs/01-REQUIREMENTS.md)（FR-xx/SR-xx）为准——**每个 issue/PR 关联需求编号**；无对应编号的先开 issue 讨论归属；
- 协议行为以 [protocol/](protocol/) 为唯一权威（spec + JSON Schema + 夹具）；发现实现与规格不一致，**先提规格问题**，不要单侧迁就实现。

## 分支模型

- `main` = 集成线，**一切变更走 PR**；
- 特性分支 `feat/…`、修复分支 `fix/…`，从 `main` 切出；
- 维护者另有固定自动化协作分支（如 `zcode/main`），跨线合并一律走 PR，不直接 push `main`。

## 提交与 PR 要求

1. **质量门**（`docs/04-AUDIT-CHECKLIST.md` §3，评审员逐项核）：
   - 单测：协议相关 ≥80% 分支覆盖；执行器每指令 ≥5 场景；
   - 确定性门禁本地先过：JSON 格式、G12（产品零 hook）、G13（涉密零入库）；
   - go 分量：`cd server && go vet ./... && go test ./...`；
   - android 分量：`cd android && ./gradlew test`（wrapper 8.7 已入库）；
   - E2E（触及协议/服务端时）：`python test/e2e/run_cross_e2e.py`（仓库根执行，退出码 0）。
2. **协议权威件变更**（protocol/**）：属破坏性变更须走规格裁决（先 issue，附三实现一致性影响面：Go/Kotlin/python E2E）；夹具文件名与字段是冻结的测试接口。
3. **依赖新增**：默认拒绝新增第三方依赖（android 运行时白名单见版本目录注释；Go 仅 modernc.org/sqlite + gorilla/websocket）。确有必要：PR 说明不可替代性 + license 相容性 + 更新 [docs/THIRD-PARTY-NOTICES.md](docs/THIRD-PARTY-NOTICES.md)。专有 SDK / 遥测 / 统计库一律不收（G3）。
4. **红线**（PR 即拒）：
   - 任何 hook/注入框架进入产品源集（frida/Xposed/dexclassloader，G12）；
   - 密钥、口令、证书私钥、内部取证材料入库（G13）；
   - 支持矩阵未经真机证据把 ⬜ 标成 ✅/🟡（docs/05 规则）；
   - 复制上游闭源实现代码（本仓库为行为级重写）。

## Commit 规范

`类型(范围): 摘要` —— 类型 `feat/fix/docs/test/refactor/chore/ci`；范围用车道/模块（如 `W-M4(rel)`、`server/scheduler`）。正文写"为什么"，链接关联 issue 与任务卡（docs/03 卡 ID）。

## Issue 模板

bug / feature 模板见 `.github/ISSUE_TEMPLATE/`。bug 请附：版本/commit、复现步骤、日志（**脱敏**：deviceId/token/配对码勿原文粘贴）。

## 行为准则

技术讨论对事不对人；不接受对目标应用（企业微信）的破解、外挂类请求；不接受骚扰/批量营销类功能需求（README 合规与免责）。
