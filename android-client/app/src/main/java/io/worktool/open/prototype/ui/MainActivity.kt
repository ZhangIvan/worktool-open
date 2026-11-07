package io.worktool.open.prototype.ui

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.os.Bundle
import android.os.Looper
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.LinearLayout
import android.widget.RadioGroup
import android.widget.RadioButton
import io.worktool.open.prototype.R
import io.worktool.open.prototype.policy.VariantFactory
import io.worktool.open.prototype.policy.VariantFeatures

/** Foreground-only manual session. Lifecycle callbacks never reconnect or submit a command. */
class MainActivity : Activity(), ClientUiListener {
    private var bridge: ClientUiBridge? = null
    private val presentation = VariantFactory.presentation
    private val endpointRules = VariantFactory.endpointPolicy()
    private var originDialog: AlertDialog? = null
    private var foreground = false
    private var current = ClientUiSnapshot()
    private var pendingAction: String? = null
    private lateinit var endpoint: EditText
    private lateinit var enrollmentId: EditText
    private lateinit var enrollmentToken: EditText
    private lateinit var pairButton: Button
    private lateinit var resumeButton: Button
    private lateinit var pauseButton: Button
    private lateinit var status: TextView
    private lateinit var identity: TextView
    private lateinit var ledger: TextView
    private lateinit var inbox: TextView
    private lateinit var error: TextView
    private var confirmationDialog: AlertDialog? = null
    private var activeConfirmation: ExportConfirmationUi? = null
    private val decided = mutableSetOf<String>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(null)
        UiViews.secureWindow(this)
        bridge = (application as? ClientUiBridgeProvider)?.clientUiBridge
        val body = UiViews.screen(this, R.id.ui_main_root)
        UiViews.text(this, body, "WorkTool 原生合成客户端", size = 23f)
        UiViews.text(this, body, presentation.banner, R.id.ui_main_banner, 16f)
        UiViews.text(this, body, "只执行 message.send_text 与 file.export 的本地合成效果。67 项仅是参考能力追踪目录，其他参考能力未实现（unsupported）。原生网络、账本或界面模块存在，不等于企微或 Android 兼容性已验收。")
        status = UiViews.text(this, body, "尚未连接状态桥接", R.id.ui_main_status)
        identity = UiViews.text(this, body, "身份：未读取", R.id.ui_main_identity)
        error = UiViews.text(this, body, "", R.id.ui_main_error).apply {
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        UiViews.text(this, body, "手动安全配对", size = 19f)
        UiViews.text(this, body, presentation.description)
        endpoint = UiViews.input(this, body, presentation.endpointHint, R.id.ui_main_endpoint)
        enrollmentId = UiViews.input(this, body, "Enrollment ID", R.id.ui_main_enrollment_id, maxLength = 64)
        enrollmentToken = UiViews.input(this, body, "Enrollment token（一次性，不显示到状态页）", R.id.ui_main_enrollment_token, secret = true, maxLength = 4096)
        pairButton = UiViews.button(this, body, "配对，不自动执行", R.id.ui_main_pair) { pair() }
        resumeButton = UiViews.button(this, body, "手动恢复并连接", R.id.ui_main_resume) { resumeManually() }
        pauseButton = UiViews.button(this, body, "暂停新动作并断开", R.id.ui_main_pause) { pauseManually() }
        UiViews.text(this, body, "退到后台、离开本页或旋转都会暂停并断线；回来后不会自动恢复或重放。暂停不能撤回已经发生的效果。")
        if(VariantFeatures.ownFixtureAccessibility) {
            UiViews.text(this, body, "默认连接为1.0合成模式。独立 own-fixture 1.1 仅在下一页手动选择，须服务器设备cap与精确driver授权；不是企微，当前Go尚无该网关。")
            UiViews.button(this, body, "打开本 App Fixture 候选（暂停连接）", R.id.ui_main_open_fixture) {
                if(VariantFeatures.ownFixtureAccessibility) startActivity(Intent(this, FixtureActivity::class.java))
            }
        }
        UiViews.text(this, body, "持久账本与结果确认", size = 19f)
        ledger = UiViews.text(this, body, "尚未读取账本。", R.id.ui_main_ledger)
        UiViews.text(this, body, "本地合成收件箱", size = 19f)
        inbox = UiViews.text(this, body, "尚未读取合成收件箱。", R.id.ui_main_inbox)
        render(ClientUiSnapshot())
        if (bridge == null) error.text = "BRIDGE_UNAVAILABLE：原生 UI 尚未接入状态桥接，不能配对或连接。"
    }

