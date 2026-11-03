# WorkTool 合成控制台

React + TypeScript + Vite；所有业务数据通过同源 `/api/v1` 读取，不用静态假数据冒充任务完成。只监听 `127.0.0.1:5173`，开发代理到 `127.0.0.1:3100`，不提供公开部署入口。

从原型根目录运行已安装的 `npm run console`；生产构建用根目录 `npm run build`。不在本目录另行安装依赖。

## 已实现的 API 调用页面

登录与退出、身份概览、模拟设备列表/管理员配对/撤销、operator 两能力任务创建/列表/详情/取消请求、operator 导出申请、独立 approver 批准/拒绝、admin/auditor 去敏审计、67 项能力目录。目录来自根 `contracts/capabilities.json`；仅 `message.send_text` 和 `file.export` 在合成切片中可用。

所有写请求带会话 cookie 与 CSRF；业务意图带幂等键。任务创建响应不确认时保留原请求和键，不能悄悄生成第二任务。批准仅覆盖申请主体、设备和确切 scope；创建 file.export 的设备必须匹配 scope.deviceId；owner/admin 无批准入口。file.export 强制本地确认字段。密码和会话不写 localStorage；配对材料只留当前页面内存，到期或关闭清除。

未接通的成员管理、多角色、高危治理、真实企微/Android、批量、回调、生产更新及清理控制台在后续页明确标 `planned_unavailable`，没有模拟成功按钮。没有实现下载 UI，不暗示批准就已下载。

## 验证边界

`api.test.ts` 是 fetch 层单元检查，不是后台或浏览器验收。TypeScript 和生产 bundle 可独立检查；浏览器启动当前被执行环境阻断，实际 UI 登录、配对、状态、审批及 Back/权限变化尚未得到浏览器验证，需后续集成测试记录。不能据构建通过宣称设计中的 99 项均已通过。

运行浅层测试：在根目录执行 `node --test console/api.test.ts`（Node 24）。运行前端类型检查：`node_modules/.bin/tsc --noEmit -p console/tsconfig.json`。

## 权限漂移修订

`src/permissions.ts` 统一导航、直接路由读取与操作按钮。owner 首批仅概览和目录，不继承 admin 的配对/撤销/删除/审计/取消；admin 无导出审批或业务命令创建。operator 的任务列表、详情和取消按钮要求服务端 `requestedBy` 与当前 principalId 一致，缺失或不匹配时拒绝显示。此为 UI 防御性检查，后端仍须独立校验 tenant、身份、角色与对象范围。

## 可选 Go 只读迁移模式

默认 `npm run console` / `npm run build` 仍使用原 Node 控制台及 3100 代理，不改变既有流程。
明确选择 `npm run console:go` 时，Vite 仍固定在 `http://127.0.0.1:5173`，仅将 `/api` 代理至本机 Go 服务 `127.0.0.1:3101`，保留浏览器 Host（`changeOrigin: false`），不代理 WebSocket。Go 服务必须另行启动；前端不会自动启动服务、配对设备或部署。

Go 模式只调用 `/api/v1/auth/me`、`/api/v1/auth/login`、`/api/v1/auth/logout` 和只读 `/api/v1/devices`。登录后进入 `#/devices`；仅 admin/operator 可以读取，owner 不继承读取权限。未知或未迁移路由明确显示不可用，不载入原 Node 页面。后端鉴权仍是权限边界。命令、导出、审计、配对、撤销等管理写入均未迁移；不显示假零值或假成功。空列表只表示 Go API 返回了 `items: []`，不代表存在真实设备、完成业务或已部署。

`npm run build:go` 是该模式的本地前端构建检查，不是部署命令；默认构建写入 `console/dist`，Go 构建独立写入 `console/dist-go`，不会相互覆盖。`node --test console/*.test.ts` 包含代理选择、精确角色/路由限制及接口范围回归。这些是单元和源码防回归检查，不替代浏览器端到端登录/退出或 Go 服务集成验收。

Go 会话在窗口 focus 与每 15 秒重新校验；刷新身份后重新读取设备。角色降级、401、已知会话到期或校验失败会移除受保护数据。卸载和较新登录/退出会取消旧请求，旧响应不能恢复旧身份。
