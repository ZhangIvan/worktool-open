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
        else -> NormalizedIntent.Unsupported(bean.type)
    }

    private fun normalizeSendText(bean: WeworkMessageBean): NormalizedIntent {
        val target = bean.extraString(PayloadFields.TARGET_ROOM)
        if (target.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.TARGET_ROOM, "必填且非空")
        }
        val content = bean.extraString(PayloadFields.RECEIVED_CONTENT)
        if (content.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.RECEIVED_CONTENT, "必填且非空")
        }
        return NormalizedIntent.Valid(
            CommandIntent.SendText(
                targetRoom = target,
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
                members = bean.stringListExtra(PayloadFields.GROUP_MEMBERS),
                announcement = announcement?.takeIf { it.isNotBlank() },
            ),
        )
    }

    private fun normalizeUpdateGroup(bean: WeworkMessageBean): NormalizedIntent {
        val target = bean.extraString(PayloadFields.TARGET_ROOM)
        if (target.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.TARGET_ROOM, "必填且非空")
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
        val target = bean.extraString(PayloadFields.TARGET_ROOM)
        if (target.isNullOrBlank()) {
            return invalid(bean.type, PayloadFields.TARGET_ROOM, "必填且非空")
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