    override fun onStart() {
        super.onStart()
        foreground = true
        bridge?.attach(this)
        bridge?.snapshot()?.let { onSnapshot(it) }
    }

    override fun onStop() {
        foreground = false
        originDialog?.dismiss(); originDialog=null
        clearPairingInputs()
        rejectVisibleConfirmation()
        bridge?.pauseAndDisconnect()
        bridge?.detach(this)
        pendingAction = null
        super.onStop()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.clear() // No credentials, input text, approval selection, or form restoration on rotation.
    }

    override fun onSnapshot(snapshot: ClientUiSnapshot) {
        if (Looper.myLooper() != Looper.getMainLooper()) {
            runOnUiThread { onSnapshot(snapshot) }
            return
        }
        if (!foreground || isFinishing || isDestroyed) return
        current = snapshot
        when (pendingAction) {
            "PAIR" -> if (snapshot.paired || snapshot.errorCode != null) pendingAction = null
            "CONNECT" -> if (snapshot.wsState == UiWsState.CONNECTED || snapshot.wsState == UiWsState.ERROR || snapshot.errorCode != null || snapshot.statusCode == "DISCONNECTED_MANUAL_RESUME_REQUIRED") pendingAction = null
            "PAUSE" -> if (snapshot.paused && snapshot.wsState in setOf(UiWsState.DISCONNECTED,UiWsState.ERROR)) pendingAction = null
        }
        render(snapshot)
        val request = snapshot.confirmation
        if (request == null || snapshot.paused || snapshot.wsState != UiWsState.CONNECTED) {
            dismissConfirmation()
        } else if (key(request) !in decided && key(request) != activeConfirmation?.let { key(it) }) {
            showExportConfirmation(request)
        }
    }

    private fun render(snapshot: ClientUiSnapshot) {
        val ready = bridge != null && snapshot.initialized
        val busy = snapshot.busy || pendingAction != null
        status.text = if (!snapshot.initialized) "正在等待已发布的状态快照；未把未知状态当成完成。" else
            "连接模式：${snapshot.connectionMode.name} · 协商版本：${snapshot.negotiatedVersion ?: "无"}\nWS：${snapshot.wsState.name}\n本地暂停：${if (snapshot.paused) "是" else "否"}\n状态代码：${UiViews.code(snapshot.statusCode)}\n操作处理中：${if (busy) "是" else "否"}"
        identity.text = snapshot.identity?.let {
            "身份已受保护保存：${if (snapshot.paired) "是" else "未确认"}\nDevice ID 尾部：…${UiViews.suffix(it.deviceIdSuffix)}\nKey ID 尾部：…${UiViews.suffix(it.keyIdSuffix)}\n不显示 secret、token 或完整凭据。"
        } ?: "尚无已确认的配对身份。"
        if (snapshot.errorCode != null) error.text = "错误代码：${UiViews.code(snapshot.errorCode)}。不会显示原异常、帧内容或秘密。"
        pairButton.isEnabled = ready && !busy && !snapshot.paired
        endpoint.isEnabled = pairButton.isEnabled || (endpointRules.requiresExplicitConnectionOrigin && ready && !busy && snapshot.paired && snapshot.paused)
        enrollmentId.isEnabled = pairButton.isEnabled
        enrollmentToken.isEnabled = pairButton.isEnabled
        resumeButton.isEnabled = ready && snapshot.paired && !busy && (snapshot.paused || snapshot.wsState != UiWsState.CONNECTED)
        pauseButton.isEnabled = ready && (snapshot.paired || snapshot.busy || snapshot.wsState != UiWsState.DISCONNECTED)
        ledger.text = if (!snapshot.initialized) "尚未读取账本。" else if (snapshot.ledger.isEmpty())
            "当前账本没有记录；这不代表其他系统任务已完成。" else snapshot.ledger.take(25).joinToString("\n\n") {
                "…${UiViews.suffix(it.commandId)} · ${it.stage.take(40)}\n结果已持久化：${if (it.hasDurableResult) "是" else "否"} · result_ack：${if (it.resultAcked) "已确认" else "未确认"}"
            } + "\n\n这里只显示最近 ${minOf(snapshot.ledger.size, 25)} 条；ACK 不是执行成功的替代。"
        inbox.text = if (!snapshot.initialized) "尚未读取合成收件箱。" else if (snapshot.inbox.isEmpty())
            "当前没有本地合成消息。不会读取真实联系人或聊天。" else snapshot.inbox.takeLast(20).joinToString("\n\n") {
                "${it.referenceId.take(80)}\n${it.text.take(500)}"
            } + "\n\n仅展示合成内容，长文本已截断。"
    }

