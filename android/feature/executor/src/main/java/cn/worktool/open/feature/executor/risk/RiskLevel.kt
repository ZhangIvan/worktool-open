/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.risk

/**
 * 指令风险级（FR-53 误发保护，docs/01 §1 FR-53 行原文口径）：
 * - [L0] 普通：未列入 L1/L2 清单的常规指令；
 * - [L1] 批量发送、建群、拉人；
 * - [L2] 解散群、删联系人、清队列。
 *
 * P0 子集落位：SEND_TEXT(203)=L0（单发；批量发送 229 不在 P0 子集）；
 * INIT_GROUP(206)=L1（建群）；UPDATE_GROUP(207)=含拉人 L1 / 其余 L0；
 * MODIFY_GROUP_MEMBER_INFO(225)=L0；PASS_FRIEND(212)=L0。
 *
 * 注意：FR-53 分级策略**可配置**，且 L2 需独立 approver 确认（复用审批角色）、
 * 服务端侧另有同风险级 unknown 冻结联动（state-transitions.csv E06/E17）——
 * 本枚举是端侧执行前复检（FR-53 落位「服（端执行前复检）」）的标注载体，
 * 策略裁决主体在服务端调度链路（T3.2），端侧不越权放行 L2 无确认指令
 * （P0 子集不含 L2 指令；后续任务卡扩展时在对应执行器上标注）。
 */
enum class RiskLevel {
    L0,
    L1,
    L2,
}
