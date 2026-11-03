/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Typeface
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.LayoutInflater
import android.view.View
import android.view.ViewGroup
import android.widget.BaseAdapter
import android.widget.Button
import android.widget.EditText
import android.widget.ListView
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import cn.worktool.open.core.accessibility.WorkToolAccessibilityService
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore

/**
 * 主界面（产品冲刺 A 车道）：状态卡 + 快捷操作 + 底部标签页（状态|指令|设置）。
 *
 * 结构（activity_main.xml）：
 * - 顶部状态卡：机器人在线/离线（绿/灰圆点）+ server 地址 + 最后心跳时间。
 *   在线判定 = 会话在跑 && 连接开 && 最近心跳 ≤ 心跳间隔（默认 15s，随凭据），
 *   数据源 [SessionRuntime.statusTracker]（对 core:protocol V2Session 的传输层观测）。
 *   另有无障碍服务观测行（CR UI-02）：[WorkToolAccessibilityService.instance] 为 null
 *   （服务未开启/未连接）时显示警示 + 跳系统设置入口——此时一切指令执行都会失败
 *   （AccessibilityUiDriver 无通道），不提示则用户无从知道原因。
 * - 中部快捷操作：暂停/恢复（FR-52，直接绑定 task-engine PauseManager；两步恢复
 *   状态机由 [PausePanelState] 映射，不可绕过）+ 干跑开关（FR-54 端侧干跑门，
 *   [SessionRuntime.dryRunEnabled] → DeviceSessionLoop.dryRunGate：下发指令只产出
 *   预览零副作用，回执 metaJson.deviceDryRun=true 标记）。
 *   FR-50 悬浮窗控制（P1）不在本冲刺范围（dodDeferred，见 SessionRuntime 类注）。
 * - 底部标签页：状态（设备/会话信息 + 事件日志）| 指令（P0 目录 + 上次执行状态 +
 *   详情干跑预览 FR-54）| 设置（server 地址 / 断开配对 / 日志开关 / 关于）。
 *
 * 启动路由（ask ⑥）：未配对（无凭据或缺 server 地址）→ [PairingActivity]。
 * SR-10：无任何伪装命名；G3：零遥测（所有数据仅本地 + 用户自配 server 会话）。
 */
class MainActivity : Activity() {

    private lateinit var settings: AppSettings
    private lateinit var credentialStore: AndroidKeystoreCredentialStore
    private lateinit var runtime: SessionRuntime
    private lateinit var commandAdapter: CommandListAdapter

    private val mainHandler = Handler(Looper.getMainLooper())
    private val tick = object : Runnable {
        override fun run() {
            renderStatusCard()
            renderPausePanel()
            commandAdapter.refresh(runtime.snapshotOutcomeByType())
            mainHandler.postDelayed(this, TICK_MILLIS)
        }
    }

    // 状态卡
    private lateinit var statusDot: View
    private lateinit var statusText: TextView
    private lateinit var statusServer: TextView
    private lateinit var statusHeartbeat: TextView
    private lateinit var statusA11y: TextView
    private lateinit var openA11yButton: Button

    // 快捷操作
    private lateinit var pauseButton: Button
    private lateinit var cancelResumeButton: Button

    // 标签页
    private lateinit var tabStatus: View
    private lateinit var tabCommands: View
    private lateinit var tabSettings: View
    private lateinit var tabButtons: List<TextView>

    // 状态 tab
    private lateinit var sessionDetail: TextView
    private lateinit var eventLog: TextView

    // 设置 tab
    private lateinit var settingsServerValue: TextView
    private lateinit var logSwitch: Switch

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        settings = AppSettings(this)
        credentialStore = AndroidKeystoreCredentialStore(this)

        // 启动路由：未配对 → 配对引导流（ask ⑥）
        if (OnboardingForms.launchRoute(credentialStore, settings.serverAddress) !=
            OnboardingForms.LaunchRoute.MAIN
        ) {
            startActivity(Intent(this, PairingActivity::class.java))
            finish()
            return
        }

        setContentView(R.layout.activity_main)
        runtime = SessionRuntime(this)
        runtime.logEnabled = settings.logEnabled
        runtime.onCredentialRevoked = {
            // 902 凭据撤销（服务端强制重配对）：清凭据 → 引导流
            credentialStore.clear()
            mainHandler.post {
                Toast.makeText(this, R.string.main_credential_revoked, Toast.LENGTH_LONG).show()
                startActivity(
                    Intent(this, PairingActivity::class.java).apply {
                        flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK
                    },
                )
            }
        }
        runtime.onEventLine = { line ->
            mainHandler.post { appendEventLine(line) }
        }

