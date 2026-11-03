/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.task.ActionIntent
import cn.worktool.open.core.task.DryRunRunner
import cn.worktool.open.core.task.RiskLevel
import cn.worktool.open.core.task.RiskLevels
import cn.worktool.open.core.task.Source

/**
 * 指令目录（产品冲刺 A 车道）：docs/05 §「P0 首批指令（v0.1.0）」行的端侧只读镜像。
 *
 * 口径（如实登记）：
 * - 列出集 = docs/05 P0 行的全部指令号（101/102/203/206/207/225/212/501/502/509）；
 * - 风险级 = core:task-engine [RiskLevels]（FR-53 类型级兜底表，与服务端 guard.go
 *   两级命名统一）——**不在此另立分级**；101/102 为端→云上报类（无执行语义），
 *   不参与风险分级（risk=null，UI 显示「上报」）；
 * - 参数说明 = feature:executor [PayloadFields] 实际消费的候选字段（spec §4.4
 *   候选名，冻结前口径），非杜撰；
 * - 「上次执行状态」由 SessionRuntime 从执行回执回调回填（无回执 = 未执行），
 *   本类只提供渲染归一 [lastStatusText]。
 */
object CommandCatalog {

    /** 一条目录行（对应指令列表页一行；101/102 等合并行按 docs/05 行式）。 */
    data class Row(
        /** 展示名（docs/05「能力」列）。 */
        val title: String,
        /** 指令号（多号合并行取主号）。 */
        val commandType: Int,
        /** 合并行全部指令号（如 101/102、501/502）。 */
        val allTypes: List<Int>,
        /** null = 上报类（无执行风险分级）。 */
        val risk: RiskLevel?,
        /** 参数说明（PayloadFields 候选字段口径）。 */
        val paramsNote: String,
        /** 上次执行回执：null = 未执行过。 */
        val lastOutcome: Outcome?,
    ) {
        data class Outcome(val success: Boolean, val errorCode: Int, val atMillis: Long)
    }

    /** P0 目录（docs/05 §P0 全行；顺序按 docs/05 表序）。 */
    fun p0(lastOutcomeByType: Map<Int, Row.Outcome> = emptyMap()): List<Row> = listOf(
        row(
            title = "新消息读取上报",
            primary = MessageTypes.TYPE_RECEIVE_MESSAGE_LIST,
            all = listOf(MessageTypes.TYPE_RECEIVE_MESSAGE_LIST, MessageTypes.TYPE_CONSOLE_TOAST),
            risk = null,
            note = "端→云自动上报（101 消息列表 / 102 控制台提示），无参数、不产生副作用",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "发送消息（含@）",
            primary = MessageTypes.SEND_MESSAGE,
            all = listOf(MessageTypes.SEND_MESSAGE),
            risk = RiskLevels.of(MessageTypes.SEND_MESSAGE),
            note = "targetRoom（目标会话名，必填）· receivedContent（正文，必填）· atList（@名单，可空）",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "创建外部群",
            primary = MessageTypes.INIT_GROUP,
            all = listOf(MessageTypes.INIT_GROUP),
            risk = RiskLevels.of(MessageTypes.INIT_GROUP),
            note = "groupName（新群名，必填）· groupMembers（拉人名单，可空）· announcement（群公告，可空）",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "修改群信息",
            primary = MessageTypes.UPDATE_GROUP,
            all = listOf(MessageTypes.UPDATE_GROUP),
            risk = RiskLevels.of(MessageTypes.UPDATE_GROUP),
            note = "targetRoom（目标群名）· announcement（公告）· groupRemark（备注）· addMembers（拉人名单；拉人=L1）",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "修改群成员（拉踢/备注）",
            primary = MessageTypes.MODIFY_GROUP_MEMBER_INFO,
            all = listOf(MessageTypes.MODIFY_GROUP_MEMBER_INFO),
            risk = RiskLevels.of(MessageTypes.MODIFY_GROUP_MEMBER_INFO),
            note = "targetRoom（目标群名，必填）· memberName（成员名，必填）· markName（备注）· tagList（标签）",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "新好友自动通过",
            primary = MessageTypes.PASS_ALL_FRIEND_REQUEST,
            all = listOf(MessageTypes.PASS_ALL_FRIEND_REQUEST),
            risk = RiskLevels.of(MessageTypes.PASS_ALL_FRIEND_REQUEST),
            note = "端侧本地规则动作（REST 不收，docs/05 AUD-WM3-09）；服务端下发的 212 同样由端侧全量通过",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "群/好友列表查询",
            primary = MessageTypes.GET_GROUP_INFO,
            all = listOf(MessageTypes.GET_GROUP_INFO, MessageTypes.GET_FRIEND_INFO),
            risk = RiskLevels.of(MessageTypes.GET_GROUP_INFO),
            note = "只读查询（501 群列表 / 502 好友列表，分页上限 50 页）；结果经上报通道回传，不产生副作用",
            lastOutcomeByType = lastOutcomeByType,
        ),
        row(
            title = "上传本地文件/日志",
            primary = MessageTypes.GET_LOCAL_FILE,
            all = listOf(MessageTypes.GET_LOCAL_FILE),
            risk = RiskLevels.of(MessageTypes.GET_LOCAL_FILE),
            note = "filePath（目标文件路径；缺省 = 当前日志文件）——语义为上传指定文件（docs/05 F30）",
            lastOutcomeByType = lastOutcomeByType,
        ),
    )

    /** 目录行风险标签（UI 渲染；null=上报类）。 */
    fun riskLabel(risk: RiskLevel?): String = when (risk) {
        null -> "上报"
        RiskLevel.L0 -> "L0"
        RiskLevel.L1 -> "L1"
        RiskLevel.L2 -> "L2"
    }

    /** 上次执行状态渲染：未执行 / 成功 / 失败(错误码)。 */
    fun lastStatusText(outcome: Row.Outcome?): String = when {
        outcome == null -> "未执行"
        outcome.success -> "成功"
        else -> "失败(code=${outcome.errorCode})"
    }

    /**
     * 干跑预览（详情页「干跑」按钮）：task-engine [DryRunRunner]（FR-54 同引擎同路径，
     * 仅 policy 分叉）产出「将要做什么」报告，零副作用。注：服务端控制台的干跑
     * （docs/05 203/206/207/501/502/509 干跑取证 = 服务端代行）与端侧本地预览共用
     * 同一风险级口径 [RiskLevels]。
     */
    fun dryRunPreview(row: Row, clock: () -> Long): DryRunRunner.DryRunReport =
        DryRunRunner(clock).report(
            ActionIntent(
                commandType = row.commandType,
                source = Source.USER_TAKEOVER, // 用户在 App 详情页手动发起的预览
            ),
        )

    private fun row(
        title: String,
        primary: Int,
        all: List<Int>,
        risk: RiskLevel?,
        note: String,
        lastOutcomeByType: Map<Int, Row.Outcome>,
    ) = Row(title, primary, all, risk, note, lastOutcomeByType[primary])
}
