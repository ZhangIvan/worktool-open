/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 动作执行器端口（T3.3 消费、T3.1 实现）。
 *
 * 绑定契约：feature:* 车道的指令执行器（FR-01/02/09/10/15，docs/03 T3.1）实现本接口并
 * 按 [supportedType] 注册进 [LocalTaskEngine.registerExecutor]；本地规则引擎只产出
 * [ActionIntent]（203 发消息 / 212 通过好友），不直接触达无障碍层。
 * PASS_FRIEND 执行器（FR-15）即 `supportedType = 212` 的实现。
 */
interface ActionExecutor {

    /** 支持的协议指令 type（protocol/spec.md §4.2，如 203 SEND_MESSAGE / 212 PASS_ALL_FRIEND_REQUEST）。 */
    val supportedType: Int

    /**
     * 执行动作意图。实现必须：
     * - 在安全断点检查暂停态（FR-52「进行中动作序列执行至安全断点停止」——经引擎注入的
     *   暂停探针或服务侧自检，T3.1 落地）；
     * - 返回结果回执语义错误码（0 成功 / 10xxx 格式 / 20xxx 执行，spec §4.3）。
     */
    fun execute(intent: ActionIntent): ExecOutcome
}

/** 执行结果（错误码族对齐 spec §4.3）。 */
data class ExecOutcome(
    val success: Boolean,
    val errorCode: Int,
    val errorReason: String? = null,
) {
    companion object {
        val OK = ExecOutcome(success = true, errorCode = 0)
    }
}

/**
 * 本地已执行事件上报端口（重连合并，docs/02 §5.2 / spec §5.4.5）。
 *
 * app 接线层把实现绑定到 `ProtocolClient.V2Session.sendReport`（AtLeastOnce 队列，
 * 先落盘后发送）；载荷由引擎构造（`source=local` + ruleId/eventHash），
 * 服务端按 (ruleId, eventHash) 去重——重复上报天然安全。
 */
interface LocalEventReporter {

    /**
     * 上报已执行的本地事件载荷。
     * @return true = 已提交发送队列（持久化完成）；false = 当前不可发送，待下次重连重试。
     */
    fun report(beans: List<cn.worktool.open.core.protocol.WeworkMessageBean>): Boolean
}
