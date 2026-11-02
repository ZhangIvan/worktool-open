/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.content.Context
import cn.worktool.open.core.accessibility.rules.RuleConfig
import cn.worktool.open.core.accessibility.rules.RuleConfigException
import cn.worktool.open.core.accessibility.rules.RuleConfigParser

/**
 * 规则配置装载（T2.4）：assets → [RuleConfig]。
 *
 * 约定：优先读 [DEFAULT_FILE]（运维/后续版本可随包分发实际规则），
 * 缺失时回落内置样例 [SAMPLE_FILE]（resourceId 均为占位，需 D1 真机校准）。
 * 读取失败/解析失败统一抛 [RuleConfigException]（含原因），不静默吞错。
 */
object RuleConfigLoader {

    /** 实际生效配置（当前版本随包内置；远端下发在 T3.x 评审后接入）。 */
    const val DEFAULT_FILE = "accessibility_rules.json"

    /** 内置样例（演示格式 + WorkTool 自身可试掷的规则）。 */
    const val SAMPLE_FILE = "accessibility_rules.sample.json"

    fun fromAssets(context: Context, fileName: String): RuleConfig {
        val text = try {
            context.assets.open(fileName).use { it.readBytes().toString(Charsets.UTF_8) }
        } catch (e: Exception) {
            throw RuleConfigException("读取 assets/$fileName 失败: ${e.message}", e)
        }
        return RuleConfigParser.parse(text)
    }

    /** 读默认配置，缺失则回落样例。 */
    fun fromAssetsOrDefault(context: Context): RuleConfig =
        try {
            fromAssets(context, DEFAULT_FILE)
        } catch (e: RuleConfigException) {
            fromAssets(context, SAMPLE_FILE)
        }
}
