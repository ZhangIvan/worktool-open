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
 *
 * v0.1 首批补齐（docs/05 P0 首批行 501/502/509；查询类只读）：
 * - [GetGroupList]（type=501 GET_GROUP_INFO，FR-28）
 * - [GetFriendList]（type=502 GET_FRIEND_INFO，FR-29）
 * - [GetLocalFile]（type=509 GET_LOCAL_FILE，FR-33；dry-run=报告文件+大小）
 *
 * 上报方向意图（101）不进本接口——见 [MessageEvent]（端→云，非云下发指令）。
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

    // ---- 查询类 501/502/509（docs/05 P0 首批行；只读 → FR-53 L0） ----

    /**
     * 501 群信息查询（FR-28；2026-11-04 会话 10 对齐原版语义）：
     * 原版 `WeworkGetImpl.getGroupInfo(selectList)` = **按名单逐群进入读取**，
     * 空名单走「群管理页」枚举——企微 5.0.x 已无该入口（原版同构建同样
     * 「未找到群聊入口」失败），故空名单如实失败而非虚构枚举。
     * 名单项 = 消息列表会话标题（未命名群=成员名串，如「成员甲、成员乙、…」）。
     */
    data class GetGroupList(
        val groupNames: List<String> = emptyList(),
        val maxPages: Int = DEFAULT_MAX_PAGES,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_GROUP_INFO
        override val targetDescription: String get() =
            if (groupNames.isEmpty()) {
                "群信息查询（selectList 空，无枚举入口）"
            } else {
                "群信息查询 " + groupNames.take(3) + if (groupNames.size > 3) " 等${groupNames.size}群" else ""
            }

        companion object {
            /** 翻页护栏（历史枚举口径保留；按名单路径未用）。 */
            const val DEFAULT_MAX_PAGES: Int = 50

            /** 名单护栏（对齐原版逐群循环 + 防界面异常无界循环）。 */
            const val MAX_GROUPS: Int = 50
        }
    }

    /** 502 好友列表查询（FR-29）：导航+分页读取 → 结构化 [cn.worktool.open.feature.executor.query.QueryResult.FriendList]。 */
    data class GetFriendList(
        val maxPages: Int = DEFAULT_MAX_PAGES,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_FRIEND_INFO
        override val targetDescription: String get() = "通讯录联系人列表（全量分页）"

        companion object {
            const val DEFAULT_MAX_PAGES: Int = 50
        }
    }

    /**
     * 509 上传本地文件（FR-33；语义 = GET_LOCAL_FILE 上传，默认当前日志，
     * audit/02 F30）：dry-run 报告「将上传哪个文件 + 大小」。
     */
    data class GetLocalFile(
        /** 目标文件路径；null = 默认当前日志文件（执行器经 LocalFilePort 解析）。 */
        val filePath: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_LOCAL_FILE
        override val targetDescription: String get() = filePath ?: "当前日志文件（默认）"
    }

    // ---- B1 补齐（docs/10 §B B1 行：控制面 + 上报触发面） ----

    /** 201 停止并回首页（FR-52 接管联动：暂停请求 + 回企微消息首页）。 */
    data class StopAndGoHome(
        val reason: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.STOP_AND_GO_HOME
        override val targetDescription: String get() = "企微消息首页"
    }

    /**
     * 102 控制台提示（spec §4.2 云→端）：服务端向端推送错误提示；
     * 60s 节流 + 二次熔断语义在执行器（[cn.worktool.open.feature.executor.commands.ConsoleToastExecutor]）。
     */
    data class ConsoleToast(
        val errorCode: Int?,
        val errorReason: String?,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.TYPE_CONSOLE_TOAST
        override val targetDescription: String get() = "设备控制台（悬浮窗/系统对话框）"
    }

    /** 202 新消息接收主循环开关（docs/10 B1；载荷缺省 = 拒绝，fail-closed）。 */
    data class ReceiveLoopSwitch(
        val enabled: Boolean,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.LOOP_RECEIVE_NEW_MESSAGE
        override val targetDescription: String get() = if (enabled) "开启新消息接收主循环" else "关闭新消息接收主循环"
    }

    /** 283 触发一次新消息接收（读会话列表 → 经 101 信封上报，FR-01 主动触发面）。 */
    data class TriggerReceiveNewMessage(
        val maxConversations: Int = DEFAULT_MAX_CONVERSATIONS,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.DO_RECEIVE_NEW_MESSAGE
        override val targetDescription: String get() = "会话列表（单轮扫描上限 $maxConversations）"

        companion object {
            /** 单轮扫描护栏（对齐 PassFriend/翻页上限口径，防异常列表无界遍历）。 */
            const val DEFAULT_MAX_CONVERSATIONS: Int = 50
        }
    }

    // ---- B2 批（docs/10 §B B2 行：消息·联系人·文件·查询 22 条） ----

    /** 204 引用回复：按发送人+原文定位消息 → 引用回复正文。 */
    data class ReplyMessage(
        val targets: List<String>,
        val quotedSender: String,
        val quotedContent: String,
        val quotedTextType: Int,
        val content: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.REPLY_MESSAGE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 205 转发单条消息：源会话定位 → 转发给多个接收人（附加留言可选）。 */
    data class RelayMessage(
        val sourceRooms: List<String>,
        val quotedSender: String,
        val quotedContent: String,
        val quotedTextType: Int,
        val receivers: List<String>,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.RELAY_MESSAGE
        override val targetDescription: String get() = sourceRooms.joinToString("、")
    }

    /** 逐条转发载荷条目（227/228 共用子结构：原文定位键）。 */
    data class RelayItem(
        val originalContent: String,
        val textType: Int,
    )

    /** 227/228 意图公共面（同构载荷，仅指令号/模式语义不同）。 */
    interface RelayBatchIntent : CommandIntent {
        val sourceRooms: List<String>
        val messages: List<RelayItem>
        val receivers: List<String>
        val extraText: String?
    }

    /** 227 多条逐条转发（mode=逐条：每条独立送达）。 */
    data class RelayMultiMessage(
        override val sourceRooms: List<String>,
        override val messages: List<RelayItem>,
        override val receivers: List<String>,
        override val extraText: String? = null,
    ) : RelayBatchIntent {
        override val commandType: Int get() = MessageTypes.RELAY_MULTI_MESSAGE
        override val targetDescription: String get() = sourceRooms.joinToString("、")
    }

    /** 228 多条合并转发（mode=合并：打包为一条聊天记录）。 */
    data class RelayMergeMessage(
        override val sourceRooms: List<String>,
        override val messages: List<RelayItem>,
        override val receivers: List<String>,
        override val extraText: String? = null,
    ) : RelayBatchIntent {
        override val commandType: Int get() = MessageTypes.RELAY_MERGE_MESSAGE
        override val targetDescription: String get() = sourceRooms.joinToString("、")
    }

    /** 批量发送出站消息条目（229/230 共用；文本/文件/链接等按 textType 分流）。 */
    data class OutboundMessage(
        val textType: Int,
        val content: String?,
        val objectName: String? = null,
        val fileUrl: String? = null,
        val fileType: String? = null,
    )

    /** 229 批量发送（逐条：N 条异构消息 × M 接收人按序逐条送达）。 */
    data class SendMultiMessage(
        val messages: List<OutboundMessage>,
        val receivers: List<String>,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SEND_MULTI_MESSAGE
        override val targetDescription: String get() = receivers.joinToString("、")
    }

    /** 230 合并发送（N 条消息打包为一条聊天记录发 M 接收人）。 */
    data class SendMergeMessage(
        val messages: List<OutboundMessage>,
        val receivers: List<String>,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SEND_MERGE_MESSAGE
        override val targetDescription: String get() = receivers.joinToString("、")
    }

    /** 238 批量连续文本（receivedContent=JSON 数组字符串，规范化侧已解析为有序文本）。 */
    data class SendBatchText(
        val targets: List<String>,
        val texts: List<String>,
        val atList: List<String> = emptyList(),
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 226 撤回消息（按原文定位机器人近期所发消息 → 长按撤回）。 */
    data class RecallMessage(
        val targets: List<String>,
        val originalContent: String,
        val textType: Int,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.RECALL_MESSAGE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 213 手机号加好友（friend 子结构；附言存在=添加语义、缺省=修改好友信息语义）。 */
    data class AddFriendByPhone(
        val phone: String,
        val name: String? = null,
        val markName: String? = null,
        val markExtra: String? = null,
        val tagList: List<String> = emptyList(),
        val leavingMsg: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.ADD_FRIEND_BY_PHONE
        override val targetDescription: String get() = phone
    }

    /** 220 群内加好友（目标外部群 + 群内成员名）。 */
    data class AddFriendByGroup(
        val groupName: String,
        val memberName: String,
        val markName: String? = null,
        val markExtra: String? = null,
        val tagList: List<String> = emptyList(),
        val leavingMsg: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.ADD_FRIEND_BY_GROUP
        override val targetDescription: String get() = "$groupName/$memberName"
    }

    /** 234 删除联系人（FR-53 L2：不可逆，独立 approver 由服务端治理）。 */
    data class DeleteContact(
        val contactName: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.DELETE_CONTACT
        override val targetDescription: String get() = contactName
    }

    /** 236 删除单向微信客户（规范化侧已去重去空并执行 ≤50 校验）。 */
    data class DeleteSingleDirectionalContact(
        val names: List<String>,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT
        override val targetDescription: String get() = names.joinToString("、")
    }

    /** 218 推送任意文件（fileUrl 端侧下载 → objectName 落盘 → 逐会话发送）。 */
    data class PushFile(
        val targets: List<String>,
        val objectName: String,
        val fileUrl: String,
        val fileType: String,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_FILE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 224 发送链接卡片（字段为 WorkTool-Open 自拟 schema，厂商无公开契约）。 */
    data class PushLink(
        val targets: List<String>,
        val title: String,
        val description: String,
        val url: String,
        val iconUrl: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_LINK
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 208 推微盘图片（objectName=微盘内已存图片显示名，无下载动作）。 */
    data class PushMicroDiskImage(
        val targets: List<String>,
        val objectName: String,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_MICRO_DISK_IMAGE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 209 推微盘文件（同 208 流程，目标为任意类型文件）。 */
    data class PushMicroDiskFile(
        val targets: List<String>,
        val objectName: String,
        val extraText: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_MICRO_DISK_FILE
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 503 获取我的信息（「我」页/个人信息详情页读取 → 结构化推送）。 */
    class GetMyInfo : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_MY_INFO
        override val targetDescription: String get() = "企微「我」个人信息页"
    }

    /** 505 获取最近会话列表（消息首页列表去重采集 → 结构化推送）。 */
    class GetRecentList : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_RECENT_LIST
        override val targetDescription: String get() = "消息首页最近会话列表"
    }

    /** 506 获取企业列表（企业切换页采集 → 结构化推送；厂商无公开 schema，自拟等价面）。 */
    class GetCorpList : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_CORP_LIST
        override val targetDescription: String get() = "企业切换页企业列表"
    }

    /** 504 获取群二维码（我方自拟兼容面：厂商 APK 无主动执行器；进群设置读码 → 推送）。 */
    data class GetGroupQrCode(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_GROUP_QRCODE
        override val targetDescription: String get() = groupName
    }

    /** 507 获取全部好友信息（「我的客户」列表分页采集 → 名称集合）。 */
    class GetAllFriendInfo : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_ALL_FRIEND_INFO
        override val targetDescription: String get() = "通讯录「我的客户」全量列表"
    }

    /** 508 获取全部群信息（群聊列表全量 + 逐群详情，每群一条结构化推送）。 */
    class GetAllGroupInfo : CommandIntent {
        override val commandType: Int get() = MessageTypes.GET_ALL_GROUP_INFO
        override val targetDescription: String get() = "通讯录「群聊」全量列表 + 逐群详情"
    }

    // ---- B3 批（docs/10 §B B3 行：群运营·效率·运维 30 条） ----

    /** 219 解散群（FR-53 L2：需群主身份，解散不可逆）。 */
    data class DismissGroup(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.DISMISS_GROUP
        override val targetDescription: String get() = groupName
    }

    /** 214 展示/读取群信息（群名/备注/群主/公告/成员列表 → 结构化推送）。 */
    data class ShowGroupInfo(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SHOW_GROUP_INFO
        override val targetDescription: String get() = groupName
    }

    /** 239 创建客户群永久入群群活码（create 新建 / select 绑定已有群，成功推 504）。 */
    data class CreateCustomerGroupLiveQrCode(
        val groupMode: String?,
        val selectList: List<String>,
        val newGroupName: String,
        val groupTemplate: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE
        override val targetDescription: String get() = newGroupName
    }

    /** 240 添加群看板网页（群公告网页板式；仅内部群，字段为 WorkTool-Open 自拟）。 */
    data class AddGroupBoardWebpage(
        val groupName: String,
        val url: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.ADD_GROUP_BOARD_WEBPAGE
        override val targetDescription: String get() = groupName
    }

    /** 244 退出群聊（普通成员退出 + 清残留会话；群主身份被拦截）。 */
    data class ExitGroup(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.EXIT_GROUP
        override val targetDescription: String get() = groupName
    }

    /** 237 清理企微存储空间（破坏性清理：缓存 + 资源文件和其他企业的聊天记录）。 */
    class CleanCache : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.CLEAN_CACHE
        override val targetDescription: String get() = "企微设置→存储空间清理"
    }

    /** 221 添加待办（仅企业内部成员；逐目标进单聊发待办）。 */
    data class AddNeedDeal(
        val targets: List<String>,
        val content: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.ADD_NEED_DEAL
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 222 上下班打卡（无参：按页面首个可用打卡按钮执行）。 */
    class ClockIn : CommandIntent {
        override val commandType: Int get() = MessageTypes.CLOCK_IN
        override val targetDescription: String get() = "工作台→打卡"
    }

    /** 223 切换企业（每次切换 = 登出登录，勿频繁；成功后重取自身信息）。 */
    data class SwitchCorp(
        val targetCorp: String,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SWITCH_CORP
        override val targetDescription: String get() = targetCorp
    }

    /** 233 扫一扫（入群扫码双语义；字段名未冻结，码内容可选直传）。 */
    data class ScanQrCode(
        val qrContent: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.SCAN_QR_CODE
        override val targetDescription: String get() = qrContent ?: "企微扫一扫"
    }

    /** 215/216/217/231/232 排除性占位（docs/10：双侧皆无实现 → 显式「未实现」回执）。 */
    data class UnimplementedCall(
        override val commandType: Int,
    ) : CommandIntent {
        override val targetDescription: String get() = "未实现指令占位（N/A-APK）"
    }

    /** 235 点击聊天推荐条（四形态：发起收款/企业名片/推荐客服/商品图册）。 */
    data class ClickRecommendItem(
        val targets: List<String>,
        val itemName: String,
        val amount: String? = null,
        val productName: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.CLICK_RECOMMEND_ITEM
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 511 会议信息查询（objectName=yyyy-MM-dd，今天或未来 28 天内）。 */
    data class GetMeetingInfo(
        val date: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.GET_MEETING_INFO
        override val targetDescription: String get() = date
    }

    /** 512 群成员信息查询（含群名/备注/群主/公告/成员列表；已退群=失败语义）。 */
    data class GetGroupMemberInfo(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.GET_GROUP_MEMBER_INFO
        override val targetDescription: String get() = groupName
    }

    /** 515 获取群名和群备注（轻量：不读成员列表；已退群仍可搜索=成功）。 */
    data class GetGroupNameInfo(
        val groupName: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.GET_GROUP_NAME_INFO
        override val targetDescription: String get() = groupName
    }

    /** 513 设备资源信息查询（纯本地采集，不动企微界面）。 */
    class GetDeviceResource : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.GET_DEVICE_RESOURCE
        override val targetDescription: String get() = "本机资源快照（内存/存储/系统信息）"
    }

    /** 514 获取日志片段（messageId 定位或时间段两种模式，互斥单飞）。 */
    data class GetLogSnippet(
        val targetMessageId: String? = null,
        val logStartTime: String? = null,
        val logEndTime: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.GET_LOG_SNIPPET
        override val targetDescription: String
            get() = targetMessageId?.let { "messageId=$it" } ?: "时间段 $logStartTime~$logEndTime"
    }

    /** 510 上传日志文件（logDate 缺省=今天；整日日志打包上传）。 */
    data class UploadLogFile(
        val logDate: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.UPLOAD_LOG_FILE
        override val targetDescription: String get() = logDate ?: "今天"
    }

    /** 301/302 意图公共面（同构载荷，仅通道不同）。 */
    interface RobotLogIntent : CommandIntent {
        val logText: String?
    }

    /** 301 运行日志上报（端→云出站通道的本地触发面；SR-06 脱敏）。 */
    data class RobotLog(
        override val logText: String? = null,
    ) : RobotLogIntent {
        override val commandType: Int get() = MessageTypes.ROBOT_LOG
        override val targetDescription: String get() = "运行日志通道"
    }

    /** 302 错误日志上报（崩溃/错误级事件出站通道）。 */
    data class RobotErrorLog(
        override val logText: String? = null,
    ) : RobotLogIntent {
        override val commandType: Int get() = MessageTypes.ROBOT_ERROR_LOG
        override val targetDescription: String get() = "错误日志通道"
    }

    /** 304 清空待执行指令队列（FR-53 L2）。 */
    class RobotQueueClear : CommandIntent {
        override val commandType: Int get() = MessageTypes.ROBOT_QUEUE_CLEAR
        override val targetDescription: String get() = "全部待执行指令"
    }

    /** 305 清除指定待执行指令（originalContent=messageId，中英逗号分隔批量）。 */
    data class RobotQueueRemove(
        val messageIds: List<String>,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.ROBOT_QUEUE_REMOVE
        override val targetDescription: String get() = messageIds.joinToString("、")
    }

    /** 306 查询待执行队列（只读快照；showMessageHistory=true 附指令详情）。 */
    data class RobotQueueGet(
        val showMessageHistory: Boolean = false,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.ROBOT_QUEUE_GET
        override val targetDescription: String get() = "待执行队列快照"
    }

    /** 307 配置更新通知（通知即全量重拉服务端配置，载荷忽略）。 */
    class RobotConfigUpdated : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.ROBOT_CONFIG_UPDATED
        override val targetDescription: String get() = "服务端配置全量重拉"
    }

    /** 303 控制器连通性测试（回显即测试：无 UI 动作，唯一效果=日志回显）。 */
    data class RobotControllerTest(
        val echo: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.ROBOT_CONTROLLER_TEST
        override val targetDescription: String get() = "链路探活（日志回显）"
    }

    /** 243 二维码图片 URL 入群（下载写相册 → 个微侧扫码入群，依赖 FR-56 规则包）。 */
    data class QrCodeJoinGroup(
        val fileUrl: String,
    ) : CommandIntent {
        override val commandType: Int get() = ExtendedCommandTypes.QR_CODE_JOIN_GROUP
        override val targetDescription: String get() = fileUrl
    }

    // ---- B4 批（docs/10 §B B4 行：长尾 2 条） ----

    /** 210 推小程序卡片（厂商无公开 schema，字段自拟；R6 UI 自动化等价路径）。 */
    data class PushMicroprogram(
        val targets: List<String>,
        val title: String,
        val description: String? = null,
        val pageUrl: String,
        val iconUrl: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_MICROPROGRAM
        override val targetDescription: String get() = targets.joinToString("、")
    }

    /** 211 推腾讯文档卡片（docs/10 B4：厂商已废弃企微≥4.1.32；我方等价实现保通道）。 */
    data class PushOffice(
        val targets: List<String>,
        val title: String,
        val description: String? = null,
        val docUrl: String,
        val iconUrl: String? = null,
    ) : CommandIntent {
        override val commandType: Int get() = MessageTypes.PUSH_OFFICE
        override val targetDescription: String get() = targets.joinToString("、")
    }
}

/**
 * 指令载荷候选字段名（集中一处，冻结时只改这里）。
 *
 * spec §4.4 冻结状态：`targetRoom`/`receivedContent` 为**候选字段名**（待 T3.2 apifox
 * 冒烟确认，冻结前不得作为 v1 互通依据）；`at`/`atList`/`extraText`/`friend` 为已还原
 * 字段（spec §4.4「已还原」行）。206/207/225 的正文/名单字段同样未冻结（spec §9 Q1），
 * 本对象中的候选名 = WorkTool-Open 端侧暂定，冻结后如需改名仅调整 [IntentNormalizer]。
 *
 * 2026-11-05 契约冻结车道（docs/16 §指令字段契约冻结）：203/206/207/225 主读字段
 * 已冻结为 upstream openapi 契约名（203=titleList、206 名单=selectList、207/225
 * 目标=groupName）——见 [IntentNormalizer] 各 normalize* 决策注释；
 * [TARGET_ROOM]/[GROUP_MEMBERS] 降级为迁移期只读别名/退役候选名。
 */
object PayloadFields {
    /**
     * 目标会话/群名（203/207/225 曾用候选名；2026-11-05 起三指令主读
     * titleList/groupName，本名降级为迁移期只读别名，T3.2 冒烟后撤）。
     */
    const val TARGET_ROOM = "targetRoom"

    /** 指令正文（候选，203）。 */
    const val RECEIVED_CONTENT = "receivedContent"

    /** 新群名（候选，206）。 */
    const val GROUP_NAME = "groupName"

    /** 建群拉人名单（206 曾用候选名；2026-11-05 起主读 [SELECT_LIST]，本名退役不再读取）。 */
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

    /** 目标文件路径（候选，509；空缺 = 默认当前日志文件，audit/02 F30）。 */
    const val FILE_PATH = "filePath"

    // ---- 对等计划扩展批（docs/10 §B）候选字段名（同样待冻结口径） ----

    /** 目标会话/接收人数组（203/204/205/208/209/218/221/224/226/227/229/230/235/238/210/211 通例；203 于 2026-11-05 冻结入列）。 */
    const val TITLE_LIST = "titleList"

    /** 接收人数组（205/227/228/229/230；无 titleList 的批次指令由本字段承担目标）。 */
    const val NAME_LIST = "nameList"

    /** 被引用/原始消息发送人（204/205）。 */
    const val RECEIVED_NAME = "receivedName"

    /** 被引用/原始消息内容（204/205/226/305/514 复用为 messageId）。 */
    const val ORIGINAL_CONTENT = "originalContent"

    /** 群名（219/220/240/244/504/512/515 等复用上方 [GROUP_NAME] 同名候选）。 */
    const val OBJECT_NAME = "objectName"

    /** 附加留言（205/208/209/218/227/228/229/230；与 extraText 已还原字段二选一）。 */
    const val EXTRA_TEXT_FIELD = "extraText"

    /** 文件类型（218：image、audio、video 或星号通配其他）。 */
    const val FILE_TYPE_FIELD = "fileType"

    /** 链接/文档跳转 URL（224/240/210/211 自拟 schema）。 */
    const val FILE_URL_FIELD = "fileUrl"

    /** icon 图标 URL（224/210/211 自拟 schema）。 */
    const val ICON_URL = "iconUrl"

    /** 链接副标题/描述（224/210/211 自拟 schema）。 */
    const val DESCRIPTION = "description"

    /** 转发消息条目数组（227/228：SubMessageBean 轻量结构）。 */
    const val MESSAGE_LIST = "messageList"

    /** 批量发送完整消息对象数组（229/230）。 */
    const val WEWORK_MESSAGE_LIST = "weworkMessageList"

    /** 群活码模式（239：create/select）。 */
    const val GROUP_MODE = "groupMode"

    /** 群活码选择已有群列表（239，≤5 不重复）。 */
    const val SELECT_LIST = "selectList"

    /** 群活码新群名（239；兼容 groupName 取值）。 */
    const val NEW_GROUP_NAME = "newGroupName"

    /** 客户群模板名（239，可选）。 */
    const val GROUP_TEMPLATE = "groupTemplate"

    /** 主循环开关（202；"true"/"false"）。 */
    const val LOOP_ENABLED = "loopEnabled"

    /** 待取消指令 messageId 串（305；中英逗号分隔）。 */
    const val MESSAGE_IDS = "messageIds"

    /** 队列详情开关（306；true=回执附指令全量 JSON）。 */
    const val SHOW_MESSAGE_HISTORY = "showMessageHistory"

    /** 日志日期（510；yyyy-MM-dd，缺省今天）。 */
    const val LOG_DATE = "logDate"

    /** 日志片段时间范围起点（514）。 */
    const val LOG_START_TIME = "logStartTime"

    /** 日志片段时间范围终点（514）。 */
    const val LOG_END_TIME = "logEndTime"

    /** 日志自由文本（301/302 出站通道）。 */
    const val LOG_TEXT = "log"

    /** 扫码内容直传（233 自拟；缺省走相机/相册扫描路径）。 */
    const val QR_CONTENT = "qrContent"
}