    private fun pair() {
        val target = bridge ?: return
        if (!foreground || !pairButton.isEnabled || pendingAction != null) return
        val rawOrigin = endpoint.text.toString()
        val origin = validatedOrigin(rawOrigin)
        val id = enrollmentId.text.toString().trim()
        val token = enrollmentToken.text.toString().trim()
        if (origin == null) { error.text = "INVALID_ORIGIN：请按当前构建提示手输精确 origin，不含额外URL内容。"; return }
        if (!id.matches(Regex("[a-z][a-z0-9_-]{2,63}"))) { error.text = "INVALID_ENROLLMENT_ID：请检查配对 ID。"; return }
        if (token.isEmpty() || token.length > 4096 || token.any { it.isWhitespace() }) { error.text = "INVALID_ENROLLMENT_TOKEN：请检查一次性材料。"; return }
        requestOriginConsent(origin) {
            if (!foreground || pendingAction != null) return@requestOriginConsent
            pendingAction = "PAIR"
            error.text = "配对已提交；尚未确认配对成功，也不会自动执行。"
            clearPairingInputs()
            render(current)
            try { target.enroll(it, id, token) } catch (_: Exception) { failLocalAction() }
        }
    }

    private fun requestOriginConsent(raw: String, action: (String)->Unit) {
        val origin=validatedOrigin(raw)
        if(origin==null) { error.text="INVALID_ORIGIN"; return }
        val warning=presentation.confirmationWarning
        if(warning==null) { action(origin); return }
        originDialog?.dismiss()
        originDialog=AlertDialog.Builder(this).setTitle(presentation.banner)
            .setMessage(warning+"\n\n"+origin)
            .setNegativeButton("取消，不连接") { _,_ -> endpoint.text?.clear(); originDialog=null }
            .setPositiveButton("仅确认此实验 origin") { _,_ ->
                originDialog=null
                if(foreground && !isFinishing && !isDestroyed) action(origin)
            }.create().also { dialog ->
                dialog.setCanceledOnTouchOutside(false)
                dialog.setOnCancelListener { endpoint.text?.clear(); originDialog=null }
                dialog.show(); dialog.window?.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
            }
    }

    private fun resumeManually() {
        if (!foreground || !resumeButton.isEnabled || pendingAction != null) return
        val connect: (String?)->Unit = { confirmed ->
            if(foreground && pendingAction==null) {
                pendingAction="CONNECT"
                endpoint.text?.clear()
                error.text="已请求恢复，等待真实连接状态；不会因重连重复执行旧动作。"
                render(current)
                try { bridge?.resumeAndConnect(confirmed) } catch (_:Exception) { failLocalAction() }
            }
        }
        if(endpointRules.requiresExplicitConnectionOrigin) requestOriginConsent(endpoint.text.toString()) { connect(it) }
        else connect(null)
    }

    private fun pauseManually() {
        if (!foreground || bridge == null) return
        pendingAction = "PAUSE"
        rejectVisibleConfirmation()
        render(current)
        try { bridge?.pauseAndDisconnect() } catch (_: Exception) { failLocalAction() }
    }

    private fun failLocalAction() {
        pendingAction = null
        error.text = "UI_ACTION_FAILED：操作结果未确认，请查看状态；不会自动重试。"
        render(current)
    }

    private fun clearPairingInputs() {
        endpoint.text?.clear()
        enrollmentId.text?.clear()
        enrollmentToken.text?.clear()
    }

