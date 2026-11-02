package io.worktool.open.prototype.ui

import android.app.Activity
import android.os.Bundle
import android.os.Looper
import android.text.InputFilter
import android.text.InputType
import android.view.View
import android.view.inputmethod.EditorInfo
import android.widget.EditText
import android.widget.TextView
import io.worktool.open.prototype.R

/** Real native widgets, exclusively inside this app. Not exported; no accessibility service registration. */
class FixtureActivity : Activity(), ClientUiListener {
    private var bridge: ClientUiBridge? = null
    private var foreground = false
    private lateinit var input: EditText
    private lateinit var status: TextView
    private lateinit var output: TextView
    private var localCount = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(null)
        UiViews.secureWindow(this)
        bridge = (application as? ClientUiBridgeProvider)?.clientUiBridge
        val body = UiViews.screen(this, R.id.ui_fixture_root)
        UiViews.text(this, body, "本 App 原生控件 fixture", size = 23f)
        UiViews.text(this, body, "SYNTHETIC ONLY · 不向外部应用发送", R.id.ui_fixture_banner, 16f)
        UiViews.text(this, body, "此页是实际 EditText、Button、TextView。手动按钮只更新本页合成文本，不创建远端命令或持久账本结果；不证明企微已实现。无服务注册、无启用无障碍引导。")
        UiViews.text(this, body, "synthetic_target_001", R.id.ui_fixture_target).apply {
            contentDescription = "合成目标 synthetic_target_001"
        }
        UiViews.text(this, body, "合成文本（最多 500 字符）").apply { labelFor = R.id.ui_fixture_input }
        input = EditText(this).apply {
            id = R.id.ui_fixture_input
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS
            imeOptions = EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING
            filters = arrayOf(InputFilter.LengthFilter(500))
            minLines = 3
            isSaveEnabled = false
            setSaveFromParentEnabled(false)
            importantForAutofill = View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS
            body.addView(this)
        }
        UiViews.button(this, body, "写入本页合成输出", R.id.ui_fixture_send) {
            if (!foreground) return@button
            val value = input.text.toString()
            if (value.isBlank()) { status.text = "EMPTY_INPUT：没有写入任何内容。"; return@button }
            localCount += 1
            output.text = "本页合成条目 $localCount\n目标：synthetic_target_001\n$value"
            input.text.clear()
            status.text = "LOCAL_FIXTURE_UPDATED：只修改本页 TextView；没有网络发送或远端成功回执。"
        }
        UiViews.button(this, body, "清除本页输入与输出", R.id.ui_fixture_clear) {
            input.text.clear()
            output.text = "本页暂无合成输出。"
            localCount = 0
            status.text = "LOCAL_FIXTURE_CLEARED：不删除持久账本，不声称擦除备份。"
        }
        status = UiViews.text(this, body, "本页不恢复网络连接。", R.id.ui_fixture_status)
        output = UiViews.text(this, body, "本页暂无合成输出。", R.id.ui_fixture_output)
        UiViews.button(this, body, "返回状态页（仍需手动恢复连接）", R.id.ui_fixture_back) { finish() }
    }

    override fun onStart() {
        super.onStart()
        foreground = true
        bridge?.attach(this)
        bridge?.snapshot()?.let { onSnapshot(it) }
    }

    override fun onStop() {
        foreground = false
        input.text?.clear()
        bridge?.pauseAndDisconnect()
        bridge?.detach(this)
        super.onStop()
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.clear()
    }

    override fun onSnapshot(snapshot: ClientUiSnapshot) {
        if (Looper.myLooper() != Looper.getMainLooper()) { runOnUiThread { onSnapshot(snapshot) }; return }
        if (!foreground || isFinishing || isDestroyed) return
        status.text = if (snapshot.initialized) "WS：${snapshot.wsState.name} · 本地暂停：${snapshot.paused}\n本页按钮仅修改本页 fixture，不声明远端执行结果。" else
            "状态桥接尚未初始化；本页仅有本地控件，不表示网络能力可用。"
    }
}
