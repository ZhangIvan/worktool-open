/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import cn.worktool.open.core.accessibility.rules.RuleConfig

/** 规则包层错误（装载门 G1/G2 lint、运行期门 G4~G6 拒绝共用基类）。 */
open class RulePackException(message: String, cause: Throwable? = null) : Exception(message, cause)

/** 规则包目标应用（FR-56：同引擎双端，按应用分形规则包）。 */
enum class PackApp {
    /** 企业微信（com.tencent.wework）。 */
    WEWORK,

    /** 个人微信（com.tencent.mm，FR-56 自研无厂商参照，R7 默认关闭）。 */
    WECHAT,
}

/** 屏幕身份断言表条目（G5：每屏 ≥1 个「该屏独有」选择器）。 */
data class PackScreen(
    val id: String,
    /** 全部命中才算通过该屏断言（AND 语义：多锚点=更强身份证据）。 */
    val assertSelectors: List<String>,
)

/** R7 风险确认策略（FR-56/R7：默认关闭 + 显式开启 + 风险告知确认流）。 */
data class RiskConfirmPolicy(
    /** true = 未走完 [RiskConfirmStateMachine] 确认流前所有指令 fail-closed（G6）。 */
    val required: Boolean,
    /** 政策出处引用（审计留痕，如「R7 + FR-56（docs/01 L104；docs/00 §7 R7）」）。 */
    val policyRef: String? = null,
    /** pack 选择器 major bump 后是否强制重走确认流。 */
    val reconfirmOnMajorBump: Boolean = false,
)

/**
 * 包清单（`pack.json`，schemaVersion=1；独立于 RuleConfig v1 选择器文件——
 * 设计 local-private/design/wechat-rulepack.md §1.1：不扩展现有 selectors JSON 字段集，
 * 包绑定/屏幕清单/风险开关等新概念全部落在 pack 层，不触碰 core:accessibility 选择器解析器）。
 */
data class RulePackManifest(
    val schemaVersion: Int,
    val packId: String,
    val app: PackApp,
    /** 绑定前台应用包名（G4 运行期 app 门的期望值；如 com.tencent.mm）。 */
    val packageName: String,
    /** 选择器 ID 命名空间前缀（如 `wechat.`；G1 校验包内全部选择器 ID 与之一致）。 */
    val selectorNamespace: String,
    val selectorsFile: String,
    /** 选择器文件格式；当前唯一合法值 `ruleconfig-v1`。 */
    val selectorsFormat: String,
    /** 默认开关（FR-56/R7：微信包必须 false；G6 的判定事实源是本地确认流持久标志，非本字段）。 */
    val defaultEnabled: Boolean,
    val riskConfirm: RiskConfirmPolicy = RiskConfirmPolicy(required = false),
    val screens: List<PackScreen> = emptyList(),
    /** 本包支持的协议指令 type（企微专属指令不在此列）。 */
    val commands: List<Int> = emptyList(),
    /** 本应用侧 N/A 指令（FR-56：221/222/223/208/209/210 微信侧 N/A）。 */
    val naCommands: List<Int> = emptyList(),
    /** 真机校准记录（W-M5R 后回填，如「2026-11-xx/D1/微信 8.x」；未校准=null）。 */
    val calibratedOn: String? = null,
)

/** 已装载规则包 = 清单 + 解析后的选择器集（G1 全过后的产物）。 */
data class RulePack(
    val manifest: RulePackManifest,
    val selectors: RuleConfig,
) {
    fun selector(id: String) = selectors.selector(id)

    fun screen(id: String): PackScreen? = manifest.screens.firstOrNull { it.id == id }

    fun supportsCommand(commandType: Int): Boolean = commandType in manifest.commands

    fun naForCommand(commandType: Int): Boolean = commandType in manifest.naCommands
}