    private fun key(value: ExportConfirmationUi): String = value.commandId + ":" + value.scopeDigest + ":" + value.exportId

    private fun showExportConfirmation(request: ExportConfirmationUi) {
        dismissConfirmation()
        val choices = request.allowedReferenceIds.filter { it in SyntheticUiReferences.allowed }.distinct()
        activeConfirmation = request
        var selected: String? = null
        val message = "仅对内置合成 note 计算摘要（hash），不是文件读取或下载。\n不访问存储、相册、联系人或真实聊天。\n任务：…${UiViews.suffix(request.commandId)}\n导出：…${UiViews.suffix(request.exportId)}\n范围摘要：${request.scopeDigest.take(12)}…\n必须单选，最终仍由引擎核对批准 scope。"
        val panel = LinearLayout(this).apply {
            id = R.id.ui_export_confirmation
            orientation = LinearLayout.VERTICAL
            isSaveEnabled = false
            setPadding(UiViews.dp(this@MainActivity, 20), UiViews.dp(this@MainActivity, 12), UiViews.dp(this@MainActivity, 20), 0)
        }
        panel.addView(TextView(this).apply {
            text = if (choices.isEmpty()) "$message\n没有匹配的合成引用，不能同意。" else message
            isSaveEnabled = false
            textSize = 14f
        })
        val radios = RadioGroup(this).apply { isSaveEnabled = false }
        choices.forEach { reference ->
            radios.addView(RadioButton(this).apply {
                id = View.generateViewId()
                text = reference
                tag = reference
                isSaveEnabled = false
            })
        }
        radios.setOnCheckedChangeListener { group, checkedId ->
            val choice = group.findViewById<RadioButton>(checkedId)?.tag as? String
            selected = choice
            confirmationDialog?.getButton(AlertDialog.BUTTON_POSITIVE)?.isEnabled = choice != null && choice in choices
        }
        panel.addView(radios)
        val dialog = AlertDialog.Builder(this)
            .setTitle("本地确认合成导出")
            .setView(panel)
            .setNegativeButton("拒绝") { _, _ -> decide(request, null, false) }
            .setPositiveButton("同意合成摘要", null)
            .create()
        confirmationDialog = dialog
        dialog.window?.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        dialog.setCanceledOnTouchOutside(false)
        dialog.setOnCancelListener { decide(request, null, false) }
        dialog.setOnShowListener {
            dialog.window?.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).isEnabled = false
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                val now = current.confirmation
                val choice = selected
                if (foreground && !current.paused && current.wsState == UiWsState.CONNECTED && now != null &&
                    key(now) == key(request) && choice != null && choice in SyntheticUiReferences.allowed && choice in now.allowedReferenceIds) {
                    decide(request, choice, true)
                } else {
                    error.text = "CONFIRMATION_CHANGED：当前范围或前台状态已变化，不能批准。"
                    decide(request, null, false)
                }
            }
        }
        dialog.show()
    }

    private fun decide(request: ExportConfirmationUi, referenceId: String?, approved: Boolean) {
        if (!decided.add(key(request))) { dismissConfirmation(); return }
        dismissConfirmation()
        try {
            bridge?.decideExport(request.commandId, request.scopeDigest, referenceId, approved)
            error.text = "已提交本地${if (approved) "同意" else "拒绝"}决定；是否完成以账本和 result_ack 为准。"
        } catch (_: Exception) {
            error.text = "CONFIRMATION_SUBMIT_FAILED：决定未确认，不会自动同意或重试。"
        }
    }

    private fun rejectVisibleConfirmation() {
        val request = activeConfirmation
        if (request != null) decide(request, null, false) else dismissConfirmation()
    }

    private fun dismissConfirmation() {
        confirmationDialog?.setOnCancelListener(null)
        confirmationDialog?.dismiss()
        confirmationDialog = null
        activeConfirmation = null
    }

    companion object {
        /** Validation only; no DNS, requests, default host, or plaintext fallback. */
        internal fun validatedOrigin(value: String): String? = try {
            VariantFactory.endpointPolicy().parse(value)
        } catch (_: Exception) { null }
    }
}
