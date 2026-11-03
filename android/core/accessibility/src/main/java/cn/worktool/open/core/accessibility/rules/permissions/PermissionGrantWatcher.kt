/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.permissions

import cn.worktool.open.core.accessibility.event.AccessibilityEventSink
import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.core.accessibility.ops.AtomicOperations
import cn.worktool.open.core.accessibility.ops.OpResult
import cn.worktool.open.core.accessibility.rules.pack.WeComRulePack
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * FR-57 企微权限弹窗自动授权 watcher（复用 [AccessibilityEventSink] 快照模式）。
 *
 * 识别与授权流（docs/01 FR-57；证据 apk-analysis/jadx-bad2/sources/j2/m0.java:383-560，
 * 词表与校准点见 [PermissionDialogLexicon]）：
 * - 事件门：`TYPE_WINDOW_STATE_CHANGED`（=32）+ 包名 ∈ [PermissionGrantConfig.watchPackages]
 *   （默认仅企微）+ 窗口语料命中 [PermissionDialogType] 特征词 +「允许」且不含否定词；
 * - 三级授权流（相邻动作间隔 ≥[PermissionGrantConfig.paceMinMillis] 拟人抖动，docs/12 铁律 4）：
 *   点「使用应用时允许」→ 若出现「仅在使用中允许」点之 → 若有「允许访问全部」点之；
 *   分屏形态由翻页事件逐级推进，同屏合并形态按级别序逐个点击（各标签至多一次），
 *   已点标签仍在屏 = 点击未生效信号；
 * - fail-safe（docs/12 铁律 2「两次失败=终局停止」）：连续
 *   [PermissionGrantConfig.maxConsecutiveFailures] 次无进展（按钮缺失/ACTION 点击失败/
 *   点击未生效）→ 放弃该弹窗记 MANUAL_REQUIRED，并按类型抑制至弹窗签名消失后重臂；
 *   授权超时（[PermissionGrantConfig.grantTimeoutMillis]）同样 MANUAL_REQUIRED。
 *   **点击未生效覆盖两层**：严格层按「已点标签仍在屏」检测；模糊层（无标签、不进
 *   [clickedLabels]）按「同一身份键目标点击成功后下一节拍仍在屏」检测（FR57-R1：
 *   弱文本误触发时绝不对同一目标连点，两连败即放弃——否则唯一刹车只剩 15s 超时，
 *   期间可对同目标真实连点 12~18 次）；
 * - ACTION 点击失败由 [AtomicOperations] 手势补点兜底（docs/01 FR-57「手势补点」；
 *   m0.java:536-538 同构）；文本节点→可点目标向上爬层（m0.java:457-467,519-534）；
 * - 计数与审计（SR-06）：只记类型/级别/结果码，**不外发弹窗正文与窗口文本**。
 *
 * 线程契约：单线程驱动（真机 = 主线程 Handler，见 `android/HandlerGrantScheduler`；
 * JVM 单测 = 测试线程顺序 pump）。无内部锁。
 *
 * @param rootsProvider 窗口根节点快照（多窗口都扫；真机经 `WorkToolAccessibilityService.interactiveRoots`）
 * @param operations    原子操作库（click 内含手势回退）
 * @param scheduler     延迟执行端口（拟人间隔与事件去抖都经它）
 * @param jitter        抖动毫秒生成器（取值钳到 [0, paceJitterMillis]；默认 0 便于确定性测试）
 * @param audit         审计端口（稳定结果码行；调用方落日志时保证整体脱敏）
 * @param clock         毫秒时钟（超时判定；测试注入假钟）
 */
