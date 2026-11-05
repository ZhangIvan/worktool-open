/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.intent

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.WeworkMessageBean

/**
 * 协议载荷 → 规范化 [CommandIntent]（T3.1 执行器唯一入口形态）。
 *
 * 规范化规则（静态校验；对应状态机 E01/E02 guard 的端侧前置形态，
 * protocol/state-transitions.csv：载荷 schema 校验失败即拒绝）：
 * - type 不在注册表支持集 → [NormalizedIntent.Unsupported]；
 * - 已支持但必填字段缺失/为空 → [NormalizedIntent.Invalid]；
 * - 目标/正文字段取值见 [PayloadFields]（候选字段冻结状态以 spec §4.4 为准）。
 */
class IntentNormalizer(
    /** 支持的指令号集合（= 注册表 supportedTypes；注册表构造时传入，保持单一事实）。 */
    private val supportedTypes: Set<Int>,
) {

    fun normalize(bean: WeworkMessageBean): NormalizedIntent = when (bean.type) {
        !in supportedTypes -> NormalizedIntent.Unsupported(bean.type)
        MessageTypes.SEND_MESSAGE -> normalizeSendText(bean)
        MessageTypes.INIT_GROUP -> normalizeInitGroup(bean)
        MessageTypes.UPDATE_GROUP -> normalizeUpdateGroup(bean)
        MessageTypes.MODIFY_GROUP_MEMBER_INFO -> normalizeModifyMember(bean)
        MessageTypes.PASS_ALL_FRIEND_REQUEST -> NormalizedIntent.Valid(
            CommandIntent.PassFriend(),
        )
        MessageTypes.GET_GROUP_INFO -> NormalizedIntent.Valid(
            CommandIntent.GetGroupList(groupNames = selectList(bean)),
        )
        MessageTypes.GET_FRIEND_INFO -> NormalizedIntent.Valid(CommandIntent.GetFriendList())
        MessageTypes.GET_LOCAL_FILE -> normalizeGetLocalFile(bean)

        // ---- B1 补齐（docs/10 §B B1 行） ----
        MessageTypes.STOP_AND_GO_HOME -> NormalizedIntent.Valid(
            CommandIntent.StopAndGoHome(reason = bean.errorReason?.takeIf { it.isNotBlank() }),
        )
        MessageTypes.TYPE_CONSOLE_TOAST -> NormalizedIntent.Valid(
            CommandIntent.ConsoleToast(errorCode = bean.errorCode, errorReason = bean.errorReason),
        )
        MessageTypes.LOOP_RECEIVE_NEW_MESSAGE -> normalizeLoopSwitch(bean)
        ExtendedCommandTypes.DO_RECEIVE_NEW_MESSAGE -> NormalizedIntent.Valid(
            CommandIntent.TriggerReceiveNewMessage(),
        )

        // ---- B2 批（docs/10 §B B2 行） ----
        MessageTypes.REPLY_MESSAGE -> normalizeReply(bean)
        MessageTypes.RELAY_MESSAGE -> normalizeRelay(bean)
        MessageTypes.RELAY_MULTI_MESSAGE -> normalizeRelayBatch(bean, merge = false)
        MessageTypes.RELAY_MERGE_MESSAGE -> normalizeRelayBatch(bean, merge = true)
        MessageTypes.SEND_MULTI_MESSAGE -> normalizeSendBatch(bean, merge = false)
        MessageTypes.SEND_MERGE_MESSAGE -> normalizeSendBatch(bean, merge = true)
        ExtendedCommandTypes.SEND_BATCH_TEXT_MESSAGE -> normalizeSendBatchText(bean)
        MessageTypes.RECALL_MESSAGE -> normalizeRecall(bean)
        MessageTypes.ADD_FRIEND_BY_PHONE -> normalizeAddFriendByPhone(bean)
        MessageTypes.ADD_FRIEND_BY_GROUP -> normalizeAddFriendByGroup(bean)
        MessageTypes.DELETE_CONTACT -> normalizeDeleteContact(bean)
        ExtendedCommandTypes.DELETE_SINGLE_DIRECTIONAL_CONTACT -> normalizeDeleteSingleDirectional(bean)
        MessageTypes.PUSH_FILE -> normalizePushFile(bean)
        MessageTypes.PUSH_LINK -> normalizePushLink(bean)
        MessageTypes.PUSH_MICRO_DISK_IMAGE -> normalizeMicroDisk(bean, image = true)
        MessageTypes.PUSH_MICRO_DISK_FILE -> normalizeMicroDisk(bean, image = false)
        MessageTypes.GET_MY_INFO -> NormalizedIntent.Valid(CommandIntent.GetMyInfo())
        MessageTypes.GET_RECENT_LIST -> NormalizedIntent.Valid(CommandIntent.GetRecentList())
        MessageTypes.GET_CORP_LIST -> NormalizedIntent.Valid(CommandIntent.GetCorpList())
        MessageTypes.GET_GROUP_QRCODE -> normalizeGroupNameOnly(bean) { CommandIntent.GetGroupQrCode(it) }
        MessageTypes.GET_ALL_FRIEND_INFO -> NormalizedIntent.Valid(CommandIntent.GetAllFriendInfo())
        MessageTypes.GET_ALL_GROUP_INFO -> NormalizedIntent.Valid(CommandIntent.GetAllGroupInfo())

        // ---- B3 批（docs/10 §B B3 行） ----
        MessageTypes.DISMISS_GROUP -> normalizeGroupNameOnly(bean) { CommandIntent.DismissGroup(it) }
        MessageTypes.SHOW_GROUP_INFO -> normalizeGroupNameOnly(bean) { CommandIntent.ShowGroupInfo(it) }
        ExtendedCommandTypes.CREATE_CUSTOMER_GROUP_LIVE_QRCODE -> normalizeLiveQr(bean)
        ExtendedCommandTypes.ADD_GROUP_BOARD_WEBPAGE -> normalizeBoardWebpage(bean)
        ExtendedCommandTypes.EXIT_GROUP -> normalizeGroupNameOnly(bean) { CommandIntent.ExitGroup(it) }
        ExtendedCommandTypes.CLEAN_CACHE -> NormalizedIntent.Valid(CommandIntent.CleanCache())
        MessageTypes.ADD_NEED_DEAL -> normalizeAddNeedDeal(bean)
        MessageTypes.CLOCK_IN -> NormalizedIntent.Valid(CommandIntent.ClockIn())
        MessageTypes.SWITCH_CORP -> normalizeSwitchCorp(bean)
        MessageTypes.SCAN_QR_CODE -> normalizeScanQr(bean)
        MessageTypes.INIT_ANTI_HARASSMENT_RULE,
        MessageTypes.UPDATE_ANTI_HARASSMENT_RULE,
        MessageTypes.DELETED_ANTI_HARASSMENT_RULE,
        MessageTypes.VOICE_CALL,
        MessageTypes.VIDEO_CALL,
        -> NormalizedIntent.Valid(CommandIntent.UnimplementedCall(bean.type))
        ExtendedCommandTypes.CLICK_RECOMMEND_ITEM -> normalizeRecommendItem(bean)
        ExtendedCommandTypes.GET_MEETING_INFO -> normalizeMeetingInfo(bean)
        ExtendedCommandTypes.GET_GROUP_MEMBER_INFO ->
            normalizeGroupNameOnly(bean) { CommandIntent.GetGroupMemberInfo(it) }
        ExtendedCommandTypes.GET_GROUP_NAME_INFO ->
            normalizeGroupNameOnly(bean) { CommandIntent.GetGroupNameInfo(it) }
        ExtendedCommandTypes.GET_DEVICE_RESOURCE -> NormalizedIntent.Valid(CommandIntent.GetDeviceResource())
        ExtendedCommandTypes.GET_LOG_SNIPPET -> normalizeLogSnippet(bean)
        ExtendedCommandTypes.UPLOAD_LOG_FILE -> normalizeUploadLog(bean)
        MessageTypes.ROBOT_LOG -> normalizeRobotLog(bean) { CommandIntent.RobotLog(it) }
        MessageTypes.ROBOT_ERROR_LOG -> normalizeRobotLog(bean) { CommandIntent.RobotErrorLog(it) }
        MessageTypes.ROBOT_QUEUE_CLEAR -> NormalizedIntent.Valid(CommandIntent.RobotQueueClear())
        ExtendedCommandTypes.ROBOT_QUEUE_REMOVE -> normalizeQueueRemove(bean)
        ExtendedCommandTypes.ROBOT_QUEUE_GET -> NormalizedIntent.Valid(
            CommandIntent.RobotQueueGet(
                showMessageHistory = bean.extraString(PayloadFields.SHOW_MESSAGE_HISTORY)
                    ?.lowercase() == "true",
            ),
        )
        ExtendedCommandTypes.ROBOT_CONFIG_UPDATED -> NormalizedIntent.Valid(CommandIntent.RobotConfigUpdated())
        MessageTypes.ROBOT_CONTROLLER_TEST -> NormalizedIntent.Valid(
            CommandIntent.RobotControllerTest(echo = bean.extraString(PayloadFields.OBJECT_NAME)),
        )
        ExtendedCommandTypes.QR_CODE_JOIN_GROUP -> normalizeQrJoin(bean)

        // ---- B4 批（docs/10 §B B4 行） ----
        MessageTypes.PUSH_MICROPROGRAM -> normalizeCard(bean) { t, d, u, i ->
            CommandIntent.PushMicroprogram(t, d.first, d.second, u, i)
        }
        MessageTypes.PUSH_OFFICE -> normalizeCard(bean) { t, d, u, i ->
            CommandIntent.PushOffice(t, d.first, d.second, u, i)
        }
        else -> NormalizedIntent.Unsupported(bean.type)
    }

    // ---- B3/B4 规范化 ----

    /** 239：newGroupName 必填（兼容 groupName）；select 模式 selectList ≤5 不重复。 */
    private fun normalizeLiveQr(bean: WeworkMessageBean): NormalizedIntent {
        val newName = bean.extraString(PayloadFields.NEW_GROUP_NAME)?.takeIf { it.isNotBlank() }
            ?: bean.extraString(PayloadFields.GROUP_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.NEW_GROUP_NAME, "必填且非空（兼容 groupName）")
        val mode = bean.extraString(PayloadFields.GROUP_MODE)?.takeIf { it == "create" || it == "select" }
        val selectList = bean.stringListExtra(PayloadFields.SELECT_LIST).filter { it.isNotBlank() }.distinct()
        if (selectList.size > 5) {
            return invalid(bean.type, PayloadFields.SELECT_LIST, "最多 5 个且不可重复")
        }
        if (mode != null && selectList.isEmpty() && mode == "select") {
            return invalid(bean.type, PayloadFields.SELECT_LIST, "select 模式必填（完整群名精确匹配）")
        }
        return NormalizedIntent.Valid(
            CommandIntent.CreateCustomerGroupLiveQrCode(
                groupMode = mode,
                selectList = selectList,
                newGroupName = newName,
                groupTemplate = bean.extraString(PayloadFields.GROUP_TEMPLATE)?.takeIf { it.isNotBlank() },
            ),
        )
    }

    /** 240：groupName + 网页 URL（objectName 或 fileUrl，须 http(s)——字段名自拟待冻结）。 */
    private fun normalizeBoardWebpage(bean: WeworkMessageBean): NormalizedIntent {
        val group = bean.extraString(PayloadFields.GROUP_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空")
        val url = bean.extraString(PayloadFields.OBJECT_NAME)
            ?: bean.fileUrl
            ?: bean.extraString(PayloadFields.FILE_URL_FIELD)
        val urlSafe = url?.takeIf { it.startsWith("http://") || it.startsWith("https://") }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且须 http(s) URL（网页链接）")
        return NormalizedIntent.Valid(CommandIntent.AddGroupBoardWebpage(groupName = group, url = urlSafe))
    }

    /** 221：titleList（内部成员）+ receivedContent 必填。 */
    private fun normalizeAddNeedDeal(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空（仅内部成员）")
        val content = bean.extraString(PayloadFields.RECEIVED_CONTENT)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填且非空")
        return NormalizedIntent.Valid(CommandIntent.AddNeedDeal(targets, content))
    }

    /** 223：objectName=目标企业名必填。 */
    private fun normalizeSwitchCorp(bean: WeworkMessageBean): NormalizedIntent {
        val corp = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且非空（目标企业名称）")
        return NormalizedIntent.Valid(CommandIntent.SwitchCorp(targetCorp = corp))
    }

    /** 233：qrContent 可选直传（字段名自拟待冻结）。 */
    private fun normalizeScanQr(bean: WeworkMessageBean): NormalizedIntent =
        NormalizedIntent.Valid(
            CommandIntent.ScanQrCode(qrContent = bean.extraString(PayloadFields.QR_CONTENT)?.takeIf { it.isNotBlank() }),
        )

    /**
     * 235：titleList + receivedContent（推荐条文案）必填；
     * 发起收款 → extraText 金额必填（≤2 位小数）；商品图册 → objectName 商品名必填。
     */
    private fun normalizeRecommendItem(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val item = bean.extraString(PayloadFields.RECEIVED_CONTENT)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填且非空（推荐条文案）")
        val extra = bean.extraText ?: bean.extraString(PayloadFields.EXTRA_TEXT_FIELD)
        val product = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
        if (item == "发起收款") {
            val amount = extra?.takeIf { it.isNotBlank() }
                ?: return invalid(bean.type, PayloadFields.EXTRA_TEXT_FIELD, "发起收款必填金额（10/10.5/￥10）")
            if (!AMOUNT_REGEX.matches(amount)) {
                return invalid(bean.type, PayloadFields.EXTRA_TEXT_FIELD, "发起收款金额格式不正确（最多两位小数）")
            }
        }
        if (item == "商品图册" && product == null) {
            return invalid(bean.type, PayloadFields.OBJECT_NAME, "商品图册必填商品名（objectName）")
        }
        return NormalizedIntent.Valid(
            CommandIntent.ClickRecommendItem(
                targets = targets,
                itemName = item,
                amount = extra?.takeIf { it.isNotBlank() },
                productName = product,
            ),
        )
    }

    /** 511：objectName=yyyy-MM-dd（今天或未来 28 天内——日期格式端侧校验，窗口归服务端）。 */
    private fun normalizeMeetingInfo(bean: WeworkMessageBean): NormalizedIntent {
        val date = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填（yyyy-MM-dd 会议日期）")
        if (!DATE_REGEX.matches(date)) {
            return invalid(bean.type, PayloadFields.OBJECT_NAME, "日期格式非法（须 yyyy-MM-dd）")
        }
        return NormalizedIntent.Valid(CommandIntent.GetMeetingInfo(date = date))
    }

    /** 514：objectName（messageId 模式）与 logStartTime/logEndTime（时间段模式）至少其一。 */
    private fun normalizeLogSnippet(bean: WeworkMessageBean): NormalizedIntent {
        val targetId = bean.extraString(PayloadFields.ORIGINAL_CONTENT)
            ?: bean.extraString(PayloadFields.OBJECT_NAME)
        val start = bean.extraString(PayloadFields.LOG_START_TIME)?.takeIf { it.isNotBlank() }
        val end = bean.extraString(PayloadFields.LOG_END_TIME)?.takeIf { it.isNotBlank() }
        if (targetId.isNullOrBlank() && start == null && end == null) {
            return invalid(bean.type, PayloadFields.OBJECT_NAME, "targetMessageId 为空（objectName）或时间范围缺失")
        }
        return NormalizedIntent.Valid(
            CommandIntent.GetLogSnippet(
                targetMessageId = targetId?.takeIf { it.isNotBlank() },
                logStartTime = start,
                logEndTime = end,
            ),
        )
    }

    /** 510：logDate 可选 yyyy-MM-dd（缺省=今天）。 */
    private fun normalizeUploadLog(bean: WeworkMessageBean): NormalizedIntent {
        val date = bean.extraString(PayloadFields.LOG_DATE)?.takeIf { it.isNotBlank() }
        if (date != null && !DATE_REGEX.matches(date)) {
            return invalid(bean.type, PayloadFields.LOG_DATE, "日期格式非法（须 yyyy-MM-dd）")
        }
        return NormalizedIntent.Valid(CommandIntent.UploadLogFile(logDate = date))
    }

    /** 301/302：log 文本可选（缺省=通道尾部快照）。 */
    private fun normalizeRobotLog(
        bean: WeworkMessageBean,
        build: (String?) -> CommandIntent,
    ): NormalizedIntent =
        NormalizedIntent.Valid(build(bean.extraString(PayloadFields.LOG_TEXT)?.takeIf { it.isNotBlank() }))

    /** 305：originalContent（或 messageIds）= messageId 中英逗号分隔批量。 */
    private fun normalizeQueueRemove(bean: WeworkMessageBean): NormalizedIntent {
        val raw = bean.extraString(PayloadFields.ORIGINAL_CONTENT)
            ?: bean.extraString(PayloadFields.MESSAGE_IDS)
            ?: return invalid(bean.type, PayloadFields.ORIGINAL_CONTENT, "必填（待取消 messageId，逗号分隔）")
        val ids = raw.split(',', '，').map { it.trim() }.filter { it.isNotEmpty() }.distinct()
        if (ids.isEmpty()) return invalid(bean.type, PayloadFields.ORIGINAL_CONTENT, "messageId 列表为空")
        return NormalizedIntent.Valid(CommandIntent.RobotQueueRemove(messageIds = ids))
    }

    /** 243：fileUrl 必填（仅支持单个 URL）。 */
    private fun normalizeQrJoin(bean: WeworkMessageBean): NormalizedIntent {
        val url = bean.fileUrl ?: bean.extraString(PayloadFields.FILE_URL_FIELD)
        val urlSafe = url?.takeIf { it.startsWith("http://") || it.startsWith("https://") }
            ?: return invalid(bean.type, PayloadFields.FILE_URL_FIELD, "必填且须 http(s) URL（仅单个）")
        return NormalizedIntent.Valid(CommandIntent.QrCodeJoinGroup(fileUrl = urlSafe))
    }

    /** 210/211：卡片载荷（objectName=标题 / extraText=描述 / fileUrl=跳转 / iconUrl 可选）。 */
    private fun normalizeCard(
        bean: WeworkMessageBean,
        build: (List<String>, Pair<String, String?>, String, String?) -> CommandIntent,
    ): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val title = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且非空（卡片标题）")
        val url = bean.fileUrl ?: bean.extraString(PayloadFields.FILE_URL_FIELD)
        val urlSafe = url?.takeIf { it.startsWith("http://") || it.startsWith("https://") }
            ?: return invalid(bean.type, PayloadFields.FILE_URL_FIELD, "必填且须 http(s) URL（跳转页）")
        return NormalizedIntent.Valid(
            build(
                targets,
                title to bean.extraTextOf(),
                urlSafe,
                bean.extraString(PayloadFields.ICON_URL)?.takeIf { it.isNotBlank() },
            ),
        )
    }

    /** 202：开关字段缺失 = Invalid（fail-closed：开关型指令不允许猜默认态）。 */
    private fun normalizeLoopSwitch(bean: WeworkMessageBean): NormalizedIntent {
        val raw = bean.extraString(PayloadFields.LOOP_ENABLED)
            ?.trim()?.lowercase()
        val enabled = when (raw) {
            "true", "1" -> true
            "false", "0" -> false
            else -> return invalid(bean.type, PayloadFields.LOOP_ENABLED, "必填（true/false）")
        }
        return NormalizedIntent.Valid(CommandIntent.ReceiveLoopSwitch(enabled = enabled))
    }

    // ---- B2 规范化 ----

    private fun normalizeReply(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val sender = bean.extraString(PayloadFields.RECEIVED_NAME)
            ?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.RECEIVED_NAME, "必填且非空")
        val quoted = bean.extraString(PayloadFields.ORIGINAL_CONTENT)
            ?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.ORIGINAL_CONTENT, "必填且非空")
        val content = bean.extraString(PayloadFields.RECEIVED_CONTENT)
            ?.takeIf { !it.isBlank() }
            ?: return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填且非空白")
        return NormalizedIntent.Valid(
            CommandIntent.ReplyMessage(
                targets = targets,
                quotedSender = sender,
                quotedContent = quoted,
                quotedTextType = bean.textType ?: 0,
                content = content,
            ),
        )
    }

    private fun normalizeRelay(bean: WeworkMessageBean): NormalizedIntent {
        val rooms = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (rooms.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val sender = bean.extraString(PayloadFields.RECEIVED_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.RECEIVED_NAME, "必填且非空")
        val quoted = bean.extraString(PayloadFields.ORIGINAL_CONTENT)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.ORIGINAL_CONTENT, "必填且非空")
        val receivers = bean.stringListExtra(PayloadFields.NAME_LIST)
        if (receivers.isEmpty()) return invalid(bean.type, PayloadFields.NAME_LIST, "必填且非空")
        return NormalizedIntent.Valid(
            CommandIntent.RelayMessage(
                sourceRooms = rooms,
                quotedSender = sender,
                quotedContent = quoted,
                quotedTextType = bean.textType ?: 0,
                receivers = receivers,
                extraText = bean.extraTextOf(),
            ),
        )
    }

    private fun normalizeRelayBatch(bean: WeworkMessageBean, merge: Boolean): NormalizedIntent {
        val rooms = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (rooms.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val messages = relayItems(bean)
        if (messages.isEmpty()) return invalid(bean.type, PayloadFields.MESSAGE_LIST, "必填且非空（原文定位键）")
        val receivers = bean.stringListExtra(PayloadFields.NAME_LIST)
        if (receivers.isEmpty()) return invalid(bean.type, PayloadFields.NAME_LIST, "必填且非空")
        val intent = if (merge) {
            CommandIntent.RelayMergeMessage(rooms, messages, receivers, bean.extraTextOf())
        } else {
            CommandIntent.RelayMultiMessage(rooms, messages, receivers, bean.extraTextOf())
        }
        return NormalizedIntent.Valid(intent)
    }

    private fun normalizeSendBatch(bean: WeworkMessageBean, merge: Boolean): NormalizedIntent {
        val messages = outboundMessages(bean)
        if (messages.isEmpty()) return invalid(bean.type, PayloadFields.WEWORK_MESSAGE_LIST, "必填且非空")
        val receivers = bean.stringListExtra(PayloadFields.NAME_LIST)
        if (receivers.isEmpty()) return invalid(bean.type, PayloadFields.NAME_LIST, "必填且非空")
        val intent = if (merge) {
            CommandIntent.SendMergeMessage(messages, receivers, bean.extraTextOf())
        } else {
            CommandIntent.SendMultiMessage(messages, receivers, bean.extraTextOf())
        }
        return NormalizedIntent.Valid(intent)
    }

    /** 238：receivedContent = JSON 数组字符串，≥2 条非空文本（解析失败/不足 = Invalid）。 */
    private fun normalizeSendBatchText(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val raw = bean.extraString(PayloadFields.RECEIVED_CONTENT)
            ?: return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填（JSON 数组字符串）")
        val texts = try {
            kotlinx.serialization.json.Json.parseToJsonElement(raw)
                .let { it as? kotlinx.serialization.json.JsonArray }
                ?.mapNotNull { el ->
                    (el as? kotlinx.serialization.json.JsonPrimitive)?.takeIf { it.isString }?.content
                }
                ?.filter { it.isNotBlank() }
                ?: emptyList()
        } catch (e: kotlinx.serialization.SerializationException) {
            return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "批量发送内容解析失败（非法 JSON）")
        } catch (e: IllegalArgumentException) {
            return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "批量发送内容解析失败（非法 JSON）")
        }
        if (texts.size < 2) {
            return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "批量发送至少需要两条非空文本")
        }
        return NormalizedIntent.Valid(
            CommandIntent.SendBatchText(
                targets = targets,
                texts = texts,
                atList = (bean.atList ?: bean.at ?: emptyList()).filter { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeRecall(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val quoted = bean.extraString(PayloadFields.ORIGINAL_CONTENT)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.ORIGINAL_CONTENT, "必填且非空")
        return NormalizedIntent.Valid(
            CommandIntent.RecallMessage(
                targets = targets,
                originalContent = quoted,
                textType = bean.textType ?: 0,
            ),
        )
    }

    /** 213：phone 必填（添加 schema）；leavingMsg 存在与否 = 添加/修改语义分流键。 */
    private fun normalizeAddFriendByPhone(bean: WeworkMessageBean): NormalizedIntent {
        val friend = bean.friend
            ?: return invalid(bean.type, "friend", "必填（phone 添加语义 / name 修改语义）")
        val phone = friend.phone?.takeIf { it.isNotBlank() }
        val name = friend.name?.takeIf { it.isNotBlank() }
        if (phone == null && name == null) {
            return invalid(bean.type, "friend.phone|friend.name", "至少一项必填")
        }
        if (friend.leavingMsg != null && phone == null) {
            return invalid(bean.type, "friend.phone", "添加语义（含 leavingMsg）必填 phone")
        }
        return NormalizedIntent.Valid(
            CommandIntent.AddFriendByPhone(
                phone = phone ?: name.orEmpty(), // 修改语义以 name 兜底定位键
                name = name,
                markName = friend.markName?.takeIf { it.isNotBlank() },
                markExtra = friend.markExtra?.takeIf { it.isNotBlank() },
                tagList = friend.tagList.orEmpty().filter { it.isNotBlank() },
                leavingMsg = friend.leavingMsg?.takeIf { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeAddFriendByGroup(bean: WeworkMessageBean): NormalizedIntent {
        val group = bean.extraString(PayloadFields.GROUP_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空")
        val friend = bean.friend ?: return invalid(bean.type, "friend", "必填（name 定位群内成员）")
        val member = friend.name?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, "friend.name", "必填且非空")
        return NormalizedIntent.Valid(
            CommandIntent.AddFriendByGroup(
                groupName = group,
                memberName = member,
                markName = friend.markName?.takeIf { it.isNotBlank() },
                markExtra = friend.markExtra?.takeIf { it.isNotBlank() },
                tagList = friend.tagList.orEmpty().filter { it.isNotBlank() },
                leavingMsg = friend.leavingMsg?.takeIf { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeDeleteContact(bean: WeworkMessageBean): NormalizedIntent {
        val name = bean.friend?.name?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, "friend.name", "必填且非空（有备注必须用备注名）")
        return NormalizedIntent.Valid(CommandIntent.DeleteContact(contactName = name))
    }

    /** 236：nameList 优先于 friend.name；去重去空；≤50；双空 = Invalid。 */
    private fun normalizeDeleteSingleDirectional(bean: WeworkMessageBean): NormalizedIntent {
        val fromList = bean.stringListExtra(PayloadFields.NAME_LIST)
        val names = (fromList.ifEmpty {
            bean.friend?.name?.takeIf { it.isNotBlank() }?.let { listOf(it) } ?: emptyList()
        }).filter { it.isNotBlank() }.distinct()
        if (names.isEmpty()) return invalid(bean.type, "nameList|friend.name", "不能同时为空")
        if (names.size > MAX_SINGLE_DIRECTIONAL_BATCH) {
            return invalid(bean.type, PayloadFields.NAME_LIST, "单次上限 $MAX_SINGLE_DIRECTIONAL_BATCH 个")
        }
        return NormalizedIntent.Valid(CommandIntent.DeleteSingleDirectionalContact(names = names))
    }

    private fun normalizePushFile(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val objectName = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且非空")
        val url = bean.fileUrl ?: bean.extraString(PayloadFields.FILE_URL_FIELD)
        val urlSafe = url?.takeIf { it.startsWith("http://") || it.startsWith("https://") }
            ?: return invalid(bean.type, PayloadFields.FILE_URL_FIELD, "必填且须 http(s) URL")
        val fileType = bean.fileType ?: bean.extraString(PayloadFields.FILE_TYPE_FIELD)
            ?: return invalid(bean.type, PayloadFields.FILE_TYPE_FIELD, "必填（image、audio、video 或星号通配）")
        return NormalizedIntent.Valid(
            CommandIntent.PushFile(
                targets = targets,
                objectName = objectName,
                fileUrl = urlSafe,
                fileType = fileType,
                extraText = bean.extraTextOf(),
            ),
        )
    }

    /** 224：自拟 schema（objectName=标题 / extraText=副标题 / fileUrl=跳转 / iconUrl 可选）。 */
    private fun normalizePushLink(bean: WeworkMessageBean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val title = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且非空（链接标题）")
        val description = bean.extraTextOf()?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.EXTRA_TEXT_FIELD, "必填且非空（副标题）")
        val url = bean.fileUrl ?: bean.extraString(PayloadFields.FILE_URL_FIELD)
        val urlSafe = url?.takeIf { it.startsWith("http://") || it.startsWith("https://") }
            ?: return invalid(bean.type, PayloadFields.FILE_URL_FIELD, "必填且须 http(s) URL")
        return NormalizedIntent.Valid(
            CommandIntent.PushLink(
                targets = targets,
                title = title,
                description = description,
                url = urlSafe,
                iconUrl = bean.extraString(PayloadFields.ICON_URL)?.takeIf { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeMicroDisk(bean: WeworkMessageBean, image: Boolean): NormalizedIntent {
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
        if (targets.isEmpty()) return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空")
        val objectName = bean.extraString(PayloadFields.OBJECT_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.OBJECT_NAME, "必填且非空（微盘内已存显示名）")
        val intent = if (image) {
            CommandIntent.PushMicroDiskImage(targets, objectName, bean.extraTextOf())
        } else {
            CommandIntent.PushMicroDiskFile(targets, objectName, bean.extraTextOf())
        }
        return NormalizedIntent.Valid(intent)
    }

    /** 群名单键指令（504）：groupName 必填。 */
    private fun normalizeGroupNameOnly(bean: WeworkMessageBean, build: (String) -> CommandIntent): NormalizedIntent {
        val group = bean.extraString(PayloadFields.GROUP_NAME)?.takeIf { it.isNotBlank() }
            ?: return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空")
        return NormalizedIntent.Valid(build(group))
    }

    // ---- 共用取值 ----

    private fun WeworkMessageBean.extraTextOf(): String? =
        extraText?.takeIf { it.isNotBlank() }
            ?: extraString(PayloadFields.EXTRA_TEXT_FIELD)?.takeIf { it.isNotBlank() }

    /** selectList（501 群名名单，2026-11-04 会话 10 对齐原版）→ 非空字符串列表。 */
    private fun selectList(bean: WeworkMessageBean): List<String> {
        val raw = bean.extras["selectList"] as? kotlinx.serialization.json.JsonArray
            ?: return emptyList()
        return raw.mapNotNull { el ->
            (el as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() }
        }
    }

    /** messageList（227/228）→ RelayItem（原文定位键非空条目）。 */    private fun relayItems(bean: WeworkMessageBean): List<CommandIntent.RelayItem> {
        val raw = bean.extras[PayloadFields.MESSAGE_LIST] as? kotlinx.serialization.json.JsonArray
            ?: return emptyList()
        return raw.mapNotNull { el ->
            val obj = el as? kotlinx.serialization.json.JsonObject ?: return@mapNotNull null
            val content = (obj[PayloadFields.ORIGINAL_CONTENT] as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() } ?: return@mapNotNull null
            val textType = (obj["textType"] as? kotlinx.serialization.json.JsonPrimitive)?.content?.toIntOrNull() ?: 0
            CommandIntent.RelayItem(originalContent = content, textType = textType)
        }
    }

    /** weworkMessageList（229/230）→ OutboundMessage。 */
    private fun outboundMessages(bean: WeworkMessageBean): List<CommandIntent.OutboundMessage> {
        val raw = bean.extras[PayloadFields.WEWORK_MESSAGE_LIST] as? kotlinx.serialization.json.JsonArray
            ?: return emptyList()
        return raw.mapNotNull { el ->
            val obj = el as? kotlinx.serialization.json.JsonObject ?: return@mapNotNull null
            val textType = (obj["textType"] as? kotlinx.serialization.json.JsonPrimitive)?.content?.toIntOrNull() ?: 1
            val content = (obj[PayloadFields.RECEIVED_CONTENT] as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content
            val objectName = (obj[PayloadFields.OBJECT_NAME] as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content
            val fileUrl = (obj[PayloadFields.FILE_URL_FIELD] as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content
            val fileType = (obj[PayloadFields.FILE_TYPE_FIELD] as? kotlinx.serialization.json.JsonPrimitive)
                ?.takeIf { it.isString }?.content
            if (content.isNullOrBlank() && fileUrl.isNullOrBlank()) return@mapNotNull null
            CommandIntent.OutboundMessage(
                textType = textType,
                content = content,
                objectName = objectName,
                fileUrl = fileUrl,
                fileType = fileType,
            )
        }
    }

    private companion object {
        const val MAX_SINGLE_DIRECTIONAL_BATCH = 50

        /** 235 金额格式：10 / 10.5 / ￥10（最多两位小数）。 */
        val AMOUNT_REGEX = Regex("^￥?\\d+(\\.\\d{1,2})?$")

        /** 511/510 日期格式：yyyy-MM-dd。 */
        val DATE_REGEX = Regex("^\\d{4}-\\d{2}-\\d{2}$")
    }

    private fun normalizeSendText(bean: WeworkMessageBean): NormalizedIntent {
        // 203 目标字段冻结（2026-11-05 契约冻结车道）：canonical=titleList
        // （upstream api-23520034「titleList:string[]，建议单目标」= compat/prevalidate
        // 必填表 = 管理台 catalog example 同名）；候选名 targetRoom 降级为迁移期
        // 只读别名（protocol 金标夹具 hmac-vector 仍携候选名，重算金标向量不在本
        // 车道；T3.2 apifox 冒烟后撤别名）。多元素 titleList 显式拒绝不静默截断：
        // 203=单目标语义（上游建议单目标防重试重发），批量走 229/238。
        val targets = bean.stringListExtra(PayloadFields.TITLE_LIST)
            .filter { it.isNotBlank() }
            .ifEmpty { bean.extraString(PayloadFields.TARGET_ROOM)?.takeIf { it.isNotBlank() }?.let { listOf(it) } ?: emptyList() }
        if (targets.isEmpty()) {
            return invalid(bean.type, PayloadFields.TITLE_LIST, "必填且非空（候选别名 targetRoom 迁移期兼容）")
        }
        if (targets.size > 1) {
            return invalid(bean.type, PayloadFields.TITLE_LIST, "203 为单目标语义（上游建议单目标防重试重发），多目标请用 229/238")
        }
        val content = bean.extraString(PayloadFields.RECEIVED_CONTENT)
        if (content.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填且非空")
        }
        return NormalizedIntent.Valid(
            CommandIntent.SendText(
                targetRoom = targets.single(),
                content = content,
                atList = (bean.atList ?: bean.at ?: emptyList()).filter { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeInitGroup(bean: WeworkMessageBean): NormalizedIntent {
        val name = bean.extraString(PayloadFields.GROUP_NAME)
            ?: bean.extraString(PayloadFields.TARGET_ROOM)
        if (name.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空")
        }
        val announcement = bean.extraString(PayloadFields.ANNOUNCEMENT)
            ?: bean.extraText
        return NormalizedIntent.Valid(
            CommandIntent.InitGroup(
                groupName = name,
                // 206 成员名单字段冻结（2026-11-05）：canonical=selectList
                // （upstream initGroup「selectList 添加群成员名称列表」= catalog/prevalidate
                // 同名）。此前读候选名 groupMembers 与目录漂移——目录表单按 selectList
                // 提交被端侧静默忽略（拉人名单丢失），本迁移即修该消费缺口。
                members = bean.stringListExtra(PayloadFields.SELECT_LIST),
                announcement = announcement?.takeIf { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeUpdateGroup(bean: WeworkMessageBean): NormalizedIntent {
        // 207 目标字段冻结（2026-11-05）：canonical=groupName（upstream updateGroup
        // 「groupName 待修改的群」= catalog/其余群指令执行器同名；候选名 targetRoom
        // 降级为迁移期只读别名，与 206 同型）。
        val target = bean.extraString(PayloadFields.GROUP_NAME)
            ?: bean.extraString(PayloadFields.TARGET_ROOM)
        if (target.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空（候选别名 targetRoom 迁移期兼容）")
        }
        val announcement = bean.extraString(PayloadFields.ANNOUNCEMENT)?.takeIf { it.isNotBlank() }
        val remark = bean.extraString(PayloadFields.GROUP_REMARK)?.takeIf { it.isNotBlank() }
        val addMembers = bean.stringListExtra(PayloadFields.ADD_MEMBERS).filter { it.isNotBlank() }
        if (announcement == null && remark == null && addMembers.isEmpty()) {
            return invalid(bean.type, "announcement|groupRemark|addMembers", "至少一项变更")
        }
        return NormalizedIntent.Valid(
            CommandIntent.UpdateGroup(
                targetGroup = target,
                announcement = announcement,
                groupRemark = remark,
                addMembers = addMembers,
            ),
        )
    }

    private fun normalizeModifyMember(bean: WeworkMessageBean): NormalizedIntent {
        // 225 目标字段冻结（2026-11-05）：canonical=groupName（upstream
        // modifyGroupMemberInfo「groupName 外部群」= catalog 同名；候选名 targetRoom
        // 降级为迁移期只读别名，与 206/207 同型）。
        val target = bean.extraString(PayloadFields.GROUP_NAME)
            ?: bean.extraString(PayloadFields.TARGET_ROOM)
        if (target.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.GROUP_NAME, "必填且非空（候选别名 targetRoom 迁移期兼容）")
        }
        val member = bean.extraString(PayloadFields.MEMBER_NAME)
            ?: bean.friend?.name
        if (member.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.MEMBER_NAME, "必填且非空（或 friend.name）")
        }
        val markName = bean.extraString(PayloadFields.MARK_NAME) ?: bean.friend?.markName
        val tags = bean.stringListExtra(PayloadFields.TAG_LIST)
            .ifEmpty { bean.friend?.tagList ?: emptyList() }
        if (markName.isNullOrBlank() && tags.isEmpty()) {
            return invalid(bean.type, "markName|tagList", "至少一项变更（或 friend.markName/tagList）")
        }
        return NormalizedIntent.Valid(
            CommandIntent.ModifyGroupMemberInfo(
                targetGroup = target,
                memberName = member,
                markName = markName?.takeIf { it.isNotBlank() },
                tagList = tags.filter { it.isNotBlank() },
            ),
        )
    }

    /** 509：路径可选（空缺 = 默认当前日志，语义在执行器经 LocalFilePort 解析）。 */
    private fun normalizeGetLocalFile(bean: WeworkMessageBean): NormalizedIntent {
        val path = bean.extraString(PayloadFields.FILE_PATH)?.trim()
        return NormalizedIntent.Valid(
            CommandIntent.GetLocalFile(filePath = path?.takeIf { it.isNotBlank() }),
        )
    }

    private fun invalid(type: Int, field: String, reason: String) =
        NormalizedIntent.Invalid(type, field, reason)

    private fun WeworkMessageBean.stringListExtra(name: String): List<String> {
        val raw = extras[name] as? kotlinx.serialization.json.JsonArray ?: return emptyList()
        return raw.mapNotNull { el ->
            (el as? kotlinx.serialization.json.JsonPrimitive)?.takeIf { it.isString }?.content
        }
    }
}

/** 规范化结果。 */
sealed class NormalizedIntent {
    data class Valid(val intent: CommandIntent) : NormalizedIntent()

    /** type 不在 P0 支持集（执行器未注册）。 */
    data class Unsupported(val commandType: Int) : NormalizedIntent()

    /** type 已支持但载荷不合法（必填缺失/空值/无变更项）。 */
    data class Invalid(
        val commandType: Int,
        val field: String,
        val reason: String,
    ) : NormalizedIntent()
}
