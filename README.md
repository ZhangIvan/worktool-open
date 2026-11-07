# WorkTool Open — 合成本地原型

**当前可运行：自有后台 + 控制台 + 独立模拟设备。不是 Android/企微生产系统。**
仅 `message.send_text`、`file.export` 两项合成动作；67 项需求目录完整保留，其余 `planned_unavailable`。所有任务结果来自真实 HTTP/WS 与 SQLite 状态，不是静态页面假成功。

## 先看边界
- 仅监听 `127.0.0.1`；默认无遥测、外部回调或真实业务外联。
- 不连接真实账号，不运行原 APK，不读取任意文件，不提供 shell/动态代码执行。
- 合成正文 SQLite 明文；逻辑删除不等于擦除 WAL/备份/离线设备。不要输入个人信息或生产凭据。
- 命令创建严格支持幂等键；当前导出申请POST尚未消费幂等键，网络未确认后重试可能产生多个pending申请（不会自动执行），应先刷新核对。不要把所有POST都当作幂等。
- 导出需要不同主体批准、绑定申请人/设备/范围，以及模拟设备的本地确认。批准不等于已经导出；同一主体/设备/范围内有效批准目前可用于不同命令意图，只有同幂等键防重；本期未实现一次性download-grant或真实文件下载 UI。
- 浏览器视觉/交互检查在本次 cloud 因 Chromium socket 权限受阻，**BLOCKED/NOT_RUN，无截图**。构建、fetch 层与 HTTP 代理检查不替代浏览器验证。
- 本分支为合成原型草稿；发布与合并状态以 GitHub 分支及 PR 记录为准，本地验证不代表生产准入。

## 环境与安装
Node **24.22.1**；不需要 Docker、Android SDK 或外部数据库。
```
npm ci --ignore-scripts --cache .npm-cache
npm run verify
```
完整校验前先停止正在运行的 dev server/console，HTTP 代理检查需使用本机3100/5173端口。
依赖精确版本与 integrity 在 `package-lock.json`。不要运行未审查的生命周期脚本；无需全局安装。

## 交互运行
终端 A（首次必须是受控交互终端）：
```
npm run server
```
首次生成 `tenant_local` 的 admin/operator/approver/auditor 独立随机密码，仅本地一次显示。不要复制到日志、issue 或源码。数据库在 `server/data/`（git 忽略）。终端 B：
```
npm run console
```
访问 `http://127.0.0.1:5173/`。admin 在模拟设备页创建一次性配对材料；模拟器通过 `simulator/README.md` 的程序接口完成配对/连接。operator 发文本任务；approver 是独立身份，不能用同一人自批导出。

### 快速合成演示（已配对模拟设备）
停掉上述 server 后，在终端 A 运行：
```
node qa/demo-server.ts
```
此工具在 `.runtime/` 建立独立测试数据库、已配对合成设备及4个随机账号，不打印凭据。账号信息仅在权限600的 `.runtime/ui-credentials.json`，可在自己的受控终端私下读取；**不得上传该文件**。终端 B 仍用 `npm run console`。工作区 `tenant_demo`，账号 `operator` 可发合成文本。演示不默认批准本地文件导出；导出完整路径由下面的可复现实测覆盖，或按模拟器说明显式配置预批准合成对象及本地同意。关闭按 Ctrl-C。

## 可复现实测
```
npm run typecheck
npm test
npm run build
npm run test:http-console
```
`npm test` 包括实际 SQLite/身份/CSRF、真实 HTTP/WS 合成执行、重启、未知结果不重放、主体/设备替换审批负例、控制台 fetch 单测。`test:http-console` 在同一进程环境启动后台与 Vite 代理，验证真实登录/会话/退出；仍不是浏览器渲染测试。

浏览器复现命令（需允许本地 Chromium sockets 的正常开发/CI环境）：先运行上述 demo 和 console，再执行 `python3 qa/browser_check.py`。本次受限环境该步骤未通过；不通过解除限制或访问受限路径尝试绕过。Python Playwright/Chromium仅是可选QA工具，不是服务运行依赖。

## 目录与接口
- `contracts/`：冻结的新协议 schema、合法状态事件、合成例子、67需求摘要。
- `server/`：Fastify/SQLite/scrypt、单角色/租户、审批、持久状态与审计。
- `simulator/`：持久去重、合成副作用、断线结果恢复、本地停止与确认。
- `console/`：React/TypeScript、API实际响应、错误/空状态/不可用能力。
- `tests/`、`qa/`、`artifacts/`：回归代码、HTTP检查、实际结果与阻断记录。
- `docs/design-baseline.json`：清洁设计输入hash；`docs/ADR-002-export-scope.md`：已发现批准复用缺口的修正依据。

### 状态的关键区别
transport_ack不等于accepted；accepted不等于succeeded。取消是请求；已产生副作用不能撤回。掉结果/崩溃而不能确定效果时进入execution_unknown，不能盲重试。服务器与模拟设备都持久去重；不宣称真实Android UI效果“恰好一次”。

## 协作与许可
新实现MIT；依赖保留各自许可，见 `docs/dependency-inventory.json` 和 `LICENSE`。本仓库不含原 APK、反编译树、PRIVATE清单或原始凭据。提交 issue 只用合成复现数据；私密漏洞不要放公开 issue（见 `SECURITY.md`）。功能或协议变更须补 schema/负测/迁移说明，不能通过隐藏按钮替代后端授权。真实Android、生产加密/备份/更新/回调、更多能力都需要后续明确验收，不冒充已经完成。

## Repository initialization

原仓库初始化内容（保留原文）：

```text
# worktool-open
```
