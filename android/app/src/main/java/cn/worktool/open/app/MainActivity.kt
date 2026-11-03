/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.graphics.Typeface
import android.net.Uri
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
 * 启动路由（BP-1/SP-1 + BP-7/SP-3 接线，判定 [AppLaunchRoute]）：**未授权无障碍 →
 * [SetupGuideActivity]（三卡引导，全绿放行）→ 未配对（无凭据或缺 server 地址）→
 * [PairingActivity] → 已配对 → 主界面 + onStart 自动 [SessionRuntime.start]（幂等，
 * [SessionAutoStart] 判定；会话生命周期归 [SessionForegroundService]，Activity
 * onDestroy 不再停止会话 = 持续在线）**。
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
            if (::queueContent.isInitialized && queueContent.visibility == View.VISIBLE) refreshQueueSegment()
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
    private lateinit var statusRtt: TextView
    private lateinit var statusWecom: TextView
    private lateinit var statusWindow: TextView

    /** U6 企微版本检测结果缓存（PackageManager 查询按 onResume 一次，不随 1s tick 刷新）。 */
    private var wecomResult: WecomCompatCheck.Result = WecomCompatCheck.Result(
        WecomCompatCheck.Verdict.NOT_INSTALLED, null,
    )

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

    // 指令 tab 分段（docs/16 U3）：目录 / 待执行
    private lateinit var commandListView: ListView
    private lateinit var queueContent: View
    private lateinit var queueSummary: TextView
    private lateinit var queueAdapter: DeferredQueueAdapter

    // 设置 tab
    private lateinit var settingsServerValue: TextView
    private lateinit var logSwitch: Switch

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        settings = AppSettings(this)
        credentialStore = AndroidKeystoreCredentialStore(this)

        // 启动路由（SP-1/SP-3 接线）：未授权无障碍 → 引导页 → 未配对 → 配对流 → 已配对 → 主界面
        // U7「不再询问」：无障碍未授权且用户已记 skip → 不再强制引导页（降级按配对态；
        // 状态卡警示行持续可见，安全边界见 AppLaunchRoute.decide KDoc）。
        val paired = OnboardingForms.launchRoute(credentialStore, settings.serverAddress) ==
            OnboardingForms.LaunchRoute.MAIN
        when (
            AppLaunchRoute.decide(
                paired,
                AccessibilityServiceGate.isAuthorized(this),
                settings.guideSkipAccessibility,
            )
        ) {
            AppLaunchRoute.Route.SETUP_GUIDE -> {
                startActivity(Intent(this, SetupGuideActivity::class.java))
                finish()
                return
            }
            AppLaunchRoute.Route.PAIRING -> {
                startActivity(Intent(this, PairingActivity::class.java))
                finish()
                return
            }
            AppLaunchRoute.Route.MAIN -> Unit
        }

        setContentView(R.layout.activity_main)
        // 进程级唯一运行时（SessionRuntimeHolder）：会话生命周期归前台服务，
        // 主界面只是观测面/控制面（BP-5/SP-2 改造）。
        runtime = SessionRuntimeHolder.get(this)
        runtime.logEnabled = settings.logEnabled
        runtime.onCredentialRevoked = {
            // 902 凭据撤销（服务端强制重配对）：停会话（连带停前台服务）→ 清凭据 → 引导流。
            // post 到主线程执行（回调发生在回路线程内，避免 stop() 重入自身线程）。
            mainHandler.post {
                runtime.stop()
                credentialStore.clear()
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
        // U4 TTL 门 + 采纳项 #12 重试次数（AppSettings → SessionRuntime → DeviceSessionLoop）。
        runtime.expiredTtlMillis = if (settings.skipExpiredEnabled) settings.expiredTtlHours * 3_600_000L else null
        runtime.retryCount = settings.retryCount
        // U6 企微版本检测缓存刷新（onResume 一次；安装/升级企微后回到前台自然刷新）。
        wecomResult = WecomCompatCheck.classify(WecomCompatCheck.readInstalledVersion(packageManager))
        // U2 悬浮窗：开关开 + 已授权 → 拉起（进程重启后恢复常驻气泡）。
        if (settings.floatWindowEnabled && Settings.canDrawOverlays(this)) {
            FloatControlService.start(this)
        }
        // BP-1/SP-1 ②：已配对 → 自动恢复会话。幂等（SessionAutoStart.KEEP_RUNNING =
        // 在跑不重启，避免每次 onStart 重拨 WS 抖动）；重配对的新会话由 PairingActivity
        // 成功回调显式 stop-then-start。前台服务随 start/stop 由 SessionRuntime.foreground 钩子联动。
        when (SessionAutoStart.decide(runtime.isRunning(), creds, baseUrl)) {
            SessionAutoStart.Decision.START -> runtime.start(creds, baseUrl)
            else -> Unit // KEEP_RUNNING / SKIP_*：路由层已保证主界面只在已配对时可达
        }
        // CR Q4：主界面路径补 POST_NOTIFICATIONS 一次性请求——引导页只在「无障碍未授权」
        // 时可达，已开无障碍的设备永不进引导页 → API 33+ 常驻通知（「机器人运行中」）
        // 会被静默抑制。会话在跑时请求一次（进程级共享门，拒绝后不重复骚扰，与引导页同语义）。
        if (runtime.isRunning()) {
            NotificationPermissionGate.requestOnce(this)
        }
        renderSessionDetail(creds.deviceId, creds.sessionId)
        mainHandler.post(tick)
    }

    override fun onStop() {
        super.onStop()
        mainHandler.removeCallbacks(tick)
    }

    override fun onDestroy() {
        // BP-5/SP-2：会话生命周期归 SessionForegroundService（前台服务常驻），Activity
        // 销毁**不再**停止会话（离开界面/熄屏后机器人仍在线）。显式停止只在：断开配对/
        // 修改 server（设置页）/ 902 凭据撤销。进程被系统回收时服务自愈重启见
        // SessionForegroundService.selfHealSessionIfNeeded。
        super.onDestroy()
    }

    // ---- 状态卡 ----

    private fun bindStatusCard() {
        statusDot = findViewById(R.id.main_status_dot)
        statusText = findViewById(R.id.main_status_text)
        statusServer = findViewById(R.id.main_status_server)
        statusHeartbeat = findViewById(R.id.main_status_heartbeat)
        statusRtt = findViewById(R.id.main_status_rtt)
        statusWecom = findViewById(R.id.main_status_wecom)
        statusWindow = findViewById(R.id.main_status_window)
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
        // 采纳项 #14：离线态显式「重连中(第N次)」（重连计数经 V2Session 透出；
        // 不弹模态框，状态卡文案承载）。
        val attempts = runtime.reconnectAttempts()
        if (status.stateCode == RobotStatusComputer.STATE_OFFLINE && attempts > 0) {
            statusText.text = getString(R.string.main_status_reconnecting, attempts)
        } else {
            statusText.setText(textRes)
        }
        statusServer.text = settings.serverAddress.orEmpty()
        statusHeartbeat.text =
            getString(R.string.main_status_heartbeat, RobotStatusComputer.heartbeatAgeText(status.heartbeatAgeMillis))
        // 采纳项 #8：RTT 彩条（<100ms 绿 / <999ms 黄 / 其余红；观测口径见
        // SessionStatusTracker.Snapshot.lastRttMillis 注——出向→入向配对上界近似）。
        val rtt = snap.lastRttMillis
        statusRtt.text = getString(R.string.main_status_rtt, rtt ?: -1L)
        statusRtt.setTextColor(
            getColor(
                when (RobotStatusComputer.rttBand(rtt)) {
                    RobotStatusComputer.RttBand.GREEN -> R.color.status_online
                    RobotStatusComputer.RttBand.YELLOW -> R.color.status_warn
                    RobotStatusComputer.RttBand.RED -> R.color.status_error
                    RobotStatusComputer.RttBand.UNKNOWN -> R.color.status_offline
                },
            ),
        )
        statusRtt.visibility = if (rtt == null) View.GONE else View.VISIBLE
        renderWecomRow()
        // U1：非时段内休眠行（非模态；判定源 = 会话的运行时段调度器当前口径）。
        val windowEnabled = settings.runtimeWindowEnabled
        val outOfWindow = windowEnabled &&
            runtime.windowScheduler.currentVerdict(System.currentTimeMillis()) ==
            cn.worktool.open.core.task.WindowVerdict.OUT_OF_WINDOW
        statusWindow.visibility = if (outOfWindow) View.VISIBLE else View.GONE
        // CR UI-02 无障碍服务观测：null = 未开启/未连接 → 指令通道不可用，须可见可跳转；
        // 服务连接后（含从系统设置返回）自动隐藏。观测点与服务/调试页同源（instance）。
        val a11yConnected = WorkToolAccessibilityService.instance != null
        statusA11y.visibility = if (a11yConnected) View.GONE else View.VISIBLE
        openA11yButton.visibility = if (a11yConnected) View.GONE else View.VISIBLE
    }

    /** U6 企微版本行（docs/05 兼容矩阵只读副本；onResume 重查，tick 不重复查 PackageManager）。 */
    private fun renderWecomRow() {
        val r = wecomResult
        when (r.verdict) {
            WecomCompatCheck.Verdict.NOT_INSTALLED -> {
                statusWecom.text = getString(R.string.main_wecom_not_installed)
                statusWecom.setTextColor(getColor(R.color.status_offline))
            }
            WecomCompatCheck.Verdict.COMPATIBLE -> {
                statusWecom.text = getString(R.string.main_wecom_compatible, r.versionName)
                statusWecom.setTextColor(getColor(R.color.status_online))
            }
            WecomCompatCheck.Verdict.UNVERIFIED, WecomCompatCheck.Verdict.UNKNOWN -> {
                statusWecom.text = getString(R.string.main_wecom_unverified, r.versionName ?: "?")
                statusWecom.setTextColor(getColor(R.color.status_warn))
            }
            WecomCompatCheck.Verdict.INCOMPATIBLE -> {
                statusWecom.text = getString(R.string.main_wecom_incompatible, r.versionName)
                statusWecom.setTextColor(getColor(R.color.status_error))
            }
        }
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

    // ---- 指令 tab：目录 / 待执行 分段（docs/16 U3）----

    private fun bindCommandsTab() {
        commandAdapter = CommandListAdapter(CommandCatalog.p0())
        commandListView = findViewById(R.id.main_command_list)
        commandListView.adapter = commandAdapter
        commandListView.setOnItemClickListener { _, _, position, _ -> showCommandDetail(commandAdapter.rows[position]) }

        queueContent = findViewById(R.id.main_cmd_queue_content)
        queueSummary = findViewById(R.id.main_queue_summary)
        queueAdapter = DeferredQueueAdapter()
        findViewById<ListView>(R.id.main_queue_list).adapter = queueAdapter

        findViewById<Button>(R.id.main_cmd_seg_catalog).setOnClickListener { showCommandsSegment(catalog = true) }
        findViewById<Button>(R.id.main_cmd_seg_queue).setOnClickListener { showCommandsSegment(catalog = false) }
        findViewById<Button>(R.id.main_btn_clear_queue).setOnClickListener { confirmClearQueue() }
        showCommandsSegment(catalog = true)
    }

    /** 目录/待执行分段切换（U3；APK「查看待执行消息列表」对话框的页面化等价）。 */
    private fun showCommandsSegment(catalog: Boolean) {
        commandListView.visibility = if (catalog) View.VISIBLE else View.GONE
        queueContent.visibility = if (catalog) View.GONE else View.VISIBLE
        if (!catalog) refreshQueueSegment()
    }

    private fun refreshQueueSegment() {
        val deferred = runtime.pauseManager.deferred()
        queueAdapter.rows = deferred
        queueSummary.text = getString(R.string.main_queue_summary, deferred.size)
    }

    /** 清空待执行队列（确认对话框 → PauseManager.discardDeferred，SR-12 全量审计）。 */
    private fun confirmClearQueue() {
        val count = runtime.pauseManager.deferred().size
        if (count == 0) {
            Toast.makeText(this, R.string.main_queue_cleared_toast, Toast.LENGTH_SHORT).show()
            refreshQueueSegment()
            return
        }
        AlertDialog.Builder(this)
            .setTitle(R.string.main_queue_clear_title)
            .setMessage(getString(R.string.main_queue_clear_message, count))
            .setPositiveButton(R.string.main_queue_clear_confirm) { _, _ ->
                runtime.discardDeferredQueue(reason = "user-clear")
                Toast.makeText(this, R.string.main_queue_cleared_toast, Toast.LENGTH_SHORT).show()
                refreshQueueSegment()
            }
            .setNegativeButton(R.string.main_dialog_cancel, null)
            .show()
    }

    /** 待执行队列行适配器（U3：commandId/type/入队时间；数据源 PauseManager.deferred）。 */
    private inner class DeferredQueueAdapter : BaseAdapter() {

        var rows: List<cn.worktool.open.core.task.PauseManager.DeferredCommand> = emptyList()
            set(value) {
                field = value
                notifyDataSetChanged()
            }

        override fun getCount(): Int = rows.size
        override fun getItem(position: Int) = rows[position]
        override fun getItemId(position: Int): Long = position.toLong()

        override fun getView(position: Int, convertView: View?, parent: ViewGroup): View {
            val view = convertView ?: layoutInflater.inflate(R.layout.item_command, parent, false)
            val row = rows[position]
            val fmt = java.text.SimpleDateFormat("MM-dd HH:mm:ss", java.util.Locale.US)
                .format(java.util.Date(row.receivedAtMillis))
            view.findViewById<TextView>(R.id.cmd_row_title).text =
                getString(R.string.main_queue_item, row.commandId, row.commandType)
            view.findViewById<TextView>(R.id.cmd_row_types).text = fmt
            view.findViewById<TextView>(R.id.cmd_row_last).text = ""
            return view
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

        bindRuntimeWindowSettings()
        bindFloatWindowSwitch()
        bindSkipExpiredSettings()
        bindRetryCountSetting()
        findViewById<Button>(R.id.main_btn_stop_session).setOnClickListener { confirmStopSession() }
    }

    /** U1 运行时段设置区：总开关 + 双 TimePicker（默认 8:00–22:00，跨零点支持）。 */
    private fun bindRuntimeWindowSettings() {
        val windowSwitch = findViewById<Switch>(R.id.main_settings_window_switch)
        val startButton = findViewById<Button>(R.id.main_btn_window_start)
        val endButton = findViewById<Button>(R.id.main_btn_window_end)
        val summary = findViewById<TextView>(R.id.main_settings_window_summary)

        fun render() {
            windowSwitch.isChecked = settings.runtimeWindowEnabled
            startButton.text = getString(R.string.settings_window_start, minuteText(settings.runtimeWindowStartMinute))
            endButton.text = getString(R.string.settings_window_end, minuteText(settings.runtimeWindowEndMinute))
            summary.text = if (settings.runtimeWindowEnabled) {
                getString(
                    R.string.settings_window_summary_on,
                    minuteText(settings.runtimeWindowStartMinute),
                    minuteText(settings.runtimeWindowEndMinute),
                )
            } else {
                getString(R.string.settings_window_summary_off)
            }
        }

        windowSwitch.setOnCheckedChangeListener { _, checked ->
            settings.runtimeWindowEnabled = checked
            // 配置变更即重置调度器基线（避免用旧窗口态误判边界迁移）
            runtime.windowScheduler.rebase(System.currentTimeMillis())
            render()
        }
        startButton.setOnClickListener {
            pickTime(settings.runtimeWindowStartMinute) { minute ->
                settings.runtimeWindowStartMinute = minute
                runtime.windowScheduler.rebase(System.currentTimeMillis())
                render()
            }
        }
        endButton.setOnClickListener {
            pickTime(settings.runtimeWindowEndMinute) { minute ->
                settings.runtimeWindowEndMinute = minute
                runtime.windowScheduler.rebase(System.currentTimeMillis())
                render()
            }
        }
        render()
    }

    private fun pickTime(currentMinute: Int, onPicked: (Int) -> Unit) {
        android.app.TimePickerDialog(
            this,
            { _, hour, minute -> onPicked(hour * 60 + minute) },
            currentMinute / 60,
            currentMinute % 60,
            true,
        ).show()
    }

    private fun minuteText(minuteOfDay: Int): String =
        "${minuteOfDay / 60}:${"%02d".format(minuteOfDay % 60)}"

    /** U2 悬浮窗开关：缺 overlay 授权跳系统设置页（复用引导式授权，fail-soft）。 */
    private fun bindFloatWindowSwitch() {
        val floatSwitch = findViewById<Switch>(R.id.main_settings_float_switch)
        floatSwitch.isChecked = settings.floatWindowEnabled
        floatSwitch.setOnCheckedChangeListener { _, checked ->
            settings.floatWindowEnabled = checked
            if (checked) {
                if (Settings.canDrawOverlays(this)) {
                    FloatControlService.start(this)
                } else {
                    Toast.makeText(this, R.string.float_no_permission, Toast.LENGTH_LONG).show()
                    startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:$packageName")))
                }
            } else {
                FloatControlService.stop(this)
            }
        }
    }

    /** U4 跳过过期指令：开关 + TTL 小时数（1/2/4/8 单选）。 */
    private fun bindSkipExpiredSettings() {
        val skipSwitch = findViewById<Switch>(R.id.main_settings_skip_expired_switch)
        val ttlButton = findViewById<Button>(R.id.main_btn_ttl_hours)
        skipSwitch.isChecked = settings.skipExpiredEnabled
        skipSwitch.setOnCheckedChangeListener { _, checked ->
            settings.skipExpiredEnabled = checked
            runtime.expiredTtlMillis = if (checked) settings.expiredTtlHours * 3_600_000L else null
        }
        val hoursOptions = intArrayOf(1, 2, 4, 8)
        ttlButton.text = getString(R.string.settings_ttl_button, settings.expiredTtlHours)
        ttlButton.setOnClickListener {
            val labels = hoursOptions.map { "$it 小时" }.toTypedArray()
            val checkedIndex = hoursOptions.indexOf(settings.expiredTtlHours).takeIf { it >= 0 } ?: 1
            AlertDialog.Builder(this)
                .setTitle(R.string.settings_ttl_title)
                .setSingleChoiceItems(labels, checkedIndex) { dialog, which ->
                    settings.expiredTtlHours = hoursOptions[which]
                    runtime.expiredTtlMillis =
                        if (settings.skipExpiredEnabled) hoursOptions[which] * 3_600_000L else null
                    ttlButton.text = getString(R.string.settings_ttl_button, hoursOptions[which])
                    dialog.dismiss()
                }
                .setNegativeButton(R.string.main_dialog_cancel, null)
                .show()
        }
    }

    /** 采纳项 #12：执行失败重试次数（0/1/2/3/5 单选；0 = 首错即停）。 */
    private fun bindRetryCountSetting() {
        val retryButton = findViewById<Button>(R.id.main_btn_retry_count)
        val options = intArrayOf(0, 1, 2, 3, 5)
        fun render() {
            retryButton.text = getString(R.string.settings_retry_button, settings.retryCount)
        }
        retryButton.setOnClickListener {
            val labels = options.map { if (it == 0) "0（不重试）" else "$it 次" }.toTypedArray()
            val checkedIndex = options.indexOf(settings.retryCount).takeIf { it >= 0 } ?: 0
            AlertDialog.Builder(this)
                .setTitle(R.string.settings_retry_title)
                .setSingleChoiceItems(labels, checkedIndex) { dialog, which ->
                    settings.retryCount = options[which]
                    runtime.retryCount = options[which]
                    render()
                    dialog.dismiss()
                }
                .setNegativeButton(R.string.main_dialog_cancel, null)
                .show()
        }
        render()
    }

    /** 采纳项 #11 停止会话（确认后 SessionRuntime.stop；不用 System.exit）。 */
    private fun confirmStopSession() {
        AlertDialog.Builder(this)
            .setTitle(R.string.settings_stop_session)
            .setMessage(getString(R.string.session_stopped_toast))
            .setPositiveButton(R.string.settings_stop_session) { _, _ ->
                runtime.stop()
                Toast.makeText(this, R.string.session_stopped_toast, Toast.LENGTH_LONG).show()
            }
            .setNegativeButton(R.string.main_dialog_cancel, null)
            .show()
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
