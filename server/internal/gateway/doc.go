// Package gateway 实现 WS 网关（docs/02-ARCHITECTURE.md §4；任务卡 T2.3）。
//
// 计划职责：
//   - 会话协商：hello 握手；v1 兼容路径 /webserver/wework/{robotId} 与
//     v2 安全会话 /api/v2/device-sessions（新客户端首选）；
//   - 双向每会话 seq 严格递增 + eventId 去重；心跳 v1 5s / v2 15s，失联 45s 关闭；
//   - 指令下发与结果回收（FR-01/34）。
//
// T1.3 骨架阶段为空实现占位，协议规格以 protocol/（T2.1 交付）为唯一权威。
package gateway