class PermissionGrantWatcher(
    private val config: PermissionGrantConfig = PermissionGrantConfig(),
    private val rootsProvider: () -> List<UiNode>,
    private val operations: AtomicOperations,
    private val scheduler: GrantScheduler,
    private val jitter: () -> Long = { 0L },
    private val audit: (String) -> Unit = {},
    private val clock: () -> Long = System::currentTimeMillis,
) : AccessibilityEventSink {

    /** 延迟执行端口（拟人间隔 ≥800ms 的载体；docs/12 铁律 4）。 */
    fun interface GrantScheduler {
        fun schedule(delayMillis: Long, task: Runnable)
    }

    /** 每类弹窗累计计数（SR-06 口径：仅类型与结果计数，无正文）。 */
    data class TypeStats(
        val attempts: Int = 0,
        val granted: Int = 0,
        val manualRequired: Int = 0,
        val cancelled: Int = 0,
        val skippedDisabled: Int = 0,
        val clicksOk: Int = 0,
        val clicksFailed: Int = 0,
    )

    private enum class Phase { IDLE, GRANTING }

    /** 无进展失败种类（审计码；docs/12 铁律 2 的计数对象）。 */
    private enum class FailureKind { MISSING_BUTTON, CLICK_FAILED, INEFFECTIVE_CLICK }

    private enum class GrantResult { GRANTED, MANUAL_REQUIRED, CANCELLED }

    private class Counter {
        var attempts = 0
        var granted = 0
        var manualRequired = 0
        var cancelled = 0
        var skippedDisabled = 0
        var clicksOk = 0
        var clicksFailed = 0

        fun toStats() = TypeStats(
            attempts, granted, manualRequired, cancelled, skippedDisabled, clicksOk, clicksFailed,
        )
    }

    private val stats: Map<PermissionDialogType, Counter> =
        PermissionDialogType.entries.associateWith { Counter() }

    private var phase = Phase.IDLE
    private var activeType: PermissionDialogType? = null
    private val clickedLabels = LinkedHashSet<String>()

    /**
     * 上一次**点击成功**的模糊层目标稳定身份键（[UiNode.identityKey]；FR57-R1）。
     * 模糊层目标 label=null 不进 [clickedLabels]，未生效检测须另记账：下一节拍
     * `fuzzyCandidates.firstOrNull()` 命中同一身份键且签名未消失 = 上次模糊点击
     * 未生效 → 按 INEFFECTIVE_CLICK 计失败。严格/一次性层点击成功即作废（翻页=进展）。
     */
    private var lastFuzzyIdentity: String? = null
    private var consecutiveFailures = 0
    private var deadlineMillis = 0L
    private var evaluationPending = false

    /** MANUAL_REQUIRED 后按类型抑制，直到窗口语料不再命中该类弹窗签名（重臂）。 */
    private val suppressed = HashSet<PermissionDialogType>()

    // ---- 对外观测（调试页/测试；不暴露任何窗口文本） ----

    val granting: Boolean get() = phase == Phase.GRANTING

    val grantingType: PermissionDialogType? get() = activeType

    val suppressedTypes: Set<PermissionDialogType> get() = suppressed.toSet()

    fun statsSnapshot(): Map<PermissionDialogType, TypeStats> = stats.mapValues { it.value.toStats() }

    // ---- 事件入口（事件门：包名 + 窗口状态变化） ----

    override fun onAccessibilityEvent(event: AccessibilityEventSnapshot) {
        val pkg = event.packageName ?: return
        if (pkg !in config.watchPackages) return
        if (phase == Phase.GRANTING) {
            // 授权中：窗口状态/内容变化都可能意味着弹窗翻页或消失——去抖后评估。
            if (event.eventType == TYPE_WINDOW_STATE_CHANGED ||
                (config.progressOnContentChanged && event.eventType == TYPE_WINDOW_CONTENT_CHANGED)
            ) {
                requestEvaluation()
            }
            return
        }
        if (!config.masterEnabled) return
        if (event.eventType != TYPE_WINDOW_STATE_CHANGED) return
        // 入口评估延一拍执行（快照已拷贝文本，不滞留框架事件对象）。
        scheduler.schedule(0L) { evaluateEntry(event.texts) }
    }

    // ---- 入口：分类 + 类型开关 + 抑制重臂 ----

    private fun evaluateEntry(eventTexts: List<String>) {
        if (phase != Phase.IDLE || !config.masterEnabled) return
        val scan = scanWindows()
        val corpus = Corpus(eventTexts + scan.texts)
        // 抑制重臂先于分类：签名已消失的抑制类型解除（人工已处置或弹窗被关闭）。
        val stale = suppressed.filter { !corpus.matchesSignature(it) }
        if (stale.isNotEmpty()) {
            suppressed.removeAll(stale.toSet())
            stale.forEach { audit("perm_grant:REARM type=${it.code}") }
        }
        val type = classify(corpus) ?: return
        if (type in suppressed) return // 仍在抑制期（签名还在，等人工处置）
        if (config.perTypeEnabled[type] != true) {
            stats.getValue(type).skippedDisabled++
            audit("perm_grant:SKIP type=${type.code} reason=TYPE_DISABLED")
            return
        }
        enterGranting(type)
        requestEvaluation()
    }

    private fun enterGranting(type: PermissionDialogType) {
        phase = Phase.GRANTING
        activeType = type
        clickedLabels.clear()
        lastFuzzyIdentity = null
        consecutiveFailures = 0
        deadlineMillis = clock() + config.grantTimeoutMillis
        stats.getValue(type).attempts++
        audit("perm_grant:ENTRY type=${type.code}")
    }

    // ---- 授权中：评估节拍（docs/12 拟人：相邻动作间隔 ≥paceMin + 抖动） ----

    private fun requestEvaluation() {
        if (evaluationPending) return
        evaluationPending = true
        scheduler.schedule(pace()) {
            evaluationPending = false
            if (phase == Phase.GRANTING) evaluate()
        }
    }

    private fun pace(): Long = config.paceMinMillis + jitter().coerceIn(0L, config.paceJitterMillis)

    private fun evaluate() {
        val type = activeType ?: return
        if (clock() > deadlineMillis) {
            audit("perm_grant:OBSERVE type=${type.code} kind=TIMEOUT failures=$consecutiveFailures")
            finish(type, GrantResult.MANUAL_REQUIRED, reason = "TIMEOUT")
            return
        }
        val scan = scanWindows()

        // 1) 严格层：按级别序找「未点过」标签的可点目标（三级流主路径）。
        val strictHit = findStrictTarget(scan)
        if (strictHit != null) {
            performClick(type, strictHit)
            return
        }

        // 2) 可选系统一次性层（默认关；[CAL_PKG] 真机校准点）。
        if (config.acceptSystemOneShot) {
            val oneShot = PermissionDialogLexicon.SYSTEM_ONE_SHOT
                .firstNotNullOfOrNull { scan.labelTargets[it] }
            if (oneShot != null) {
                performClick(type, ClickTarget(oneShot.label, oneShot.target, "ONE_SHOT", oneShot.levelCode))
                return
            }
        }

        // 3) 已点标签仍在屏且签名仍在 = 点击未生效（m0.java:544-560「ACTION_CLICK 后仍存在」同构）。
        if (scan.hasAnyLabel(clickedLabels)) {
            registerFailure(FailureKind.INEFFECTIVE_CLICK)
            return
        }

        // 4) 弹窗签名消失 = 已翻页完成/弹窗关闭 → 授权结束（有过成功点击=成功；否则=取消）。
        val corpus = Corpus(scan.texts)
        if (!corpus.matchesSignature(type)) {
            val noProgress = clickedLabels.isEmpty() && stats.getValue(type).clicksOk == 0
            finish(
                type,
                if (noProgress) GrantResult.CANCELLED else GrantResult.GRANTED,
                reason = if (noProgress) "SIGNATURE_GONE_NO_CLICK" else null,
            )
            return
        }

        // 5) 模糊兜底层（签名仍在的前提下才允许；守卫沿上游 m0.java:451-455，[CAL_FUZZY] 校准点）。
        //    FR57-R1：上一节拍点击成功过的同一身份键目标仍在屏 = 模糊点击未生效
        //    （fuzzyCandidates.firstOrNull() 每节拍重选同一静态节点）→ 计失败不重试，
        //    走两连败放弃+抑制（docs/12 铁律 2 对模糊层生效）。
        if (config.fuzzyAllowFallback) {
            val fuzzy = scan.fuzzyCandidates.firstOrNull()
            if (fuzzy != null) {
                if (fuzzy.identityKey() == lastFuzzyIdentity) {
                    registerFailure(FailureKind.INEFFECTIVE_CLICK)
                    return
                }
                performClick(type, ClickTarget(null, fuzzy, "FUZZY", null))
                return
            }
        }

        // 6) 签名仍在但无可点目标（含「找不到节点」）→ 计失败（docs/12 铁律 2）。
        registerFailure(FailureKind.MISSING_BUTTON)
    }

    // ---- 点击执行（ACTION_CLICK 失败 → AtomicOperations 手势补点，docs/01 FR-57） ----

    private fun performClick(type: PermissionDialogType, target: ClickTarget) {
        val level = target.levelCode ?: "UNLEVEL"
        when (val op = operations.click(target.node)) {
            is OpResult.Ok -> {
                target.label?.let(clickedLabels::add)
                if (target.tier == "FUZZY") {
                    // FR57-R1：模糊层以身份键记「已点」（label=null 不进 clickedLabels）；
                    // 点击失败（Failed 分支）不记账——CLICK_FAILED 已按失败计数。
                    lastFuzzyIdentity = target.node.identityKey()
                } else {
                    lastFuzzyIdentity = null // 严格/一次性层有进展（翻页）：旧模糊身份作废
                }
                consecutiveFailures = 0
                stats.getValue(type).clicksOk++
                audit(
                    "perm_grant:CLICK type=${type.code} level=$level tier=${target.tier} " +
                        "method=${op.method.name}",
                )
                // 三级流最深一级（「允许访问全部」）点击成功 = 授权流终态。
                if (target.label != null && target.label in PermissionDialogLexicon.STEP3_ALLOW_ALL) {
                    finish(type, GrantResult.GRANTED, reason = null)
                    return
                }
                requestEvaluation() // 拟人间隔后再核验进展（翻页/消失/未生效）
            }
            is OpResult.Failed -> {
                stats.getValue(type).clicksFailed++
                audit(
                    "perm_grant:CLICK_FAIL type=${type.code} level=$level tier=${target.tier} " +
                        "reason=${op.reason.name} failures=${consecutiveFailures + 1}",
                )
                registerFailureAfterAudit(FailureKind.CLICK_FAILED)
            }
        }
    }

    private fun registerFailure(kind: FailureKind) {
        audit("perm_grant:OBSERVE type=${activeType?.code ?: "-"} kind=${kind.name} failures=${consecutiveFailures + 1}")
        registerFailureAfterAudit(kind)
    }

    /** docs/12 铁律 2：连续 [PermissionGrantConfig.maxConsecutiveFailures] 次失败=终局停止。 */
    private fun registerFailureAfterAudit(kind: FailureKind) {
        val type = activeType ?: return
        consecutiveFailures++
        if (consecutiveFailures >= config.maxConsecutiveFailures) {
            finish(type, GrantResult.MANUAL_REQUIRED, reason = kind.name)
        } else {
            requestEvaluation()
        }
    }

    // ---- 终态（docs/12 铁律 2：放弃即按类型抑制，弹窗签名消失后重臂） ----

    private fun finish(type: PermissionDialogType, result: GrantResult, reason: String?) {
        phase = Phase.IDLE
        evaluationPending = false
        activeType = null
        val c = stats.getValue(type)
        when (result) {
            GrantResult.GRANTED -> c.granted++
            GrantResult.MANUAL_REQUIRED -> {
                c.manualRequired++
                suppressed += type
            }
            GrantResult.CANCELLED -> c.cancelled++
        }
        audit(
            "perm_grant:DONE type=${type.code} result=$result levels=${clickedLabels.size}" +
                (reason?.let { " reason=$it" } ?: ""),
        )
        clickedLabels.clear()
        lastFuzzyIdentity = null
        consecutiveFailures = 0
    }

    // ---- 窗口扫描与目标定位 ----

    private class ClickTarget(val label: String?, val node: UiNode, val tier: String, val levelCode: String?)

    private class LabelHit(val label: String, val levelCode: String, val target: UiNode)

    private class WindowScan {
        val texts = ArrayList<String>()
        val labelTargets = LinkedHashMap<String, LabelHit>()
        val fuzzyCandidates = ArrayList<UiNode>()
        private val seenTexts = HashSet<String>()
        var nodeVisits = 0
            private set

        fun recordText(text: String): Boolean = seenTexts.add(text)

        fun countNode() {
            nodeVisits++
        }

        fun hasAnyLabel(labels: Set<String>): Boolean = labels.any(labelTargets::containsKey)
    }

    private fun scanWindows(): WindowScan {
        val scan = WindowScan()
        for (root in rootsProvider()) {
            scanNode(root, scan)
        }
        return scan
    }

    private fun scanNode(node: UiNode, scan: WindowScan) {
        if (scan.nodeVisits >= MAX_SCAN_NODES) return
        scan.countNode()
        val text = node.effectiveText()
        if (!text.isNullOrEmpty() && scan.recordText(text)) {
            scan.texts += text
            // 严格层精确匹配（trim 后全等；「不允许」等否定词不会与正向标签全等，天然排除）。
            for ((levelCode, labels) in ALL_LEVELS) {
                if (text in labels) {
                    clickableTarget(node)?.let {
                        scan.labelTargets.putIfAbsent(text, LabelHit(text, levelCode, it))
                    }
                }
            }
            // 模糊兜底候选（守卫沿上游 m0.java:451-455：含「允许」、不含否定词、无问号、长度上限）。
            if (config.fuzzyAllowFallback &&
                text.length <= PermissionDialogLexicon.FUZZY_MAX_TEXT_LEN &&
                !text.contains('?') && !text.contains('？') &&
                PermissionDialogLexicon.DENY_WORDS.none(text::contains) &&
                text.contains(PermissionDialogLexicon.GENERIC_ALLOW)
            ) {
                clickableTarget(node)?.let { scan.fuzzyCandidates += it }
            }
        }
        for (i in 0 until node.childCount) {
            node.childAt(i)?.let { scanNode(it, scan) }
            if (scan.nodeVisits >= MAX_SCAN_NODES) return
        }
    }

    /** 文本节点→可点目标：含自身向上爬 ≤[PermissionDialogLexicon.CLICKABLE_CLIMB_DEPTH] 层（m0.java:457-467）。 */
    private fun clickableTarget(node: UiNode): UiNode? {
        var cur: UiNode? = node
        var depth = 0
        while (cur != null && depth < PermissionDialogLexicon.CLICKABLE_CLIMB_DEPTH) {
            if (cur.isClickable) return cur
            cur = cur.parent
            depth++
        }
        return null
    }

    private fun findStrictTarget(scan: WindowScan): ClickTarget? {
        for ((levelCode, labels) in PermissionDialogLexicon.GRANT_STEP_LABELS) {
            val hit = labels.firstNotNullOfOrNull { scan.labelTargets[it] } ?: continue
            if (hit.label !in clickedLabels) {
                return ClickTarget(hit.label, hit.target, "STRICT", hit.levelCode)
            }
        }
        return null
    }

    // ---- 语料判定（特征词 +「允许」，SR-06：语料只用于判定，不出现在任何审计行） ----

    private class Corpus(val texts: List<String>) {
        fun matchesSignature(type: PermissionDialogType): Boolean {
            val featureHit = type.featureWords.any { w -> texts.any { it.contains(w) } }
            val allowHit = texts.any { t ->
                t.contains(PermissionDialogLexicon.GENERIC_ALLOW) &&
                    PermissionDialogLexicon.DENY_WORDS.none(t::contains)
            }
            return featureHit && allowHit
        }
    }

    private fun classify(corpus: Corpus): PermissionDialogType? =
        PermissionDialogType.entries.firstOrNull { corpus.matchesSignature(it) }

    private fun UiNode.effectiveText(): String? = text?.trim() ?: contentDescription?.trim()

    companion object {
        /** 框架 `AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED` 值（JVM 侧自持常量，避免 android 依赖）。 */
        const val TYPE_WINDOW_STATE_CHANGED = 32

        /** 框架 `AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED` 值。 */
        const val TYPE_WINDOW_CONTENT_CHANGED = 2048

        /** 全部严格层 + 可选层（扫描时统一登记标签目标）。 */
        private val ALL_LEVELS: List<Pair<String, List<String>>> =
            PermissionDialogLexicon.GRANT_STEP_LABELS + ("ONE_SHOT" to PermissionDialogLexicon.SYSTEM_ONE_SHOT)

        /** 单窗口扫描节点数上限（防巨树卡顿；与 SelectorEngine 扫描上限同量级）。 */
        private const val MAX_SCAN_NODES = 2000
    }
}

