// Package admin 实现 v0.1 管理端最小面（W-M4 任务卡：Go 直接伺服，零 npm）。
//
// 范围（docs/09 §C「admin 最小面」，四端点 + 审批列表支撑 UI）：
//   - POST /api/v1/admin/pairing-codes  签发配对码（替代 DB 手工引导；spec §5.1.1
//     6 位数字/TTL 10min/单次有效——落 pairing_codes 表，/api/v2/pair 原路径消费）
//   - GET  /api/v1/admin/devices        设备列表 + 在线状态（在线探测经 Deps.Online
//     注入；nil = 未接线恒 false，README 记录接线要求）
//   - GET  /api/v1/admin/commands       指令审计流（含 dryRun 标记，FR-54）
//   - GET  /api/v1/admin/approvals      审批列表（UI 审批按钮的数据面）
//   - POST /api/v1/admin/approvals/{approvalId}/decision  审批决议（L2 approver，
//     复用 scheduler.DecideApproval：独立 approver 自批拒绝/仅 pending 可决议/审计留痕）
//   - GET  /admin/                      单页静态 UI（admin/index.html，原生 JS 零构建链；
//     经 Deps.UIDir 指向仓库 admin/ 目录）
//
// 鉴权：Bearer admin token，复用 api_tokens 表（FR-34 兼容面同表）+ admin scope。
// 表无独立 scope 列（schema 归 store 车道，本车道不越界改库）——以保留主体名
// robot_id='admin' 编码 admin scope：该值非合法 v2 deviceId（pattern ≥8 字符，
// spec §5.1.2），亦不应作为业务 robot 使用（README 告知）。仅存 SHA-256 hash
// （spec §5.1.3 同纪律），原始令牌一次返回。
//
// 边界：RBAC 五角色（docs/02 §4）与 FR-41/42 全量管理面归 W-M5+；本包为 v0.1
// 最小面，protocol/spec.md §1 明确 admin API 不在协议规格 In 范围内。
package admin
