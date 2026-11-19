package io.worktool.open.prototype.ui

import android.app.Activity
import android.app.AlertDialog
import android.content.ComponentName
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.InputFilter
import android.text.InputType
import android.text.method.ScrollingMovementMethod
import android.view.View
import android.view.WindowManager
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.ScrollView
import io.worktool.open.prototype.R
import io.worktool.open.prototype.policy.VariantFeatures
import io.worktool.open.prototype.policy.ConnectionMode
import io.worktool.open.prototype.accessibility.FixtureServiceRegistry
import io.worktool.open.prototype.accessibility.FixtureButtonOutcome

/** Candidate host UI. No other-app target, automatic service enabling, or background execution. */
class FixtureActivity : Activity(), ClientUiListener {
    private var bridge: ClientUiBridge? = null
    private val featureEnabled = VariantFeatures.ownFixtureAccessibility
    private var foreground = false
    private var attached = false
    private var activityEpoch = 0L
    private var clickPending = false
    private var connectPending = false
    private var probePending = false
    private var current = ClientUiSnapshot()
    private lateinit var input: EditText
    private lateinit var status: TextView
    private lateinit var serviceStatus: TextView
    private lateinit var output: TextView
    private lateinit var probeText: TextView
    private lateinit var connectButton: Button
    private lateinit var enableButton: Button
    private lateinit var inspectButton: Button
    private lateinit var sendButton: Button
    private var localCount = 0
    private var confirmationDialog: AlertDialog? = null
    private var activeConfirmation: FixtureConfirmationUi? = null
    private val handledConfirmations = mutableSetOf<String>()
    private val handler = Handler(Looper.getMainLooper())
    private val statusRefresh = object : Runnable {
        override fun run() {
            if (!foreground) return
            renderControls()
            handler.postDelayed(this, 1000L)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(null)
        if(!featureEnabled){finish();return}
        UiViews.secureWindow(this)
        bridge = (application as? ClientUiBridgeProvider)?.clientUiBridge
        val body = UiViews.screen(this, R.id.ui_fixture_root)
        UiViews.text(this, body, "本包 Fixture 动作候选", size = 23f)
        UiViews.text(this, body, "SYNTHETIC ONLY · 不向真实企微发送", R.id.ui_fixture_banner, 16f)
        UiViews.text(this, body, "这是含受保护 AccessibilityService 的新候选，不是首个无服务 APK。fixture.ui.submit_text 是独立动作，旧 message.send_text/203 仍只写合成收件箱。系统启用、本地开关、服务器授权和逐任务确认缺一不可。默认1.0配对身份不能凭切模式自授新cap；服务器不支持1.1则拒绝，绝不改为203发送。当前Go尚无1.1网关。构建与设备验收未完成前不能宣称可用。")
        serviceStatus = UiViews.text(this, body, "尚未读取服务状态。", R.id.ui_fixture_service_status)
        status = UiViews.text(this, body, "尚未手动恢复连接。", R.id.ui_fixture_status).apply {
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        UiViews.button(this, body, "打开系统设置，由我手动选择是否启用", R.id.ui_fixture_open_accessibility_settings) {
            if (!AccessibilityUi.openSettings(this)) status.text = "SETTINGS_UNAVAILABLE：没有自动启用服务。"
        }
        connectButton = UiViews.button(this, body, "显式以 own-fixture 1.1 恢复连接", R.id.ui_fixture_manual_connect) {
            if (!foreground || connectPending || bridge == null) return@button
            connectPending = true
            renderControls()
            bridge?.resumeAndConnect(mode=ConnectionMode.OWN_FIXTURE_1_1)
        }
        enableButton = UiViews.button(this, body, "开启本页动作（仍须逐任务确认）", R.id.ui_fixture_enable_actions) {
            val state = FixtureServiceRegistry.status()
            if (!foreground || !state.serviceConnected) { status.text = "SERVICE_NOT_CONNECTED：先由用户启用服务。"; return@button }
            FixtureServiceRegistry.setLocalEnabled(!state.locallyEnabled)
            if (state.locallyEnabled) { rejectConfirmation(); bridge?.pauseAndDisconnect() }
            renderControls()
        }
        UiViews.button(this, body, "停止本页动作并断线", R.id.ui_fixture_stop) { stopLocalActions(false) }
        UiViews.button(this, body, "停止并停用系统中的本包服务", R.id.ui_fixture_disable_service) { stopLocalActions(true) }
        UiViews.text(this, body, "仅允许本安装包和本 Activity 的固定控件。离开、旋转、服务中断或进程重建会取消本地租约；返回后不会自动恢复或重放。")
        UiViews.text(this, body, "synthetic_target_001", R.id.ui_fixture_target)
        UiViews.text(this, body, "合成文本（最多 500 Unicode code points）").apply { labelFor = R.id.ui_fixture_input }
        input = EditText(this).apply {
            id = R.id.ui_fixture_input
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            imeOptions = EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
            filters = arrayOf(codePointFilter(500))
            minLines = 3
            maxLines = 4
            layoutParams = LinearLayout.LayoutParams(-1, UiViews.dp(this@FixtureActivity, 110))
            isSaveEnabled = false
            setSaveFromParentEnabled(false)
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS
            body.addView(this)
        }
        sendButton = UiViews.button(this, body, "提交到本页 Fixture", R.id.ui_fixture_send) { submitFixtureText() }
        output = UiViews.text(this, body, "本页暂无合成输出。", R.id.ui_fixture_output).apply {
            movementMethod = ScrollingMovementMethod()
            isVerticalScrollBarEnabled = true
            layoutParams = LinearLayout.LayoutParams(-1, UiViews.dp(this@FixtureActivity, 100))
        }
        inspectButton = UiViews.button(this, body, "只读检查目标（不填写、不点击）", R.id.ui_fixture_dry_run) {
            if (!foreground || probePending || bridge == null) return@button
            probePending = true
            probeText.text = "检查已请求；这不是批准，也不是动作执行结果。"
            renderControls()
            bridge?.inspectFixture()
        }
        probeText = UiViews.text(this, body, "尚未检查目标。", R.id.ui_fixture_probe)
        UiViews.button(this, body, "清除本页普通输入与输出", R.id.ui_fixture_clear) {
            if (clickPending || activeConfirmation != null) { status.text = "ACTION_PENDING：在途动作未收敛，不能改写目标。"; return@button }
            input.text.clear()
            output.text = "本页暂无合成输出。"
            localCount = 0
            status.text = "LOCAL_FIXTURE_CLEARED：没有删除持久回执或远端账本。"
        }
        UiViews.button(this, body, "返回状态页（会暂停并断线）", R.id.ui_fixture_back) { finish() }
        renderControls()
    }

    override fun onStart() {
        super.onStart()
        if(!featureEnabled)return
        attached = true
        bridge?.attach(this)
        bridge?.snapshot()?.let { onSnapshot(it) }
    }

    override fun onResume() {
        super.onResume()
        if(!featureEnabled)return
        foreground = true
        activityEpoch += 1L
        FixtureServiceRegistry.foregroundEntered(this)
        // foregroundEntered must not restore local approval or automatically connect.
        handler.post(statusRefresh)
        renderControls()
    }

    override fun onPause() {
        if(!featureEnabled){super.onPause();return}
        foreground = false
        activityEpoch += 1L
        handler.removeCallbacks(statusRefresh)
        FixtureServiceRegistry.foregroundLeft(this)
        rejectConfirmation()
        bridge?.pauseAndDisconnect()
        input.text?.clear()
        connectPending = false
        probePending = false
        super.onPause()
    }

    override fun onStop() {
        if(!featureEnabled){super.onStop();return}
        attached = false
        bridge?.pauseAndDisconnect()
        bridge?.detach(this)
        dismissConfirmation()
        super.onStop()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.clear()
    }

    override fun onSnapshot(snapshot: ClientUiSnapshot) {
        if (Looper.myLooper() != Looper.getMainLooper()) { runOnUiThread { onSnapshot(snapshot) }; return }
        if (!attached || isFinishing || isDestroyed) return
        current = snapshot
        if (snapshot.wsState == UiWsState.CONNECTED || snapshot.wsState == UiWsState.ERROR || snapshot.errorCode != null || snapshot.statusCode == "DISCONNECTED_MANUAL_RESUME_REQUIRED") connectPending = false
        snapshot.fixtureProbe?.let {
            probePending = false
            probeText.text = "只读检查：${it.ready} · ${UiViews.code(it.reasonCode)}\n包：${it.packageName ?: "未知"}\n组件：${it.activityClass ?: "未知"}\n窗口：${it.windowId ?: "未知"} · elapsed：${it.checkedAtElapsedMs}\n检查不授予动作权限，执行前必须重新验证。"
        }
        if (snapshot.errorCode != null) { status.text = "错误：${UiViews.code(snapshot.errorCode)}；没有自动重试。"; probePending = false }
        if (snapshot.paused || snapshot.wsState != UiWsState.CONNECTED) {
            dismissConfirmation()
        } else if (foreground) {
            snapshot.fixtureConfirmation?.let { if (key(it) !in handledConfirmations && key(it) != activeConfirmation?.let(::key)) showConfirmation(it) }
            if (snapshot.fixtureConfirmation == null) dismissConfirmation()
        }
        renderControls()
    }

    private fun renderControls() {
        if (!::serviceStatus.isInitialized) return
        val state = FixtureServiceRegistry.status()
        serviceStatus.text = AccessibilityUi.describe(this)
        connectButton.isEnabled = foreground && current.initialized && current.paired && !current.busy && !connectPending && (current.paused || current.wsState != UiWsState.CONNECTED)
        enableButton.isEnabled = foreground && state.serviceConnected
        enableButton.text = if (state.locallyEnabled) "关闭本页动作开关" else "开启本页动作（仍须逐任务确认）"
        inspectButton.isEnabled = foreground && state.serviceConnected && !probePending && bridge != null
        sendButton.isEnabled = foreground && !clickPending
        if (activeConfirmation != null && (!state.serviceConnected || !state.locallyEnabled || !state.foreground)) rejectConfirmation()
    }

    private fun submitFixtureText() {
        if (!foreground || clickPending) return
        val text = input.text.toString()
        if (text.isBlank()) { status.text = "EMPTY_INPUT：没有执行。"; return }
        clickPending = true
        val epoch = activityEpoch
        renderControls()
        FixtureServiceRegistry.buttonClick(this, text) { result ->
            clickPending = false
            if (!foreground || epoch != activityEpoch || isFinishing || isDestroyed) return@buttonClick
            when (result.outcome) {
                FixtureButtonOutcome.MANUAL_ONLY -> {
                    localCount += 1
                    output.text = "本页普通合成条目 $localCount\n目标：synthetic_target_001\n$text"
                    status.text = "LOCAL_ONLY：没有 remote ticket，不生成远端成功回执。"
                    input.text.clear()
                }
                FixtureButtonOutcome.COMMITTED -> {
                    val receipt = result.receipt
                    if (receipt == null) {
                        status.text = "RECEIPT_MISSING：拒绝回退为手动动作。"
                    } else {
                        output.text = receipt.renderText
                        status.text = "持久 Fixture 回执已显示；远端终态仍由引擎核对后置条件和 result_ack。"
                        input.text.clear()
                    }
                }
                FixtureButtonOutcome.REJECTED -> status.text = "${UiViews.code(result.reasonCode)}：票据无效或提交失败，零手动回退。"
            }
            renderControls()
        }
    }

    private fun stopLocalActions(disable: Boolean) {
        FixtureServiceRegistry.setLocalEnabled(false)
        rejectConfirmation()
        bridge?.pauseAndDisconnect()
        if (disable) FixtureServiceRegistry.disableService()
        status.text = "已请求停止后续动作；已产生效果不能假定撤回。"
        renderControls()
    }

    private fun key(request: FixtureConfirmationUi) = request.commandId + ":" + request.attemptId + ":" + request.payloadDigest

    private fun showConfirmation(request: FixtureConfirmationUi) {
        val state = FixtureServiceRegistry.status()
        val expectedActivity = ComponentName(this, FixtureActivity::class.java).className
        if (!foreground || !state.serviceConnected || !state.locallyEnabled || request.targetPackage != packageName || request.targetActivity != expectedActivity || request.text.codePointCount(0, request.text.length) > 500) {
            handledConfirmations.add(key(request))
            bridge?.decideFixture(request.commandId, request.attemptId, request.payloadDigest, false)
            status.text = "LOCAL_SCOPE_DENIED：当前包、组件或本地许可不匹配。"
            return
        }
        dismissConfirmation()
        activeConfirmation = request
        val content = LinearLayout(this).apply {
            id = R.id.ui_fixture_confirmation
            orientation = LinearLayout.VERTICAL
            isSaveEnabled = false
            setPadding(UiViews.dp(this@FixtureActivity, 20), UiViews.dp(this@FixtureActivity, 12), UiViews.dp(this@FixtureActivity, 20), 0)
        }
        content.addView(TextView(this).apply {
            text = "fixture.ui.submit_text · 实际操作本包控件\n包：${request.targetPackage}\n组件：${request.targetActivity}\n任务：…${UiViews.suffix(request.commandId)}\n参数摘要：${request.payloadDigest.take(12)}…\n\n将填写并提交以下完整合成文本：\n${request.text}\n\n不会发送真实企微消息。批准仅本次意图，不是全局授权。"
            isSaveEnabled = false
            textSize = 14f
        })
        val scroll = ScrollView(this).apply { isSaveEnabled = false; addView(content) }
        val dialog = AlertDialog.Builder(this).setTitle("确认一次本包 Fixture 动作")
            .setView(scroll)
            .setNegativeButton("拒绝") { _, _ -> decide(request, false) }
            .setPositiveButton("批准这一次", null).create()
        confirmationDialog = dialog
        dialog.window?.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        dialog.setCanceledOnTouchOutside(false)
        dialog.setOnCancelListener { decide(request, false) }
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener { decide(request, true) }
        }
        dialog.show()
    }

    private fun decide(request: FixtureConfirmationUi, approved: Boolean) {
        if (!handledConfirmations.add(key(request))) { dismissConfirmation(); return }
        val epoch = activityEpoch
        dismissConfirmation()
        // Give the dialog window a chance to leave. Driver must independently obtain fresh nodes/window.
        window.decorView.post {
            val latest = current.fixtureConfirmation
            val local = FixtureServiceRegistry.status()
            val allow = approved && foreground && activityEpoch == epoch && local.serviceConnected && local.locallyEnabled && local.foreground &&
                !current.paused && current.wsState == UiWsState.CONNECTED && latest != null && key(latest) == key(request)
            bridge?.decideFixture(request.commandId, request.attemptId, request.payloadDigest, allow)
            if (foreground) status.text = "已提交本次${if (allow) "批准" else "拒绝"}决定；等待引擎重新检查窗口和实际效果。"
        }
    }

    private fun rejectConfirmation() {
        val request = activeConfirmation
        if (request != null && handledConfirmations.add(key(request))) bridge?.decideFixture(request.commandId, request.attemptId, request.payloadDigest, false)
        dismissConfirmation()
    }

    private fun dismissConfirmation() {
        confirmationDialog?.setOnCancelListener(null)
        confirmationDialog?.dismiss()
        confirmationDialog = null
        activeConfirmation = null
    }

    private fun codePointFilter(limit: Int): InputFilter = InputFilter { source, start, end, destination, dstart, dend ->
        val kept = destination.subSequence(0, dstart).toString() + destination.subSequence(dend, destination.length)
        val remaining = limit - kept.codePointCount(0, kept.length)
        val replacement = source.subSequence(start, end).toString()
        when {
            remaining <= 0 -> ""
            replacement.codePointCount(0, replacement.length) <= remaining -> null
            else -> replacement.substring(0, replacement.offsetByCodePoints(0, remaining))
        }
    }
}
