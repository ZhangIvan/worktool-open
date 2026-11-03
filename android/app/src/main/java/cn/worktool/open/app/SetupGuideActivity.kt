/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.TextView
import android.widget.Toast
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore

/**
 * 设置引导页（BP-7/SP-3，配对前置页）：三张检测卡，全绿才放行（「完成，去配对」）。
 *
 * - 卡 a 无障碍服务：按钮直达系统无障碍设置页（ACTION_ACCESSIBILITY_SETTINGS）+
 *   **5s 轮询** enabled_accessibility_services 检测我方服务 → 绿勾
 *   （判据 [AccessibilityServiceGate]，与 MainActivity 路由同源）；
 * - 卡 b 电池优化豁免：ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS 直达授权对话框，
 *   PowerManager.isIgnoringBatteryOptimizations 检测；部分 ROM 裁剪该入口 → fail-soft
 *   提示手动路径；
 * - 卡 c MIUI 自启动：静态指引文案（安全中心 → 应用管理 → 自启动）。自启动授权
 *   无公开检测 API → MIUI 设备显示「我已完成设置」手动确认（如实标注用户自述）；
 *   非 MIUI 设备 NOT_APPLICABLE 自动绿（检测不到就只显示指引，docs/14 SP-3）。
 *
 * API 33+ POST_NOTIFICATIONS 运行时请求并入本页（前台服务常驻通知需要；每次进程
 * 生命周期只请求一次，拒绝后不重复骚扰——系统自身也会节流）。请求门为进程级共享
 * [NotificationPermissionGate]（CR Q4）：主界面路径（已开无障碍、不进本页）同样有
 * 一次性请求，两入口共享「本进程只弹一次」标记。
 *
 * 放行去向由 [AppLaunchRoute.destinationAfterGuide] 决定：已配对 → 主界面（不重复
 * 配对）；未配对 → 配对引导流。卡状态机 = [SetupGuideCards]（纯 JVM 可测）。
 */
class SetupGuideActivity : Activity() {

    private val cards = SetupGuideCards.initial().toMutableMap()
    private val mainHandler = Handler(Looper.getMainLooper())
    private val miuiDevice = MiuiAutostartGuide.isMiuiDevice(Build.MANUFACTURER)

    private lateinit var a11yIcon: TextView
    private lateinit var a11yStatus: TextView
    private lateinit var a11yAction: Button
    private lateinit var batteryIcon: TextView
    private lateinit var batteryStatus: TextView
    private lateinit var batteryAction: Button
    private lateinit var miuiIcon: TextView
    private lateinit var miuiStatus: TextView
    private lateinit var miuiAction: Button
    private lateinit var doneButton: Button

    /** 5s 轮询（onResume 起，onPause 停）。 */
    private val poll = object : Runnable {
        override fun run() {
            refreshDetections()
            mainHandler.postDelayed(this, POLL_MILLIS)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_setup_guide)

        a11yIcon = findViewById(R.id.guide_a11y_icon)
        a11yStatus = findViewById(R.id.guide_a11y_status)
        a11yAction = findViewById(R.id.guide_a11y_action)
        batteryIcon = findViewById(R.id.guide_battery_icon)
        batteryStatus = findViewById(R.id.guide_battery_status)
        batteryAction = findViewById(R.id.guide_battery_action)
        miuiIcon = findViewById(R.id.guide_miui_icon)
        miuiStatus = findViewById(R.id.guide_miui_status)
        miuiAction = findViewById(R.id.guide_miui_action)
        doneButton = findViewById(R.id.guide_done)

        a11yAction.setOnClickListener { openAccessibilitySettings() }
        batteryAction.setOnClickListener { requestIgnoreBatteryOptimizations() }
        miuiAction.setOnClickListener {
            // 用户手动确认（系统不可检测；如实标注「用户自述」，不冒充系统检测）
            cards[SetupGuideCards.CardKey.MIUI_AUTOSTART] = SetupGuideCards.confirmManually(
                cards.getValue(SetupGuideCards.CardKey.MIUI_AUTOSTART),
            )
            render()
        }
        doneButton.setOnClickListener { finishGuide() }

        requestPostNotificationsOnce()
        refreshDetections()
    }

    override fun onResume() {
        super.onResume()
        refreshDetections()
        mainHandler.post(poll)
    }

    override fun onPause() {
        super.onPause()
        mainHandler.removeCallbacks(poll)
    }

    // ---- 检测刷新（5s 轮询 + 关键时机）----

