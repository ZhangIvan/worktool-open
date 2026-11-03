/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Service
import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.graphics.PixelFormat
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.provider.Settings
import android.view.Gravity
import android.view.MotionEvent
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.LinearLayout
import android.widget.Toast

/**
 * 悬浮窗控制服务（docs/16 U2 / FR-50 最小集：暂停/恢复两钮，任意界面可操作——
 * 上游 DefaultFloatService 气泡等价；printNode/OCR/回桌面调试项不采纳）。
 *
 * 实现：SYSTEM_ALERT_WINDOW + TYPE_APPLICATION_OVERLAY，[WindowManager.addView]
 * 挂一个可拖动的两钮小面板（暂停 / 恢复），控制接线 = 进程级唯一
 * [SessionRuntimeHolder] 的 PauseManager（与主界面 PausePanel、运行时段调度器
 * 同一状态机，[cn.worktool.open.core.task.PauseManager.pauseSource] 单一事实源）。
 *
 * 恢复 = 两击式确认（[FloatControlUi]/[TwoClickConfirm]：首击钮变「确认恢复？」
 * 3s 内二击生效），FR-52 两步语义不绕过。
 *
 * 权限：缺 overlay 授权 → fail-soft（toast + 跳 ACTION_MANAGE_OVERLAY_PERMISSION
 * 由调用方 MainActivity 引导；本服务自身不持 Activity 上下文跳转设置页）。
 * 开关 = AppSettings.floatWindowEnabled（默认关；设置 tab 切换即启停本服务）。
 */
class FloatControlService : Service() {

    private val mainHandler = Handler(Looper.getMainLooper())
    private val confirm = TwoClickConfirm()
    private var overlay: View? = null
    private lateinit var windowManager: WindowManager

    private val refresh = object : Runnable {
        override fun run() {
            renderButtons()
            mainHandler.postDelayed(this, REFRESH_MILLIS)
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        windowManager = getSystemService(WindowManager::class.java)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!Settings.canDrawOverlays(this)) {
            Toast.makeText(this, R.string.float_no_permission, Toast.LENGTH_LONG).show()
            stopSelf()
            return START_NOT_STICKY
        }
        if (overlay == null) addOverlay()
        mainHandler.removeCallbacks(refresh)
        mainHandler.post(refresh)
        return START_STICKY
    }

    override fun onDestroy() {
        mainHandler.removeCallbacks(refresh)
        removeOverlay()
        super.onDestroy()
    }

    // ---- 悬浮面板 ----

    private fun addOverlay() {
        val density = resources.displayMetrics.density
        val pad = (8 * density).toInt()

        val pauseButton = Button(this).apply { text = getText(R.string.main_pause) }
        val resumeButton = Button(this).apply { text = getText(R.string.float_resume) }
        val panel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setBackgroundColor(Color.argb(230, 26, 28, 32))
            setPadding(pad, pad, pad, pad)
            addView(pauseButton, LinearLayout.LayoutParams(WRAP, WRAP))
            addView(resumeButton, LinearLayout.LayoutParams(WRAP, WRAP).apply { topMargin = pad })
        }

        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE
                or WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS,
            PixelFormat.TRANSLUCENT,
        ).apply {
            gravity = Gravity.TOP or Gravity.END
            x = (12 * density).toInt()
            y = (160 * density).toInt()
        }

        // 整体可拖动（ACTION_MOVE 更新 x/y；APK 气泡贴边停靠的最小等价面）
        var downX = 0f
        var downY = 0f
        var startX = 0
        var startY = 0
        panel.setOnTouchListener { _, e ->
            when (e.actionMasked) {
                MotionEvent.ACTION_DOWN -> {
                    downX = e.rawX; downY = e.rawY
                    startX = params.x; startY = params.y
                    false
                }
                MotionEvent.ACTION_MOVE -> {
                    params.x = startX - (e.rawX - downX).toInt()
                    params.y = startY + (e.rawY - downY).toInt()
                    runCatching { windowManager.updateViewLayout(panel, params) }
                    true
                }
                else -> false
            }
        }

        val manager = SessionRuntimeHolder.get(this).pauseManager
        pauseButton.setOnClickListener {
            FloatControlUi.onPrimary(manager, confirm, System.currentTimeMillis())
            renderButtons()
        }
        resumeButton.setOnClickListener {
            val outcome = FloatControlUi.onPrimary(manager, confirm, System.currentTimeMillis())
            if (outcome == FloatControlUi.Outcome.PAUSED_NOW) confirm.disarm()
            renderButtons()
            toastFor(outcome)
        }

        windowManager.addView(panel, params)
        overlay = panel
    }

    private fun removeOverlay() {
        overlay?.let { runCatching { windowManager.removeView(it) } }
        overlay = null
    }

    private fun renderButtons() {
        val panel = overlay as? LinearLayout ?: return
        val manager = SessionRuntimeHolder.get(this).pauseManager
        val now = System.currentTimeMillis()
        val armed = confirm.isArmed(now)
        val pauseButton = panel.getChildAt(0) as? Button ?: return
        val resumeButton = panel.getChildAt(1) as? Button ?: return
        pauseButton.visibility =
            if (manager.state == cn.worktool.open.core.task.PauseManager.State.RUNNING) View.VISIBLE else View.GONE
        resumeButton.text = getText(
            when (FloatControlUi.primaryAction(manager.state, armed)) {
                FloatControlUi.Action.CONFIRM_RESUME -> R.string.float_resume_confirm
                else -> R.string.float_resume
            },
        )
    }

    private fun toastFor(outcome: FloatControlUi.Outcome) {
        val res = when (outcome) {
            FloatControlUi.Outcome.PAUSED_NOW -> R.string.float_paused_toast
            FloatControlUi.Outcome.RESUME_ASK_CONFIRM -> R.string.float_resume_ask_toast
            FloatControlUi.Outcome.RESUMED_NOW -> R.string.float_resumed_toast
        }
        Toast.makeText(this, res, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val REFRESH_MILLIS = 1_000L
        private const val WRAP = LinearLayout.LayoutParams.WRAP_CONTENT

        fun start(context: Context) =
            context.startService(Intent(context, FloatControlService::class.java))

        fun stop(context: Context) =
            context.stopService(Intent(context, FloatControlService::class.java))
    }
}
