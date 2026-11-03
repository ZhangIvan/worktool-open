/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.commands.AddFriendByGroupExecutor
import cn.worktool.open.feature.executor.commands.AddFriendByPhoneExecutor
import cn.worktool.open.feature.executor.commands.AddGroupBoardWebpageExecutor
import cn.worktool.open.feature.executor.commands.AddNeedDealExecutor
import cn.worktool.open.feature.executor.commands.CleanCacheExecutor
import cn.worktool.open.feature.executor.commands.ClickRecommendItemExecutor
import cn.worktool.open.feature.executor.commands.ClockInExecutor
import cn.worktool.open.feature.executor.commands.ConsoleToastExecutor
import cn.worktool.open.feature.executor.commands.CreateCustomerGroupLiveQrCodeExecutor
import cn.worktool.open.feature.executor.commands.DeleteContactExecutor
import cn.worktool.open.feature.executor.commands.DeleteSingleDirectionalContactExecutor
import cn.worktool.open.feature.executor.commands.DismissGroupExecutor
import cn.worktool.open.feature.executor.commands.ExitGroupExecutor
import cn.worktool.open.feature.executor.commands.GetAllFriendInfoExecutor
import cn.worktool.open.feature.executor.commands.GetAllGroupInfoExecutor
import cn.worktool.open.feature.executor.commands.GetCorpListExecutor
import cn.worktool.open.feature.executor.commands.GetGroupQrCodeExecutor
import cn.worktool.open.feature.executor.commands.GetMeetingInfoExecutor
import cn.worktool.open.feature.executor.commands.GetGroupMemberInfoExecutor
import cn.worktool.open.feature.executor.commands.GetGroupNameInfoExecutor
import cn.worktool.open.feature.executor.commands.GetDeviceResourceExecutor
import cn.worktool.open.feature.executor.commands.GetLogSnippetExecutor
import cn.worktool.open.feature.executor.commands.GetMyInfoExecutor
import cn.worktool.open.feature.executor.commands.GetRecentListExecutor
import cn.worktool.open.feature.executor.commands.InitGroupExecutor
import cn.worktool.open.feature.executor.commands.ModifyGroupMemberInfoExecutor
import cn.worktool.open.feature.executor.commands.GetFriendListExecutor
import cn.worktool.open.feature.executor.commands.GetGroupListExecutor
import cn.worktool.open.feature.executor.commands.GetLocalFileExecutor
import cn.worktool.open.feature.executor.commands.PassFriendExecutor
import cn.worktool.open.feature.executor.commands.PushFileExecutor
import cn.worktool.open.feature.executor.commands.PushLinkExecutor
import cn.worktool.open.feature.executor.commands.PushMicroDiskFileExecutor
import cn.worktool.open.feature.executor.commands.PushMicroDiskImageExecutor
import cn.worktool.open.feature.executor.commands.PushMicroprogramExecutor
import cn.worktool.open.feature.executor.commands.PushOfficeExecutor
import cn.worktool.open.feature.executor.commands.QrCodeJoinGroupExecutor
import cn.worktool.open.feature.executor.commands.ReceiveLoopSwitchExecutor
import cn.worktool.open.feature.executor.commands.RecallMessageExecutor
import cn.worktool.open.feature.executor.commands.RelayMergeMessageExecutor
import cn.worktool.open.feature.executor.commands.RelayMessageExecutor
import cn.worktool.open.feature.executor.commands.RelayMultiMessageExecutor
import cn.worktool.open.feature.executor.commands.ReplyMessageExecutor
import cn.worktool.open.feature.executor.commands.RobotConfigUpdatedExecutor
import cn.worktool.open.feature.executor.commands.RobotControllerTestExecutor
import cn.worktool.open.feature.executor.commands.RobotErrorLogExecutor
import cn.worktool.open.feature.executor.commands.RobotLogExecutor
import cn.worktool.open.feature.executor.commands.RobotQueueClearExecutor
import cn.worktool.open.feature.executor.commands.RobotQueueGetExecutor
import cn.worktool.open.feature.executor.commands.RobotQueueRemoveExecutor
import cn.worktool.open.feature.executor.commands.ScanQrCodeExecutor
import cn.worktool.open.feature.executor.commands.SendBatchTextExecutor
import cn.worktool.open.feature.executor.commands.SendMergeMessageExecutor
import cn.worktool.open.feature.executor.commands.SendMultiMessageExecutor
import cn.worktool.open.feature.executor.commands.SendTextExecutor
import cn.worktool.open.feature.executor.commands.ShowGroupInfoExecutor
import cn.worktool.open.feature.executor.commands.StopAndGoHomeExecutor
import cn.worktool.open.feature.executor.commands.SwitchCorpExecutor
import cn.worktool.open.feature.executor.commands.TriggerReceiveNewMessageExecutor
import cn.worktool.open.feature.executor.commands.UnimplementedCommandExecutor
import cn.worktool.open.feature.executor.commands.UploadLogFileExecutor
import cn.worktool.open.feature.executor.commands.UpdateGroupExecutor
import cn.worktool.open.feature.executor.intent.IntentNormalizer