/**
 * FR-57 配置（docs/01 FR-57 + docs/12 铁律 2/4；每类弹窗独立开关，默认全开）。
 */
data class PermissionGrantConfig(
    /** 总开关（false = 零动作零审计）。 */
    val masterEnabled: Boolean = true,
    /** 每类弹窗独立开关（默认开，可配——docs/01 FR-57 任务口径③）。 */
    val perTypeEnabled: Map<PermissionDialogType, Boolean> =
        PermissionDialogType.entries.associateWith { true },
    /** 事件包名门（默认仅企微自有预提示页；[CAL_PKG] 系统弹窗扩展为真机校准点）。 */
    val watchPackages: Set<String> = setOf(WeComRulePack_PACKAGE_NAME),
    /** 相邻 UI 动作最小间隔（docs/12 铁律 4：≥800ms）。 */
    val paceMinMillis: Long = 800L,
    /** 抖动上限毫秒（实际抖动由 jitter 生成器给出并钳到此值内）。 */
    val paceJitterMillis: Long = 400L,
    /** 单弹窗授权流程超时（超时=MANUAL_REQUIRED，docs/12 铁律 2 语义）。 */
    val grantTimeoutMillis: Long = 15_000L,
    /** 连续无进展失败上限（docs/12 铁律 2：两次失败=终局停止）。 */
    val maxConsecutiveFailures: Int = 2,
    /** 系统弹窗一次性选项层（m0.java:397）是否纳入点击候选；默认关。 */
    val acceptSystemOneShot: Boolean = false,
    /** 模糊兜底层（m0.java:451-455 守卫）开关。 */
    val fuzzyAllowFallback: Boolean = true,
    /** 授权中是否把 TYPE_WINDOW_CONTENT_CHANGED 也当翻页信号。 */
    val progressOnContentChanged: Boolean = true,
)

/** 企微包名（引用 [WeComRulePack.PACKAGE_NAME] 的编译期常量镜像，避免 JVM 侧对象依赖歧义）。 */
private const val WeComRulePack_PACKAGE_NAME = WeComRulePack.PACKAGE_NAME