    private fun refreshDetections() {
        // 卡 a：enabled_accessibility_services 含我方服务
        cards[SetupGuideCards.CardKey.ACCESSIBILITY] = SetupGuideCards.Card(
            SetupGuideCards.CardKey.ACCESSIBILITY,
            if (AccessibilityServiceGate.isAuthorized(this)) {
                SetupGuideCards.Detection.GREEN
            } else {
                SetupGuideCards.Detection.PENDING
            },
        )
        // 卡 b：电池优化白名单
        val power = getSystemService(PowerManager::class.java)
        val batteryExempt = power?.isIgnoringBatteryOptimizations(packageName) == true
        cards[SetupGuideCards.CardKey.BATTERY_OPTIMIZATION] = SetupGuideCards.Card(
            SetupGuideCards.CardKey.BATTERY_OPTIMIZATION,
            if (batteryExempt) SetupGuideCards.Detection.GREEN else SetupGuideCards.Detection.PENDING,
        )
        // 卡 c：自启动授权不可检测——MIUI = PENDING（待手动确认）；非 MIUI = 不适用自动绿。
        // 保留既有 manualConfirmed（检测刷新不清掉用户确认）。
        val previous = cards.getValue(SetupGuideCards.CardKey.MIUI_AUTOSTART)
        cards[SetupGuideCards.CardKey.MIUI_AUTOSTART] = SetupGuideCards.Card(
            SetupGuideCards.CardKey.MIUI_AUTOSTART,
            if (miuiDevice) SetupGuideCards.Detection.PENDING else SetupGuideCards.Detection.NOT_APPLICABLE,
            manualConfirmed = previous.manualConfirmed,
        )
        render()
    }

    // ---- 渲染 ----

    private fun render() {
        val a11yCard = cards.getValue(SetupGuideCards.CardKey.ACCESSIBILITY)
        val a11yGreen = SetupGuideCards.isGreen(a11yCard)
        a11yIcon.text = if (a11yGreen) "✅" else "⚠️"
        a11yStatus.text = getString(if (a11yGreen) R.string.guide_card_a11y_on else R.string.guide_card_a11y_off)
        a11yStatus.setTextColor(getColor(if (a11yGreen) R.color.status_online else R.color.status_offline))
        a11yAction.visibility = if (a11yGreen) View.GONE else View.VISIBLE

        val batteryCard = cards.getValue(SetupGuideCards.CardKey.BATTERY_OPTIMIZATION)
        val batteryGreen = SetupGuideCards.isGreen(batteryCard)
        batteryIcon.text = if (batteryGreen) "✅" else "⚠️"
        batteryStatus.text =
            getString(if (batteryGreen) R.string.guide_card_battery_on else R.string.guide_card_battery_desc)
        batteryStatus.setTextColor(getColor(if (batteryGreen) R.color.status_online else R.color.status_offline))
        batteryAction.visibility = if (batteryGreen) View.GONE else View.VISIBLE

        val miuiCard = cards.getValue(SetupGuideCards.CardKey.MIUI_AUTOSTART)
        val miuiGreen = SetupGuideCards.isGreen(miuiCard)
        miuiIcon.text = if (miuiGreen) "✅" else "ℹ️"
        miuiStatus.text = getString(
            when {
                miuiCard.detection == SetupGuideCards.Detection.NOT_APPLICABLE -> R.string.guide_card_miui_na
                miuiCard.manualConfirmed -> R.string.guide_card_miui_confirmed
                else -> R.string.guide_card_miui_desc
            },
        )
        miuiStatus.setTextColor(
            getColor(
                when {
                    miuiCard.detection == SetupGuideCards.Detection.NOT_APPLICABLE -> R.color.status_online
                    miuiCard.manualConfirmed -> R.color.status_online
                    else -> R.color.text_secondary
                },
            ),
        )
        miuiAction.visibility =
            if (!miuiDevice || miuiCard.manualConfirmed) View.GONE else View.VISIBLE

        val allGreen = SetupGuideCards.allGreen(cards.values)
        doneButton.isEnabled = allGreen
        val paired = OnboardingForms.launchRoute(
            AndroidKeystoreCredentialStore(this),
            AppSettings(this).serverAddress,
        ) == OnboardingForms.LaunchRoute.MAIN
        doneButton.setText(if (paired) R.string.guide_done_to_main else R.string.guide_done)
    }

    // ---- 各卡动作 ----

    private fun openAccessibilitySettings() {
        startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
    }

    @SuppressLint("BatteryLife") // 系统文档要求显式标注：白名单意图须用户知情授权
    private fun requestIgnoreBatteryOptimizations() {
        val intent = Intent(
            Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
            Uri.parse("package:$packageName"),
        )
        // 部分 ROM 裁剪该入口 → fail-soft（不 crash，引导手动路径）
        runCatching { startActivity(intent) }.onFailure {
            Toast.makeText(this, R.string.guide_card_battery_failed, Toast.LENGTH_LONG).show()
        }
    }

    // ---- API 33+ 通知运行时权限（并入引导页；进程级共享门，见 [NotificationPermissionGate]）----

    private fun requestPostNotificationsOnce() {
        NotificationPermissionGate.requestOnce(this)
    }

    // ---- 放行 ----

    private fun finishGuide() {
        if (!SetupGuideCards.allGreen(cards.values)) return // 按钮态兜底
        val paired = OnboardingForms.launchRoute(
            AndroidKeystoreCredentialStore(this),
            AppSettings(this).serverAddress,
        ) == OnboardingForms.LaunchRoute.MAIN
        val destination = AppLaunchRoute.destinationAfterGuide(paired)
        val target = if (destination == AppLaunchRoute.Route.MAIN) {
            MainActivity::class.java
        } else {
            PairingActivity::class.java
        }
        startActivity(
            Intent(this, target).apply {
                flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK
            },
        )
        finish()
    }

    private companion object {
        const val POLL_MILLIS = 5_000L
    }
}