/**
 * 执行器注册表：指令号 → 执行器路由（docs/02 §3「feature:* 按 201~234 对应」）。
 *
 * 注册表是 [IntentNormalizer] 支持集的单一事实来源（normalizer 构造时取
 * [supportedTypes]），避免两处清单漂移。
 */
class ExecutorRegistry private constructor(
    private val executors: Map<Int, CommandExecutor>,
) {

    fun executorFor(commandType: Int): CommandExecutor? = executors[commandType]

    fun supports(commandType: Int): Boolean = executors.containsKey(commandType)

    val supportedTypes: Set<Int> get() = executors.keys

    companion object {

        /** 注册执行器集合；指令号重复 = 接线错误，立即失败。 */
        fun of(vararg executors: CommandExecutor): ExecutorRegistry {
            val map = LinkedHashMap<Int, CommandExecutor>()
            for (e in executors) {
                val prev = map.put(e.commandType, e)
                require(prev == null) {
                    "指令号 ${e.commandType} 重复注册（${prev!!::class.simpleName} vs ${e::class.simpleName}）"
                }
            }
            return ExecutorRegistry(map)
        }

        /**
         * T3.1 P0 默认注册表：203/206/207/225/212
         * （docs/03 T3.1 P0 子集 = FR-02/09/10/15）。
         */
        fun defaultP0(): ExecutorRegistry = of(
            SendTextExecutor(),
            InitGroupExecutor(),
            UpdateGroupExecutor(),
            ModifyGroupMemberInfoExecutor(),
            PassFriendExecutor(),
        )

        /**
         * v0.1 首批全量注册表（docs/05 P0 首批行：101/102 上报面 +
         * 203/206/207/225/212 + 501/502/509 查询面）= defaultP0 五指令
         * + 三个查询执行器。
         *
         * 查询执行器经端口外接（[cn.worktool.open.feature.executor.query.QueryResultSink]
         * 结构化结果出口 / [cn.worktool.open.feature.executor.query.LocalFilePort]
         * 文件元数据）——**显式传参**而非默认空实现：生产接线必须提供出口，
         * 缺省静默丢结果属接线错误，类型系统上强制（fail-safe）。
         *
         * 101/102 上报方向不经本注册表（云→端路由无此方向），见
         * `report/MessageEventReporter`（端→云上报执行器）。
         */
        fun defaultFirstBatch(
            querySink: cn.worktool.open.feature.executor.query.QueryResultSink,
            files: cn.worktool.open.feature.executor.query.LocalFilePort,
            /** 真机校准规则注入（2026-11-04 D1 校准：查询执行器共用；null = 占位默认）。 */
            rules: cn.worktool.open.feature.executor.runtime.WeworkScreenRules? = null,
        ): ExecutorRegistry {
            val r = rules ?: cn.worktool.open.feature.executor.runtime.WeworkScreenRules()
            return of(
                SendTextExecutor(),
                InitGroupExecutor(),
                UpdateGroupExecutor(),
                ModifyGroupMemberInfoExecutor(),
                PassFriendExecutor(),
                GetGroupListExecutor(querySink, r),
                GetFriendListExecutor(querySink, r),
                GetLocalFileExecutor(files, querySink),
            )
        }

        /**
         * 对等计划全量批注册表（docs/10 §B 68 条矩阵；B1 补齐进行中——本方法随
         * B1→B4 各批推进逐批扩容，最终覆盖全部**端侧可执行**指令号）。
         *
         * 68 条中两条例外**不经本注册表**（docs/02 §3 分工，非缺漏）：
         * - 011 心跳：协议层通道（core:protocol，M2 已交付），无 UI 执行器语义；
         * - 101 消息上报：端→云方向（云→端路由无此方向），执行器 =
         *   `report/MessageEventReporter`（FR-01），283 为其主动触发面。
         *
         * 端口经 [cn.worktool.open.feature.executor.ports.ExecutorBindings] 显式装配
         * （fail-safe：无默认空实现，缺省静默丢结果属接线错误）。
         */
        fun defaultFullBatch(
            bindings: cn.worktool.open.feature.executor.ports.ExecutorBindings,
        ): ExecutorRegistry = of(
            // 首批 8 条（defaultFirstBatch 全集）
            SendTextExecutor(),
            InitGroupExecutor(),
            UpdateGroupExecutor(),
            ModifyGroupMemberInfoExecutor(),
            PassFriendExecutor(),
            GetGroupListExecutor(bindings.querySink),
            GetFriendListExecutor(bindings.querySink),
            GetLocalFileExecutor(bindings.files, bindings.querySink),
            // B1 补齐（docs/10 §B B1 行）
            StopAndGoHomeExecutor(bindings.session),
            ConsoleToastExecutor(bindings.console),
            ReceiveLoopSwitchExecutor(bindings.loop),
            TriggerReceiveNewMessageExecutor(bindings.report),
            // B2 批（docs/10 §B B2 行：消息·联系人·文件·查询 22 条）
            ReplyMessageExecutor(bindings.batch),
            RelayMessageExecutor(bindings.batch),
            RelayMultiMessageExecutor(bindings.batch),
            RelayMergeMessageExecutor(bindings.batch),
            SendMultiMessageExecutor(bindings.batch),
            SendMergeMessageExecutor(bindings.batch),
            SendBatchTextExecutor(bindings.batch),
            RecallMessageExecutor(bindings.batch),
            AddFriendByPhoneExecutor(),
            AddFriendByGroupExecutor(),
            DeleteContactExecutor(),
            DeleteSingleDirectionalContactExecutor(bindings.batch),
            PushFileExecutor(bindings.remoteFiles, bindings.batch),
            PushLinkExecutor(bindings.remoteFiles, bindings.batch),
            PushMicroDiskImageExecutor(bindings.batch),
            PushMicroDiskFileExecutor(bindings.batch),
            GetMyInfoExecutor(bindings.structured),
            GetRecentListExecutor(bindings.structured),
            GetCorpListExecutor(bindings.structured),
            GetGroupQrCodeExecutor(bindings.structured),
            GetAllFriendInfoExecutor(bindings.structured),
            GetAllGroupInfoExecutor(bindings.structured),
            // B3 批（docs/10 §B B3 行：群运营·效率·运维 30 条）
            DismissGroupExecutor(),
            ShowGroupInfoExecutor(bindings.structured),
            CreateCustomerGroupLiveQrCodeExecutor(bindings.structured),
            AddGroupBoardWebpageExecutor(),
            ExitGroupExecutor(),
            CleanCacheExecutor(bindings.cacheCleanGuard),
            AddNeedDealExecutor(bindings.batch),
            ClockInExecutor(),
            SwitchCorpExecutor(bindings.corp),
            ScanQrCodeExecutor(),
            UnimplementedCommandExecutor(MessageTypes.INIT_ANTI_HARASSMENT_RULE),
            UnimplementedCommandExecutor(MessageTypes.UPDATE_ANTI_HARASSMENT_RULE),
            UnimplementedCommandExecutor(MessageTypes.DELETED_ANTI_HARASSMENT_RULE),
            UnimplementedCommandExecutor(MessageTypes.VOICE_CALL),
            UnimplementedCommandExecutor(MessageTypes.VIDEO_CALL),
            ClickRecommendItemExecutor(bindings.batch),
            GetMeetingInfoExecutor(bindings.structured),
            GetGroupMemberInfoExecutor(bindings.structured),
            GetGroupNameInfoExecutor(bindings.structured),
            GetDeviceResourceExecutor(bindings.resources, bindings.structured),
            GetLogSnippetExecutor(bindings.logs, bindings.structured, bindings.logSnippetGuard),
            UploadLogFileExecutor(bindings.logs, bindings.logUpload),
            RobotLogExecutor(bindings.report, bindings.logs),
            RobotErrorLogExecutor(bindings.report, bindings.logs),
            RobotQueueClearExecutor(bindings.queue, bindings.batch),
            RobotQueueRemoveExecutor(bindings.queue, bindings.batch),
            RobotQueueGetExecutor(bindings.queue, bindings.structured),
            RobotConfigUpdatedExecutor(bindings.appConfig),
            RobotControllerTestExecutor(),
            QrCodeJoinGroupExecutor(bindings.qrJoin),
            // B4 批（docs/10 §B B4 行：长尾 2 条）
            PushMicroprogramExecutor(bindings.remoteFiles, bindings.batch),
            PushOfficeExecutor(bindings.remoteFiles, bindings.batch),
        )
    }
}
