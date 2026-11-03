/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import cn.worktool.open.core.accessibility.rules.SelectorEngine
import cn.worktool.open.core.accessibility.rules.StrictMatch
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 运行期分层门禁（FR-56，设计 §1.4 G3~G6）：
 *
 * - **G4 运行期 app 门**：执行前断言前台窗口包名 == pack.packageName；不符（含**包名不可得**）
 *   → [PackAppMismatchException] 拒执行。**任何一层都不允许跨包回退/重试**——微信选择器
 *   在企微上下文运行=主动拒绝返回错误，绝不试掷、绝不猜测性点击（FR-56 验收：
 *   「WeChat 规则误配到企微会话=fail-closed 拒执行」）。
 * - **G5 屏幕断言门**：导航步骤落地后用 `screens[].assertSelectors` 验屏；不中 →
 *   NODE_NOT_FOUND 语义（防走错屏继续误操作）。
 * - **G6 开关门**：`riskConfirm.required=true` 的包未走完 R7 确认流 → [PackDisabledException]
 *   （101002 语义，附原因「未开启」）。
 * - **G3 文案不信任原则**：「通讯录」「发送」「接受」等文案两 App 同名——文案**永远不作为
 *   App 身份证据**，身份只看 G4 包名门（这正是 G4 先于一切匹配执行的原因）。
 */
object RulePackGate {

    /**
     * G4 预留错误码 APP_MISMATCH。
     *
     * ⚠️ 设计稿（local-private/design/wechat-rulepack.md §1.4 G4）预留 201005，但该值已被
     * feature:executor `ExecutorErrorCodes.LOCAL_FILE_MISSING` 占用（201001-201006、
     * 201008-201009 均已分配，201007 为该段唯一空闲值），故本实现取 **201007**——
     * 设计稿需同步勘误；feature:executor 车道登记同名常量
     * （本模块零依赖 core:protocol，不能反向引用）。
     */
    const val ERROR_APP_MISMATCH: Int = 201007

    /** 多命中拒绝（T4.4「不默认取首个敏感目标」）预留码（201010 空闲；T3.2 回执冻结终值）。 */
    const val ERROR_SELECTOR_AMBIGUOUS: Int = 201010

    /** G6 关关门错误码（对齐 ExecutorErrorCodes.COMMAND_UNSUPPORTED=101002 语义）。 */
    const val ERROR_PACK_DISABLED: Int = 101002

    /** 目标节点未在当前界面找到（对齐 ExecutorErrorCodes.NODE_NOT_FOUND=201001 语义，G5）。 */
    const val ERROR_NODE_NOT_FOUND: Int = 201001

    /**
     * 运行期上下文：当前前台窗口包名（AccessibilityService root 包名或 rootInActiveWindow
     * 的 packageName）。null = 不可得——**按不符处理**（fail-closed，设计 §1.4 G4）。
     */
    data class Context(val foregroundPackageName: String?)

    /** G4：app 门断言（不符/不可得即抛 [PackAppMismatchException]）。 */
    fun assertApp(pack: RulePack, context: Context) {
        val expected = pack.manifest.packageName
        val actual = context.foregroundPackageName
        if (actual != expected) {
            throw PackAppMismatchException(
                packId = pack.manifest.packId,
                expectedPackage = expected,
                actualPackage = actual,
            )
        }
    }

    /** G6：开关门断言（`enabled`=确认流持久标志判定结果，事实源在 [RiskConfirmStateMachine]）。 */
    fun assertEnabled(pack: RulePack, enabled: Boolean) {
        if (pack.manifest.riskConfirm.required && !enabled) {
            throw PackDisabledException(pack.manifest.packId)
        }
    }

    /**
     * 门序固定的单目标解析：G4 app 门 → G6 开关门 → 严格解析（[SelectorEngine.resolveStrict]，
     * 多命中/零匹配/child0 兜底全部拒绝）。
     *
     * 门序刻意「先门禁后匹配」：企微树里存在与微信选择器同文案（如「通讯录」）的节点时，
     * app 门先失败——不试掷、不给误匹配留任何机会（G3 文案不信任原则的执行序保障）。
     */
    fun resolveSingle(
        pack: RulePack,
        context: Context,
        enabled: Boolean,
        root: UiNode,
        selectorId: String,
    ): UiNode {
        assertApp(pack, context)
        assertEnabled(pack, enabled)
        val def = pack.selector(selectorId)
            ?: throw RulePackSelectorException(
                errorCode = ERROR_NODE_NOT_FOUND,
                message = "pack '${pack.manifest.packId}' 不存在选择器 id='$selectorId'",
            )
        return when (val r = SelectorEngine().resolveStrict(root, def)) {
            is StrictMatch.Hit -> r.node
            StrictMatch.NoMatch -> throw RulePackSelectorException(
                errorCode = ERROR_NODE_NOT_FOUND,
                message = "pack '${pack.manifest.packId}' 选择器 '$selectorId' 零命中（拒绝操作，不回退猜测目标）",
            )
            is StrictMatch.Ambiguous -> throw RulePackSelectorException(
                errorCode = ERROR_SELECTOR_AMBIGUOUS,
                message = "pack '${pack.manifest.packId}' 选择器 '$selectorId' 多命中 ${r.matchCount} 个不同目标" +
                    "（默认拒绝，不取首个敏感目标）",
            )
            is StrictMatch.Invalid -> throw RulePackSelectorException(
                errorCode = ERROR_SELECTOR_AMBIGUOUS,
                message = "pack '${pack.manifest.packId}' 选择器 '$selectorId' 定义非法: ${r.reason}",
            )
        }
    }

    /**
     * G5：屏幕身份断言（全部 assertSelectors 严格命中才算在该屏；任一失败 →
     * [RulePackSelectorException]，201001 语义——防走错屏继续误操作）。
     */
    fun assertScreen(
        pack: RulePack,
        context: Context,
        enabled: Boolean,
        root: UiNode,
        screenId: String,
    ) {
        val screen = pack.screen(screenId)
            ?: throw RulePackSelectorException(
                errorCode = ERROR_NODE_NOT_FOUND,
                message = "pack '${pack.manifest.packId}' 不存在屏幕 id='$screenId'",
            )
        screen.assertSelectors.forEach { sid ->
            resolveSingle(pack, context, enabled, root, sid)
        }
    }
}

/** G4 失败：前台包名与包绑定不符（或不可得）。跨包执行的最小闭环拒绝载体。 */
class PackAppMismatchException(
    val packId: String,
    val expectedPackage: String,
    val actualPackage: String?,
) : RulePackException(
    "APP_MISMATCH(${RulePackGate.ERROR_APP_MISMATCH})：pack '$packId' 绑定 $expectedPackage，" +
        "前台包名为 ${actualPackage ?: "不可得"}——拒执行，不跨包回退/重试（FR-56 G4）",
)

/** G6 失败：包默认关闭且未走完 R7 风险确认流。 */
class PackDisabledException(val packId: String) : RulePackException(
    "COMMAND_UNSUPPORTED(${RulePackGate.ERROR_PACK_DISABLED})：pack '$packId' 未开启" +
        "（R7 风险确认流未完成，fail-closed 拒执行）",
)

/** 运行期选择器拒绝（零命中/多命中/定义非法/引用缺失；errorCode 供回执映射）。 */
class RulePackSelectorException(
    val errorCode: Int,
    message: String,
) : RulePackException(message)
