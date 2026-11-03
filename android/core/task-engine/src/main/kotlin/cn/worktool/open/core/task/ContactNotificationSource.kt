/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 通讯录通知事件源（FR-15 增强：通讯录通知事件 → 过滤 → 通过/拒绝）。
 *
 * 链路：系统通知（AccessibilityEvent/notification listener 快照）→ [parse]
 * **过滤**（非好友请求通知一律丢弃返回 null）→ [FriendRequestEvent] →
 * [FriendAutoAcceptHook.decide]（通过/拒绝决策）。
 *
 * 过滤原则（fail-closed，对齐 FR-56 G3「文案不作身份证据」）：
 * - **包名门先行**：通知来源包必须是已登记的通讯录应用（默认企微/微信双端），包名
 *   不可得=丢弃——文案形态再像好友请求也不认；
 * - 文案形态只是**第二道**启发式（「xx 请求添加你为好友/朋友」族），默认模式集可配置
 *   （通知文案随版本漂移，改配置不改代码）；提取失败（无好友名）= 丢弃。
 */
data class ContactNotification(
    /** 通知来源应用包名（如 com.tencent.mm / com.tencent.wework）；null=不可得。 */
    val packageName: String? = null,
    val title: String? = null,
    val text: String? = null,
    val postedAtMillis: Long,
)

/**
 * 好友请求通知解析器（纯函数；JVM 可测）。
 *
 * 默认模式（通知面文案假设，非控件资源名；版本敏感，经构造参数替换不改代码）：
 * - `xx 请求添加你为好友`（微信系）
 * - `xx 请求添加你为朋友`
 * - `xx 请求添加好友`（企微变体）
 */
class FriendRequestNotificationParser(
    /** 通讯录应用包名白名单（身份证据唯一来源；空集=全拒）。 */
    private val contactPackageNames: Set<String> = DEFAULT_CONTACT_PACKAGES,
    /** 好友请求文案模式（reluctant 捕获组 1=好友名）。 */
    private val requestPatterns: List<Regex> = DEFAULT_REQUEST_PATTERNS,
    /** 打招呼前缀标记（其后文本=打招呼内容；找不到=null）。 */
    private val leavingMsgMarkers: List<String> = DEFAULT_LEAVING_MSG_MARKERS,
) {

    /**
     * 解析通知 → 好友请求事件；**非好友请求通知（包名不符/文案不匹配/无好友名）返回 null**。
     */
    fun parse(notification: ContactNotification): FriendRequestEvent? {
        // 包名门：身份证据只看包名（文案不作身份证据，FR-56 G3 同源原则）
        notification.packageName?.takeIf { it in contactPackageNames } ?: return null
        val text = notification.text ?: notification.title ?: return null

        val pattern = requestPatterns.firstOrNull { it.containsMatchIn(text) } ?: return null
        val name = pattern.find(text)?.groupValues?.getOrNull(1)?.trim()?.takeIf { it.isNotEmpty() }
            ?: return null

        return FriendRequestEvent(
            friendName = name,
            leavingMsg = extractLeavingMsg(text),
            eventTimeMillis = notification.postedAtMillis,
        )
    }

    /** 打招呼内容提取：首个标记（「留言」/「备注」等）冒号后的文本；无标记=null。 */
    private fun extractLeavingMsg(text: String): String? {
        for (marker in leavingMsgMarkers) {
            val idx = text.indexOf(marker)
            if (idx >= 0) {
                val rest = text.substring(idx + marker.length).trimStart(' ', '：', ':', '　')
                if (rest.isNotEmpty()) return rest
            }
        }
        return null
    }

    companion object {
        /** 通讯录应用包名白名单（双端，FR-56）。 */
        val DEFAULT_CONTACT_PACKAGES: Set<String> = setOf(
            "com.tencent.wework",
            "com.tencent.mm",
        )

        /** 好友请求文案模式（reluctant 组 1=好友名；文案漂移经配置替换）。 */
        val DEFAULT_REQUEST_PATTERNS: List<Regex> = listOf(
            Regex("^(.+?)\\s*请求添加你为好友"),
            Regex("^(.+?)\\s*请求添加你为朋友"),
            Regex("^(.+?)\\s*请求添加好友"),
        )

        /** 打招呼内容前缀标记（微信系「留言」、企微变体「备注」）。 */
        val DEFAULT_LEAVING_MSG_MARKERS: List<String> = listOf("留言", "备注")
    }
}
