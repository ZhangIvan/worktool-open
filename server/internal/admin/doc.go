// Package admin 实现管理端 API（docs/02-ARCHITECTURE.md §4；任务卡 M4 admin 联动）。
//
// 计划职责：
//   - 机器人 CRUD、在线状态、日志/审计查询、回调配置（FR-40~42）；
//   - RBAC 五角色：owner/admin/operator/approver/auditor
//     （owner 只读不继承业务权；approver 独立且必须当前活跃，docs/06 §3 ④）。
//
// T1.3 骨架阶段为空实现占位。
package admin
