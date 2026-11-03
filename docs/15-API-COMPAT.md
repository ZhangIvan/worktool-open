# apifox 兼容适配层任务卡（T3.6）v1.0

- 版本：v1.0（2026-11-03；源=P-API 字段级比对报告 local-private/parity/apifox-field-diff.md，差异 36 项/兼容破坏 6）
- 目标：**apifox 文档调用方零改造迁移**——文档原样请求在我方 server 上得到文档所述响应

## 破坏级 6 项（全部必修）
| # | 差异 | 适配实现 |
|---|------|----------|
| K1 | 端点形态：文档 `POST /wework/sendRawMessage?robotId=` + `{socketType:2,list:[≤100]}` 信封 vs 我方单条制 | 新增薄路由 `/wework/sendRawMessage`：拆 list→逐条转统一管线（复用既有 Submit），响应聚合 |
| K2 | 响应形态：文档 data=string messageId vs 我方 object{commandId} | 兼容响应双字段：`data`（string messageId=commandId）+ `detail`（object 完整信息，文档不冲突） |
| K3 | 回调 body：ExecCallbackBean 需 messageId/type(int)/rawMsg/runTime/groupName/qrCode vs 我方 commandId/type="exec_result" 字符串 | outbox 分发 body 按文档双名补齐（alias 同值），type 改 int 族 |
| K4 | 文本消息回调（type=11 spoken/rawSpoken）未接线 | 消息事件入 outbox 分发面（新增事件族） |
| K5 | 回调响应 `type:5000` 同步代回复未解析 | 回调 HTTP 客户端解析 5000→代回复指令入队（同步语义 3s 超时放弃/单次不重试——docs/10 §C） |
| K6 | 限流语义：文档 per-IP 20QPS+封禁 vs 我方 per-robot 60QPM | 适配层加 per-IP 层（保留 per-robot），封禁逻辑=连续超限 60s 冷却 |

## 字段缺失 13 项（B2/B3 批实现时逐条带）
robotInfo/get 9 字段、sort 参数全组、必填预校验 101011 前置、批量 list 端点等——已并入各执行器契约（b2/b3 包引用本文件）。

## 语义一致确认（无需动作）
查询组 /history/results 11 字段+分页信封与文档逐一一致 ✓

## 排期
并入 **W-M5R 后的下一棒（T3.6 卡，W-M6 前）**：K1~K6 一个 Agent 实现（server/internal/api/compat/ 新薄层，不改动既有 /openapi/v1 面——双面并存），字段缺失 13 项随 B2/B3 执行器批次带。验收=apifox 文档原样报文 30 端点冒烟 100%。
