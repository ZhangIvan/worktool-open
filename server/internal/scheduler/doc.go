// Package scheduler 实现定时调度与指令队列（docs/02-ARCHITECTURE.md §4；任务卡 T3.2）。
//
// 计划职责：
//   - cron 定时调度（FR-37）；
//   - 指令队列：幂等键窗口（tenant+actor+operation+key，同键变参 409）、
//     设备墓碑 ≥7 天、execution_unknown 只凭持久证据对账（禁止超时推断成败、
//     非幂等指令不自动重试，docs/02 §5.1 三原则）；
//   - 审批（approver 分权）与熔断（FR-38/52/53）；
//   - 状态机唯一权威 = protocol/state-transitions.csv（表驱动边集，禁止复述边表）。
//
// T1.3 骨架阶段为空实现占位。
package scheduler
