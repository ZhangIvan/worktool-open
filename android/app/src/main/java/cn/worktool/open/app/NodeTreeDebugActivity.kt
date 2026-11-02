/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Activity
import android.graphics.Typeface
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.method.ScrollingMovementMethod
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import cn.worktool.open.core.accessibility.WorkToolAccessibilityService
import cn.worktool.open.core.accessibility.android.RuleConfigLoader
import cn.worktool.open.core.accessibility.json.Json
import cn.worktool.open.core.accessibility.rules.RuleConfigException
import cn.worktool.open.core.accessibility.rules.RuleConfigParser
import cn.worktool.open.core.accessibility.rules.SelectorEngine
import cn.worktool.open.core.accessibility.tree.UiNode

/**
 * 节点树调试页（T2.4，docs/03 任务卡：实时 dump 节点树 + 选择器试掷，供 D1 真机定位用）。
 *
 * 用途：在真机上打开目标 App（如企微）→ 切回本页（分屏/最近任务切换）→
 * dump 当前活动窗口节点树 → 用真实 resourceId/text 校准外部规则 JSON。
 *
 * 数据不出设备（SR-03 零遥测）；仅渲染到本地 TextView，无任何上报通道。
 * SR-10：exported=false、命名明示用途。
 */
class NodeTreeDebugActivity : Activity() {

    private val handler = Handler(Looper.getMainLooper())
    private var autoRefresh = false

    private lateinit var statusView: TextView
    private lateinit var selectorInput: EditText
    private lateinit var trialResultView: TextView
    private lateinit var treeView: TextView

    private val engine = SelectorEngine()

    private val autoRefreshTask = object : Runnable {
        override fun run() {
            refreshTree()
            if (autoRefresh) handler.postDelayed(this, AUTO_REFRESH_INTERVAL_MS)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(buildContentView())
        refreshTree()
    }

    private fun buildContentView(): View {
        val pad = dp(12)
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }

        statusView = TextView(this).apply { textSize = 12f }
        root.addView(statusView)

        val buttons = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        buttons.addView(Button(this).apply {
            text = getString(R.string.debug_refresh)
            setOnClickListener { refreshTree() }
        })
        buttons.addView(Button(this).apply {
            text = getString(R.string.debug_auto_refresh_off)
            setOnClickListener {
                autoRefresh = !autoRefresh
                text = getString(
                    if (autoRefresh) R.string.debug_auto_refresh_on else R.string.debug_auto_refresh_off,
                )
                handler.removeCallbacks(autoRefreshTask)
                if (autoRefresh) handler.postDelayed(autoRefreshTask, AUTO_REFRESH_INTERVAL_MS)
            }
        })
        buttons.addView(Button(this).apply {
            text = getString(R.string.debug_trial)
            setOnClickListener { trialSelector() }
        })
        buttons.addView(Button(this).apply {
            text = getString(R.string.debug_screenshot)
            setOnClickListener { tryScreenshot() }
        })
        root.addView(buttons)

        selectorInput = EditText(this).apply {
            setText(TRIAL_PRESET)
            textSize = 12f
            setTypeface(Typeface.MONOSPACE)
            minLines = 3
            maxLines = 6
            hint = getString(R.string.debug_selector_hint)
        }
        root.addView(selectorInput, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT,
        ))

