# 基础CI：合成本地原型

`.github/workflows/ci.yml`仅运行可复现的基础检查，不发布、不部署、不连接真实企业账号或业务端点。

## 触发与权限
- PR目标为`main`时执行，包括当前draft PR；GitHub的`pull_request.branches`筛选目标分支，不是来源分支。
- push到`worktool/synthetic-prototype`时执行。若同一提交同时对应push与PR，两种上下文都可能检查；每个上下文的新运行取消旧运行。
- GitHub托管`ubuntu-24.04`，不使用自托管runner；单job最多10分钟。
- 仅`contents: read`，checkout不持久化凭据；不注入仓库secrets、写权限、OIDC、发布token或部署环境。
- 使用普通`pull_request`，不用`pull_request_target`或不受控事件文本拼接shell。外部分支仍遵循GitHub默认审批规则，不修改仓库保护设置。

## 固定依赖来源
Node固定`24.22.1`，npm按已提交lockfile安装，并禁生命周期脚本。两项action均来自官方`actions`组织，固定完整commit SHA而非可漂移标签，解析日期2026-11-02：

|Action|固定SHA|官方核验入口|
|---|---|---|
|checkout|`d23441a48e516b6c34aea4fa41551a30e30af803`|[固定action.yml](https://github.com/actions/checkout/blob/d23441a48e516b6c34aea4fa41551a30e30af803/action.yml)|
|setup-node|`249970729cb0ef3589644e2896645e5dc5ba9c38`|[固定action.yml](https://github.com/actions/setup-node/blob/249970729cb0ef3589644e2896645e5dc5ba9c38/action.yml)|

安装阶段需读取GitHub官方actions/Node分发及npm注册表，不是零网络执行；测试仅使用合成fixture与loopback服务。禁用自动包管理器缓存，不上传构建物、数据库、日志包或运行凭据。

## 执行顺序
```
npm ci --ignore-scripts --no-audit --no-fund
npm run typecheck
npm test
npm run build
npm run test:http-console
```
任一步失败即失败，不设置continue-on-error。HTTP测试由测试脚本启动并关闭受控服务，不运行交互式生产初始化，不输出生成口令。测试数据在工作目录内的临时位置，GitHub托管job结束后环境释放。

## 不能由绿色CI推导的结论
- 不包含浏览器任务。原cloud浏览器视觉/交互仍为`BLOCKED/NOT_RUN`；HTTP代理与fetch单测不是浏览器验收，也不是规避本地限制的浏览器替代环境。
- Android、真实企微、真实文件导出、生产安全与负载验证未覆盖；两项synthetic能力不代表67项已实现。
- 该流程不执行完整依赖安全认证。固定SHA/lockfile与ignore-scripts不能证明依赖没有漏洞。
- 在远端实际产生并完成对应head SHA的运行前，只能称“CI配置已准备/本地命令已验证”，不能称GitHub CI通过。

更改Node、action SHA或命令须另行核验官方来源并经PR评审。工作流写权限不足时应报告授权缺口，不改用宽权限token或其他发布通道绕过。