        bindStatusCard()
        bindQuickActions()
        bindTabs()
        bindStatusTab()
        bindCommandsTab()
        bindSettingsTab()
    }

    override fun onStart() {
        super.onStart()
        if (::runtime.isInitialized.not()) return // 未配对路由已 finish
        val creds = credentialStore.load() ?: run {
            startActivity(Intent(this, PairingActivity::class.java))
            finish()
            return
        }
        val baseUrl = settings.serverAddress ?: run {
            startActivity(Intent(this, PairingActivity::class.java))
            finish()
            return
        }
        runtime.logEnabled = settings.logEnabled
        runtime.dryRunEnabled = settings.dryRunEnabled
        runtime.start(creds, baseUrl)
        renderSessionDetail(creds.deviceId, creds.sessionId)
        mainHandler.post(tick)
    }

    override fun onStop() {
        super.onStop()
        mainHandler.removeCallbacks(tick)
    }

    override fun onDestroy() {
        // 会话生命周期 = Activity 生命周期（进程级常驻 = 前台服务任务卡，dodDeferred，
        // 见 SessionRuntime 类注；进程存活期间后台断连由重连机制兜底）。
        if (::runtime.isInitialized) runtime.stop()
        super.onDestroy()
    }

    // ---- 状态卡 ----

    private fun bindStatusCard() {
        statusDot = findViewById(R.id.main_status_dot)
        statusText = findViewById(R.id.main_status_text)
        statusServer = findViewById(R.id.main_status_server)
        statusHeartbeat = findViewById(R.id.main_status_heartbeat)
        statusA11y = findViewById(R.id.main_status_a11y)
        openA11yButton = findViewById(R.id.main_btn_open_a11y)
        // CR UI-02：服务未开启时的一键入口（系统无障碍设置页；用户开启后返回本页，
        // 下一个 tick 即观测到连接并隐藏本入口）。
        openA11yButton.setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }
    }

    private fun renderStatusCard() {
        val snap = runtime.statusTracker.snapshot()
        val status = RobotStatusComputer.compute(
            sessionStarted = snap.sessionStarted,
            connectionOpen = snap.connectionOpen,
            lastHeartbeatAtMillis = snap.lastHeartbeatAtMillis,
            nowMillis = System.currentTimeMillis(),
            heartbeatIntervalMillis = runtime.heartbeatIntervalMillis,
        )
        val (dotRes, textRes) = when (status.stateCode) {
            RobotStatusComputer.STATE_ONLINE ->
                R.drawable.dot_online to R.string.main_status_online
            RobotStatusComputer.STATE_OFFLINE ->
                R.drawable.dot_offline to R.string.main_status_offline
            else -> R.drawable.dot_offline to R.string.main_status_not_started
        }
        statusDot.setBackgroundResource(dotRes)
        statusText.setText(textRes)
        statusServer.text = settings.serverAddress.orEmpty()
        statusHeartbeat.text =
            getString(R.string.main_status_heartbeat, RobotStatusComputer.heartbeatAgeText(status.heartbeatAgeMillis))
        // CR UI-02 无障碍服务观测：null = 未开启/未连接 → 指令通道不可用，须可见可跳转；
        // 服务连接后（含从系统设置返回）自动隐藏。观测点与服务/调试页同源（instance）。
        val a11yConnected = WorkToolAccessibilityService.instance != null
        statusA11y.visibility = if (a11yConnected) View.GONE else View.VISIBLE
        openA11yButton.visibility = if (a11yConnected) View.GONE else View.VISIBLE
    }

    // ---- 快捷操作（FR-52 暂停/恢复 + 悬浮窗开关）----

    private fun bindQuickActions() {
        pauseButton = findViewById(R.id.main_btn_pause)
        cancelResumeButton = findViewById(R.id.main_btn_cancel_resume)
        pauseButton.setOnClickListener {
            val panel = PausePanelState.of(runtime.pauseManager.state)
            PausePanelState.dispatchPrimary(
                runtime.pauseManager, panel.action,
                nowMillis = System.currentTimeMillis(), actor = ACTOR,
            )
            renderPausePanel()
        }
        cancelResumeButton.setOnClickListener {
            runtime.pauseManager.cancelResumeRequest(System.currentTimeMillis())
            renderPausePanel()
        }

        // FR-54 端侧干跑开关：开 = 下发指令只产出「将要做什么」预览（零副作用），
        // 持久化 AppSettings + 注入 SessionRuntime.dryRunEnabled（下次会话生效延续）。
        val dryRunSwitch = findViewById<Switch>(R.id.main_switch_dry_run)
        dryRunSwitch.isChecked = settings.dryRunEnabled
        dryRunSwitch.setOnCheckedChangeListener { _, checked ->
            settings.dryRunEnabled = checked
            runtime.dryRunEnabled = checked
        }
        runtime.dryRunEnabled = settings.dryRunEnabled
    }

    private fun renderPausePanel() {
        val panel = PausePanelState.of(runtime.pauseManager.state)
        pauseButton.setText(
            when (panel.action) {
                PausePanelState.Action.PAUSE -> R.string.main_pause
                PausePanelState.Action.REQUEST_RESUME -> R.string.main_resume_request
                PausePanelState.Action.CONFIRM_RESUME -> R.string.main_resume_confirm
            },
        )
        cancelResumeButton.visibility = if (panel.showCancelResume) View.VISIBLE else View.GONE
    }

    // ---- 底部标签页 ----

    private fun bindTabs() {
        tabStatus = findViewById(R.id.main_tab_status_content)
        tabCommands = findViewById(R.id.main_tab_commands_content)
        tabSettings = findViewById(R.id.main_tab_settings_content)
        tabButtons = listOf(
            findViewById(R.id.main_tab_btn_status),
            findViewById(R.id.main_tab_btn_commands),
            findViewById(R.id.main_tab_btn_settings),
        )
        tabButtons.forEachIndexed { index, button ->
            button.setOnClickListener { selectTab(index) }
        }
        selectTab(0)
    }

    private fun selectTab(index: Int) {
        val contents = listOf(tabStatus, tabCommands, tabSettings)
        contents.forEachIndexed { i, content -> content.visibility = if (i == index) View.VISIBLE else View.GONE }
        tabButtons.forEachIndexed { i, button ->
            val selected = i == index
            button.setTypeface(null, if (selected) Typeface.BOLD else Typeface.NORMAL)
            button.setTextColor(getColor(if (selected) R.color.primary else R.color.text_secondary))
        }
    }

    // ---- 状态 tab：会话信息 + 事件日志 ----

    private fun bindStatusTab() {
        sessionDetail = findViewById(R.id.main_session_detail)
        eventLog = findViewById(R.id.main_event_log)
    }

    private fun renderSessionDetail(deviceId: String, sessionId: String?) {
        sessionDetail.text = getString(R.string.main_session_detail, deviceId, sessionId ?: "—")
    }

    private fun appendEventLine(line: String) {
        if (::eventLog.isInitialized) {
            eventLog.text = "${eventLog.text}$line\n".takeLast(LOG_TAIL_CHARS)
        }
    }

    // ---- 指令 tab：P0 目录 ----

    private fun bindCommandsTab() {
        commandAdapter = CommandListAdapter(CommandCatalog.p0())
        findViewById<ListView>(R.id.main_command_list).apply {
            adapter = commandAdapter
            setOnItemClickListener { _, _, position, _ -> showCommandDetail(commandAdapter.rows[position]) }
        }
    }

    private fun showCommandDetail(row: CommandCatalog.Row) {
        val body = layoutInflater.inflate(R.layout.dialog_command_detail, null as ViewGroup?)
        body.findViewById<TextView>(R.id.cmd_detail_types).text =
            getString(R.string.cmd_detail_types, row.allTypes.joinToString("/"), CommandCatalog.riskLabel(row.risk))
        body.findViewById<TextView>(R.id.cmd_detail_params).text = row.paramsNote
        body.findViewById<TextView>(R.id.cmd_detail_last).text =
            getString(R.string.cmd_detail_last, CommandCatalog.lastStatusText(row.lastOutcome))
        val preview = body.findViewById<TextView>(R.id.cmd_detail_preview)
        val previewButton = body.findViewById<Button>(R.id.cmd_detail_dry_run)
        previewButton.setOnClickListener {
            // FR-54 干跑预览：task-engine DryRunRunner 同引擎同路径，零副作用（本地渲染）
            preview.text = CommandCatalog.dryRunPreview(row) { System.currentTimeMillis() }.toString()
        }
        AlertDialog.Builder(this)
            .setTitle(row.title)
            .setView(body)
            .setPositiveButton(R.string.main_dialog_ok, null)
            .show()
    }

    private inner class CommandListAdapter(initialRows: List<CommandCatalog.Row>) : BaseAdapter() {

        var rows: List<CommandCatalog.Row> = initialRows
            private set

        fun refresh(outcomes: Map<Int, CommandCatalog.Row.Outcome>) {
            rows = CommandCatalog.p0(outcomes)
            notifyDataSetChanged()
        }

        override fun getCount(): Int = rows.size
        override fun getItem(position: Int): CommandCatalog.Row = rows[position]
        override fun getItemId(position: Int): Long = rows[position].commandType.toLong()

        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view = convertView ?: layoutInflater.inflate(R.layout.item_command, parent, false)
            val row = rows[position]
            view.findViewById<TextView>(R.id.cmd_row_title).text = row.title
            view.findViewById<TextView>(R.id.cmd_row_types).text =
                getString(R.string.cmd_row_types, row.allTypes.joinToString("/"), CommandCatalog.riskLabel(row.risk))
            view.findViewById<TextView>(R.id.cmd_row_last).text =
                getString(R.string.cmd_row_last, CommandCatalog.lastStatusText(row.lastOutcome))
            return view
        }
    }

    // ---- 设置 tab ----

    private fun bindSettingsTab() {
        settingsServerValue = findViewById(R.id.main_settings_server_value)
        logSwitch = findViewById(R.id.main_settings_log_switch)

        renderSettingsServer()
        // 关于（版本 + License + 致谢）：版本取 AppInfo.VERSION 单一事实源
        findViewById<TextView>(R.id.main_about_body).text =
            getString(R.string.about_body, AppInfo.VERSION)

        findViewById<Button>(R.id.main_btn_change_server).setOnClickListener { confirmChangeServer() }
        findViewById<Button>(R.id.main_btn_unpair).setOnClickListener { confirmUnpair() }
        findViewById<Button>(R.id.main_btn_open_debug).setOnClickListener {
            startActivity(Intent(this, NodeTreeDebugActivity::class.java))
        }

        logSwitch.isChecked = settings.logEnabled
        logSwitch.setOnCheckedChangeListener { _, checked ->
            settings.logEnabled = checked
            runtime.logEnabled = checked
        }
    }

    private fun renderSettingsServer() {
        settingsServerValue.text = settings.serverAddress.orEmpty()
    }

    /** 修改 server 地址：凭据由原 server 签发，换地址必须重新配对（如实告知）。 */
    private fun confirmChangeServer() {
        val input = EditText(this).apply { hint = getString(R.string.settings_server_hint) }
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_change_server_title)
            .setMessage(R.string.settings_change_server_message)
            .setView(input)
            .setPositiveButton(R.string.settings_change_server_confirm) { _, _ ->
                val verdict = OnboardingForms.validateServerAddress(input.text.toString())
                if (verdict is OnboardingForms.ServerAddress.Invalid) {
                    Toast.makeText(this, R.string.pair_addr_error_format, Toast.LENGTH_LONG).show()
                    return@setPositiveButton
                }
                val normalized = (verdict as OnboardingForms.ServerAddress.Valid).normalized
                if (OnboardingForms.securityCheck(normalized) != null) {
                    Toast.makeText(this, R.string.pair_addr_error_security_generic, Toast.LENGTH_LONG).show()
                    return@setPositiveButton
                }
                settings.serverAddress = normalized
                credentialStore.clear()
                runtime.stop()
                goPairing()
            }
            .setNegativeButton(R.string.main_dialog_cancel, null)
            .show()
    }

    /** 断开配对：清除加密凭据（server 侧凭据仍在，可重新配对复活）。 */
    private fun confirmUnpair() {
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_unpair_title)
            .setMessage(R.string.settings_unpair_message)
            .setPositiveButton(R.string.settings_unpair_confirm) { _, _ ->
                runtime.stop()
                credentialStore.clear()
                settings.serverAddress = null
                goPairing()
            }
            .setNegativeButton(R.string.main_dialog_cancel, null)
            .show()
    }

    private fun goPairing() {
        startActivity(
            Intent(this, PairingActivity::class.java).apply {
                flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK
            },
        )
    }

    private companion object {
        const val TICK_MILLIS = 1_000L
        const val LOG_TAIL_CHARS = 4_000
        const val ACTOR = "app-main"
    }
}
