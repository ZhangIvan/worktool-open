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
