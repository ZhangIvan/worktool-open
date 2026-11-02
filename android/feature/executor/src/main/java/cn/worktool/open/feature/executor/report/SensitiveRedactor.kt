/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.report

/**
 * SR-06 日志脱敏工具（FR-01 消息上报链路强制使用）。
 *
 * 口径（docs/01 SR-06 行原文）：消息正文 / 手机号 / token 默认不落日志——
 * - 正文 → 只保留长度元数据（[preview]），任何日志/失败原因不得回显原文；
 * - 手机号 → 11 位大陆号段整体遮蔽（[maskPhones]）；连续 ≥7 位数字（可能含
 *   座机/证件号片段）统一按 [maskDigitRuns] 兜底遮蔽；
 * - token 类十六进制长串（≥32 位）→ 遮蔽中段。
 *
 * 注意：**上报载荷本身不受限**——FR-01 的目的就是把消息送抵服务端（线上通道
 * 加密归 protocol v2 会话层）；本对象只约束端侧日志面（logcat/审计留痕/失败原因）。
 */
object SensitiveRedactor {

    /** 大陆手机号（1[3-9] 开头 11 位）。 */
    private val PHONE = Regex("""1[3-9]\d{9}""")

    /** 连续 ≥7 位数字（座机/证件片段兜底）。 */
    private val DIGIT_RUN = Regex("""\d{7,}""")

    /** 十六进制长串（≥32 位，token/密钥特征）。 */
    private val HEX_TOKEN = Regex("""[0-9a-fA-F]{32,}""")

    /**
     * 正文安全预览：仅长度元数据，永不回显原文。
     * 例：`"(len=42)"`。
     */
    fun preview(text: String?): String = "(len=${text?.length ?: 0})"

    /** 手机号遮蔽：保留前 3 后 2（审计可区分、不可还原）。 */
    fun maskPhones(text: String): String =
        text.replace(PHONE) { it.value.take(3) + "****" + it.value.takeLast(2) }

    /** 数字长串遮蔽：保留首 2 末 1。 */
    fun maskDigitRuns(text: String): String = maskPhones(text)
        .replace(DIGIT_RUN) { it.value.take(2) + "*" + it.value.takeLast(1) }

    /** token 长串遮蔽：保留首 6 末 4。 */
    fun maskTokens(text: String): String =
        text.replace(HEX_TOKEN) { it.value.take(6) + "…" + it.value.takeLast(4) }

    /** 组合脱敏（日志面统一入口）。 */
    fun sanitize(text: String): String = maskTokens(maskDigitRuns(text))

    /**
     * 事件日志行（FR-01 上报链路专用）：正文只出 [preview]，会话/发送人过
     * [sanitize]（名称可能含手机号备注，统一遮蔽）。
     */
    fun eventLogLine(
        what: String,
        targetRoom: String,
        content: String,
        senderCount: Int,
    ): String = "$what room=${sanitize(targetRoom)} content=${preview(content)} senders=$senderCount"
}