        trialResultView = TextView(this).apply {
            textSize = 12f
            setTypeface(Typeface.MONOSPACE)
            setTextIsSelectable(true)
        }
        root.addView(trialResultView, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT,
        ).apply { topMargin = dp(4) })

        treeView = TextView(this).apply {
            textSize = 11f
            setTypeface(Typeface.MONOSPACE)
            setTextIsSelectable(true)
            movementMethod = ScrollingMovementMethod()
        }
        root.addView(treeView, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f,
        ).apply { topMargin = dp(8) })

        val scroll = android.widget.ScrollView(this).apply { addView(root) }
        return scroll
    }

    /** dump 当前活动窗口节点树（缩进层级渲染）。 */
    private fun refreshTree() {
        val service = WorkToolAccessibilityService.instance
        if (service == null) {
            statusView.text = getString(R.string.debug_service_off)
            treeView.text = ""
            return
        }
        val root = service.rootNode()
        if (root == null) {
            statusView.text = getString(R.string.debug_no_active_window)
            treeView.text = ""
            return
        }
        statusView.text = getString(
            R.string.debug_service_on,
            root.className ?: "(null)",
            root.childCount,
        )
        treeView.text = engine.dumpTree(root)
    }

    /**
     * 选择器试掷：输入为完整 selector 对象 JSON，或 `@规则id`（从 assets 样例配置取）。
     * 展示命中数与每个命中节点的单行描述。
     */
    private fun trialSelector() {
        val service = WorkToolAccessibilityService.instance
        val input = selectorInput.text?.toString()?.trim().orEmpty()
        if (service == null) {
            trialResultView.text = getString(R.string.debug_service_off)
            return
        }
        val root = service.rootNode()
        if (root == null) {
            trialResultView.text = getString(R.string.debug_no_active_window)
            return
        }
        val selector = try {
            if (input.startsWith("@")) {
                val config = RuleConfigLoader.fromAssetsOrDefault(this)
                val id = input.removePrefix("@")
                config.selector(id) ?: run {
                    trialResultView.text = getString(R.string.debug_selector_id_missing, id)
                    return
                }
            } else {
                RuleConfigParser.parseSelectorObject(Json.parse(input) as cn.worktool.open.core.accessibility.json.JsonValue.JsonObject)
            }
        } catch (e: RuleConfigException) {
            trialResultView.text = getString(R.string.debug_selector_invalid, e.message)
            return
        } catch (e: Json.JsonParseException) {
            trialResultView.text = getString(R.string.debug_selector_invalid, e.message)
            return
        } catch (e: ClassCastException) {
            trialResultView.text = getString(R.string.debug_selector_invalid, "根元素必须是 JSON 对象")
            return
        }
        val hits = engine.resolveAll(root, selector)
        val lines = buildString {
            append(getString(R.string.debug_trial_result, selector.id, hits.size)).append('\n')
            hits.take(MAX_TRIAL_HITS).forEach { append("  · ").append(engine.describeNode(it)).append('\n') }
            if (hits.size > MAX_TRIAL_HITS) append("  …\n")
        }
        trialResultView.text = lines.trimEnd()
    }

    /** 截图原子操作真机自检（结果只显示元数据，不保存不外发）。 */
    private fun tryScreenshot() {
        val service = WorkToolAccessibilityService.instance
        if (service == null) {
            trialResultView.text = getString(R.string.debug_service_off)
            return
        }
        service.screenshotOperations.takeScreenshot { result ->
            handler.post {
                val text = when (result) {
                    is cn.worktool.open.core.accessibility.ops.ScreenshotResult.Ok ->
                        getString(R.string.debug_screenshot_ok, result.width, result.height, result.pngBytes.size)
                    cn.worktool.open.core.accessibility.ops.ScreenshotResult.UnsupportedPlatform ->
                        getString(R.string.debug_screenshot_unsupported)
                    is cn.worktool.open.core.accessibility.ops.ScreenshotResult.Failed ->
                        getString(R.string.debug_screenshot_failed, result.errorCode, result.message)
                }
                trialResultView.text = text
                Toast.makeText(this, text, Toast.LENGTH_SHORT).show()
            }
        }
    }

    override fun onResume() {
        super.onResume()
        refreshTree()
    }

    override fun onPause() {
        super.onPause()
        autoRefresh = false
        handler.removeCallbacks(autoRefreshTask)
    }

    private fun dp(v: Int): Int = (v * resources.displayMetrics.density).toInt()

    companion object {
        private const val AUTO_REFRESH_INTERVAL_MS = 1000L
        private const val MAX_TRIAL_HITS = 20

        /** 试掷预置：命中当前树中全部可见 TextView（D1 校准第一步通用探针）。 */
        private const val TRIAL_PRESET =
            """{"id":"trial","match":{"className":"android.widget.TextView"},"chain":[]}"""
    }
}
