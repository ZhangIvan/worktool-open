/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.intent

import cn.worktool.open.core.protocol.MessageTypes

/**
 * 规范化指令意图（T3.1 执行器统一输入）。
 *
 * 来源链路：协议载荷 [cn.worktool.open.core.protocol.WeworkMessageBean]（type = spec §4.2
 * 指令号）经 [IntentNormalizer] 规范化为强类型意图；执行器只消费强类型，不触碰线上字段。
 *
 * P0 子集（docs/03 T3.1 / docs/01 FR-02/09/10/15）：
 * - [SendText]（type=203 SEND_MESSAGE，FR-02）
 * - [InitGroup]（type=206 INIT_GROUP，FR-09）
 * - [UpdateGroup]（type=207 UPDATE_GROUP，FR-10）
 * - [ModifyGroupMemberInfo]（type=225 MODIFY_GROUP_MEMBER_INFO，FR-10）
 * - [PassFriend]（type=212 PASS_ALL_FRIEND_REQUEST，FR-15）
 */
sealed interface CommandIntent {

    /** 协议指令号（spec §4.2 全表；常量复用 core:protocol [MessageTypes]）。 */
    val commandType: Int

    /** 执行报告展示用目标描述（FR-54 干跑报告「含目标」）。 */
    val targetDescription: String

    /** 203 发文本（支持 @ 列表，spec §4.2「发文本（支持 at/atList）」）。 */
    data class SendText(
        val targetRoom: String,
        val content: String,
        val atList: List<String> = emptyList(),
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SEND_MESSAGE
        override val targetDescription: String get() = targetRoom
    }

    /** 206 建群（拉人/公告，spec §4.2「建群（拉人/公告/备注/模板）」；备注/模板非 P0 必选）。 */
    data class InitGroup(
        val groupName: String,
        val members: List<String> = emptyList(),
        val announcement: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.INIT_GROUP
        override val targetDescription: String get() = groupName
    }

    /** 207 改群配置（FR-10 公告/备注/拉人；踢人非本 P0 子集，留待后续任务卡）。 */
    data class UpdateGroup(
        val targetGroup: String,
        val announcement: String? = null,
        val groupRemark: String? = null,
        val addMembers: List<String> = emptyList(),
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.UPDATE_GROUP
        override val targetDescription: String get() = targetGroup
    }

    /** 225 改群成员备注/标签（FR-10；成员定位复用已还原 Friend 子结构字段，spec §4.3）。 */
    data class ModifyGroupMemberInfo(
        val targetGroup: String,
        val memberName: String,
        val markName: String? = null,
        val tagList: List<String> = emptyList(),
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.MODIFY_GROUP_MEMBER_INFO
        override val targetDescription: String get() = "$targetGroup/$memberName"
    }

    /** 212 通过好友请求（spec §4.2：上游开源版空实现；我方语义 = 通过当前全部待处理请求）。 */
    data class PassFriend(
        val limit: Int = DEFAULT_ACCEPT_LIMIT,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PASS_ALL_FRIEND_REQUEST
        override val targetDescription: String get() = "全部待处理好友请求"

        companion object {
            /** 单轮通过上限（防界面异常时的无界循环；FR-53 审慎默认）。 */
            const val DEFAULT_ACCEPT_LIMIT: Int = 50
        }
    }
}

/**
 * 指令载荷候选字段名（集中一处，冻结时只改这里）。
 *
 * spec §4.4 冻结状态：`targetRoom`/`receivedContent` 为**候选字段名**（待 T3.2 apifox
 * 冒烟确认，冻结前不得作为 v1 互通依据）；`at`/`atList`/`extraText`/`friend` 为已还原
 * 字段（spec §4.4「已还原」行）。206/207/225 的正文/名单字段同样未冻结（spec §9 Q1），
 * 本对象中的候选名 = WorkTool-Open 端侧暂定，冻结后如需改名仅调整 [IntentNormalizer]。
 */
object PayloadFields {
    /** 目标会话/群名（候选，203/207/225 复用）。 */
    const val TARGET_ROOM = "targetRoom"

    /** 指令正文（候选，203）。 */
    const val RECEIVED_CONTENT = "receivedContent"

    /** 新群名（候选，206）。 */
    const val GROUP_NAME = "groupName"

    /** 建群拉人名单（候选，206；元素为成员展示名）。 */
    const val GROUP_MEMBERS = "groupMembers"

    /** 群公告（候选，206/207；与已还原 extraText 二选一，见 [IntentNormalizer]）。 */
    const val ANNOUNCEMENT = "announcement"

    /** 群备注（候选，207）。 */
    const val GROUP_REMARK = "groupRemark"

    /** 改群拉人名单（候选，207；FR-53 拉人 = L1 风险）。 */
    const val ADD_MEMBERS = "addMembers"

    /** 成员展示名（候选，225；备选取值为 friend.name）。 */
    const val MEMBER_NAME = "memberName"

    /** 成员备注（候选，225；备选取值为 friend.markName，spec §4.3 Friend 已还原字段）。 */
    const val MARK_NAME = "markName"

    /** 成员标签（候选，225；备选取值为 friend.tagList，spec §4.3 Friend 已还原字段）。 */
    const val TAG_LIST = "tagList"
}
