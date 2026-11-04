/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app.lab

import android.app.Activity
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import cn.worktool.open.app.AccessibilityUiDriver
import cn.worktool.open.app.DevicePortImpls
import cn.worktool.open.app.LogStoreWriter
import kotlinx.serialization.json.put
import cn.worktool.open.app.RuntimePortHub
import cn.worktool.open.app.DeviceSessionLoop
import cn.worktool.open.app.R
import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.crypto.Ed25519Keys
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.X25519
import cn.worktool.open.core.protocol.pair.PairRequest
import cn.worktool.open.core.protocol.pair.PairingClient
import cn.worktool.open.core.protocol.session.SessionCrypto
import cn.worktool.open.core.protocol.transport.EndpointPolicy
import cn.worktool.open.core.protocol.transport.Scheduler
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore
import cn.worktool.open.core.protocol.android.SqliteJournalStore
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.query.FileInfo
import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager
import java.io.File
import java.net.URI
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * lab 配对最小面（W-M3R 真机装配；仅 labDebug 构型，产品源集零改动——G12）。
 *
 * 流程（spec §5.1/§5.3）：
 * 1. EndpointPolicy 注册 lab 裸 IP 主机例外（仅该 host；明文不可豁免，SR-09）；
 * 2. PairingClient.pair：Ed25519 签名公钥 + X25519 临时公钥 + 6 位一次性配对码；
 * 3. 凭据经 AndroidKeystoreCredentialStore 加密落盘（SR-05/G5）；
 * 4. SessionCrypto 派生会话密钥 → V2Session.start() 发 hello（服务端可验
 *    HMAC/seq/eventId 全链），等待 hello_ack。
 *
 * W-M4 升级（G-1 取证接线）：
 * 1. Ed25519 改走 [Ed25519Keys] 三级解析且**不注入 lab 资产**——本设备（Android 12
 *    JCA 无 Ed25519）落到纯 JVM 回退库生成：UI 显示「设备生成密钥」（T2.6 装配）；
 *    回退种子经 [LabEd25519SeedStore] Keystore AES-GCM 加密持久化（重复配对公钥稳定）。
 * 2. 配对成功后不再直接起裸 V2Session，而是装配 [DeviceSessionLoop]（T3.4 执行回路）：
 *    journal=SQLite 落盘 + ExecutorRegistry 首批全量 + 真机 [AccessibilityUiDriver]，
 *    指令帧经门禁→执行器→RAW_CONFIRM 全链可视（onEvent 逐行入 UI 日志）。
 */
class LabPairingActivity : Activity() {

    private var loop: DeviceSessionLoop? = null

    /** RIG 自动化 prefill extras（见 onCreate 注释；仅 labDebug）。 */
    private companion object {
        const val EXTRA_PREFILL_HOST = "lab_prefill_host"
        const val EXTRA_PREFILL_CODE = "lab_prefill_code"
    }

    /** 进程级会话标识（301 快照 processSessionId；进程存活期恒定）。 */
    private val processSessionId: String = java.util.UUID.randomUUID().toString()
    private val timeFmt = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_lab_pairing)
        // RIG 自动化通道（会话 21；仅 labDebug 源集，产品零改动）：adb am start
        // 显式 prefill——MIUI 恢复已销毁 Activity 的视图状态（含陈旧/损坏的输入
        // 内容），uiautomator 在本机不可用，触摸注入被恢复态污染。显式 extra
        // 覆盖恢复态 = RIG 可靠装配面。无 extra = 人工输入路径不变。
        intent?.getStringExtra(EXTRA_PREFILL_HOST)?.let {
            findViewById<EditText>(R.id.lab_host).setText(it)
        }
        intent?.getStringExtra(EXTRA_PREFILL_CODE)?.let {
            findViewById<EditText>(R.id.lab_code).setText(it)
        }
        findViewById<Button>(R.id.lab_pair_button).setOnClickListener { pairAsync() }
    }

    private fun pairAsync() {
        val host = findViewById<EditText>(R.id.lab_host).text.toString().trim()
        val code = findViewById<EditText>(R.id.lab_code).text.toString().trim()
        val button = findViewById<Button>(R.id.lab_pair_button)
        button.isEnabled = false
        log("开始配对 → $host（code=****${code.takeLast(2)}）")
        Thread {
            try {
                doPair(host, code)
            } catch (e: Exception) {
                runOnUiThread {
                    log("失败：${e.javaClass.simpleName}: ${e.message}")
                    button.isEnabled = true
                }
            }
        }.start()
    }

    private fun doPair(host: String, code: String) {
        // 0. JCA 能力探针（lab 装配诊断：Android 各版本 Conscrypt 的 Ed25519/X25519
        //    支持不一——T2.5 真机联调 deferred 的实证补位）
        probeJca().forEach { (name, result) -> log("JCA $name → $result") }

        // 1. lab 裸 IP 例外（仅此 host；产品变体从不调用，注册表默认空）
        val uri = URI(host)
        val hostName = uri.host ?: throw IllegalArgumentException("URL 缺 host: $host")
        EndpointPolicy.allowLabRawIpHosts(setOf(hostName))
        log("EndpointPolicy lab 例外已注册：$hostName（仅 TLS；明文不可豁免）")

        // 2. 配对（Ed25519 签名公钥 + X25519 临时对；SR-05）
        //    W-M4（T2.6 装配 + G-1 取证）：改走 Ed25519Keys 三级解析，**不注入 lab 资产**
        //    （externalPublicKeyHex=null）→ 本设备 JCA 不可用即由纯 JVM 回退库在设备侧
        //    生成密钥对（UI 显示「设备生成密钥」）；回退种子 Keystore AES-GCM 加密持久化，
        //    重复配对公钥稳定（旧 lab 注入路径已按 ask 退役）。
        val seedStore = LabEd25519SeedStore(this)
        val signingKey = Ed25519Keys.resolve(
            persistedFallbackSeedHex = seedStore.loadSeedHex(),
        )
        signingKey.fallbackSeedHex?.let {
            seedStore.saveSeedHex(it)
            // 会话 21：同步镜像到产品面 SigningSeedStore——重装/进程重建后的会话自启
            // 走 SessionRuntime（产品装配），其重协商签名器从产品店取种子；双店同种
            // 子 = 配对注册公钥与重协商签名公钥同源（缺此镜像 → 签名公钥漂移 →
            // 服务端 RENEGOTIATE_FAILED，设备永不在线）。
            cn.worktool.open.app.SigningSeedStore(this).saveSeedHex(it)
        }
        when (signingKey.origin) {
            Ed25519Keys.Origin.DEVICE_JCA ->
                log("Ed25519 密钥：设备生成密钥 ✓（JCA 路径）pub=${signingKey.publicHex.take(16)}…")
            Ed25519Keys.Origin.DEVICE_FALLBACK ->
                log("Ed25519 密钥：设备生成密钥 ✓（纯 JVM 回退库 T2.6）pub=${signingKey.publicHex.take(16)}…")
            Ed25519Keys.Origin.EXTERNAL_PUBLIC_ONLY ->
                log("Ed25519 密钥：lab 注入（本构建不应出现——外部资产未传入）")
        }
        val ed25519PublicHex: String = signingKey.publicHex
        val ephemeralPair = X25519.generateKeyPair()
        val androidId = Settings.Secure.getString(contentResolver, Settings.Secure.ANDROID_ID)
        val deviceId = "lab-" + (androidId ?: "unknown").lowercase(Locale.US).take(16)
        val response = PairingClient(host).pair(
            PairRequest(
                pairingCode = code,
                deviceId = deviceId,
                deviceName = "RIG78-lab ${Build.MANUFACTURER} ${Build.MODEL}".take(64),
                ed25519PublicKeyHex = ed25519PublicHex,
                x25519EphemeralPublicKeyHex = Hex.encode(X25519.publicRaw(ephemeralPair.public)),
            ),
        )
        log("配对成功 deviceId=${response.deviceId} sessionId=${response.sessionId}")

        // 3. 凭据加密落盘
        val credentials = DeviceCredentials(
            deviceId = response.deviceId,
            deviceTokenHex = response.deviceTokenHex,
            tokenVersion = response.tokenVersion,
            sessionId = response.sessionId,
            serverX25519PublicKeyHex = response.serverX25519PublicKeyHex,
            rotationIntervalDays = response.rotationIntervalDays,
            heartbeatIntervalSeconds = response.heartbeatIntervalSeconds,
            sessionTimeoutSeconds = response.sessionTimeoutSeconds,
            wsPath = response.wsPath,
        )
        AndroidKeystoreCredentialStore(this).save(credentials)
        log("凭据已加密落盘（AndroidKeystore，SR-05/G5）")

        // 4. 会话密钥派生 + 执行回路（W-M4：DeviceSessionLoop 取代裸 V2Session）
        //    journal=SQLite 落盘（E09 门禁持久化先于副作用）；registry=首批全量执行器；
        //    driver=真机无障碍通道；查询结果端口 → UI 日志（501/502 结构化结果可视）。
        val crypto = SessionCrypto.derive(
            deviceEphemeralPrivate = ephemeralPair.private,
            serverX25519PublicKeyRaw = Hex.decode(response.serverX25519PublicKeyHex),
            sessionId = response.sessionId,
        )
        loop?.stop()
        val labFileDir = File(filesDir, "lab-509").apply { mkdirs() }
        // 2026-11-04 D1 真机校准（会话 9 系列 W-M3R 落地）：通讯录页联系人名 =
        // id/hlo（TextView）、列表容器 = id/l_8（RecyclerView）；最近聊天名 = id/o6j
        // （不同 id 不误收）。混淆 id 随企微构建变化——生产走外置 RuleConfig 下发，
        // 此处为实验室基线设备的程序化注入。
        val calibratedRules = cn.worktool.open.feature.executor.runtime.WeworkScreenRules(
            cn.worktool.open.core.accessibility.rules.RuleConfig(
                version = 1,
                selectors = listOf(
                    cn.worktool.open.core.accessibility.rules.SelectorDef(
                        id = cn.worktool.open.feature.executor.runtime.WeworkSelectorIds.CONTACT_LIST_ITEM,
                        match = cn.worktool.open.core.accessibility.rules.MatchSpec(
                            resourceId = "com.tencent.wework:id/hlo",
                            className = "android.widget.TextView",
                        ),
                    ),
                    cn.worktool.open.core.accessibility.rules.SelectorDef(
                        id = cn.worktool.open.feature.executor.runtime.WeworkSelectorIds.CONTACTS_LIST_CONTAINER,
                        match = cn.worktool.open.core.accessibility.rules.MatchSpec(
                            resourceId = "com.tencent.wework:id/l_8",
                        ),
                    ),
                ),
            ),
        )
        // W-M6：全量批（62 执行器）+ RuntimePortHub 端口装配——lab 与产品
        // （SessionRuntime）同构，真机验证即产品验证。校准规则注入 501/502。
        // 登记边界（审计 2026-11-04）：hub 的 201/202 走 task PauseManager（队列/
        // 暂停语义面），而 lab 回路执行门=NeverPausedPauseManager（W-M4 观测语义）——
        // 201 暂停在 lab 不拦截后续指令执行（产品 SessionRuntime 两处同源无此分叉）。
        val portHub = RuntimePortHub(
            pauseManager = cn.worktool.open.core.task.PauseManager(System::currentTimeMillis),
            clock = System::currentTimeMillis,
            notifier = { errorCode, errorReason, _ ->
                runOnUiThread { log("控制台提示 [$errorCode] $errorReason") }
            },
            onEvent = { line -> log(line) },
        )
        val registry = ExecutorRegistry.defaultFullBatch(
            rules = calibratedRules,
            bindings = cn.worktool.open.feature.executor.ports.ExecutorBindings(
                querySink = QueryResultSink { type, result ->
                    val summary = when (result) {
                        is QueryResult.GroupList -> "群×${result.groups.size}: ${result.groups.take(5)}"
                        is QueryResult.FriendList -> "好友×${result.friends.size}: ${result.friends.take(5)}"
                        is QueryResult.LocalFile -> "${result.fileName} ${result.sizeBytes}B"
                    }
                    log("查询结果 type=$type → $summary")
                    portHub.loop?.collectQueryResult(type, result) // apifox 契约：名单进 rawMsg
                },
                files = object : LocalFilePort {
                    override fun defaultLogFilePath(): String = File(labFileDir, "worktool-lab.log").absolutePath
                    override fun stat(path: String): FileInfo? = File(path).takeIf { it.isFile }?.let {
                        FileInfo(it.name, it.absolutePath, it.length())
                    }
                },
                structured = portHub,
                batch = portHub,
                report = cn.worktool.open.feature.executor.report.MessageReportSink { beans ->
                    portHub.loop?.sendReportBeans(beans) ?: false
                },
                session = portHub,
                loop = portHub,
                console = portHub,
                queue = portHub,
                resources = DevicePortImpls.Resources(applicationContext),
                logs = DevicePortImpls.Logs { labFileDir },
                logUpload = DevicePortImpls.LogUpload { host },
                remoteFiles = DevicePortImpls.RemoteFiles(applicationContext),
                qrJoin = DevicePortImpls.QrJoin(applicationContext),
                appConfig = DevicePortImpls.AppConfig { host },
                corp = DevicePortImpls.CurrentCorp,
            ),
        )
        val l = DeviceSessionLoop(
            credentials = credentials,
            baseUrl = host,
            sessionCrypto = crypto,
            // W-M6：不再注入外部重派器——协议层自持 ephemeral（重建 POST 上送
            // 新公钥+新私钥派生），与产品变体同路径（会话 10 真机实证其前置版）。
            journal = SqliteJournalStore(this),
            scheduler = Scheduler.shared(),
            clock = System::currentTimeMillis,
            registry = registry,
            driverProvider = { AccessibilityUiDriver(foregroundActivityContext = { this }) },
            pauseManager = NeverPausedPauseManager(),
            clientVersion = "0.1.0-lab-wm6",
            onEvent = { line -> log(line) },
            takeBatchOutcomes = portHub::takeBatchOutcomes,
            // 会话 21 自愈（spec §5.5.1）：服务端重启凭据失忆 → 重建 POST 401 →
            // 同一配对签名密钥重协商换发新凭据（signingKey 上方已解析，公钥稳定）。
            renegotiationSigner = { deviceId, ts, nonce ->
                Hex.encode(Ed25519Keys.sign(signingKey, "$deviceId|$ts|$nonce".toByteArray(Charsets.UTF_8)))
            },
        )
        portHub.attach(l)
        l.start()
        loop = l
        // FR-49 ③：WS onOpen 环境快照 301（与产品 SessionRuntime.sendEnvSnapshot301
        // 同口径——lab 直发供真机验证；经 journal 队列连接建立即投递）
        runCatching {
            val dm = resources.displayMetrics
            val bean = cn.worktool.open.core.protocol.WeworkMessageBean(
                type = 301,
                extras = kotlinx.serialization.json.buildJsonObject {
                    put("appVersion", "0.1.0-lab-wm6")
                    put("workVersion", packageManager.getPackageInfo("com.tencent.wework", 0).versionName ?: "?")
                    put("resolution", "${dm.widthPixels}x${dm.heightPixels}")
                    put("processSessionId", processSessionId)
                    put("previousExitReason", "first_run")
                    put(
                        "logHealth",
                        kotlinx.serialization.json.buildJsonObject {
                            put("enabled", true)
                            put("todayFile", LogStoreWriter.fileFor(labFileDir).name)
                            put("todayBytes", LogStoreWriter.fileFor(labFileDir).let { if (it.isFile) it.length() else 0L })
                        },
                    )
                },
            )
            l.sendReportBeans(listOf(bean))
            log("301 环境快照已入上报队列（FR-49 ③）")
        }
        runOnUiThread { log("hello 已发送（ws=${response.wsPath}），等待 hello_ack…") }
    }

    /** JCA 能力探针：逐项报告 KeyPairGenerator/KeyAgreement/Signature 可用性（含 provider 名）。 */
    private fun probeJca(): List<Pair<String, String>> = listOf(
        "KeyPairGenerator Ed25519",
        "KeyPairGenerator EdDSA",
        "Signature Ed25519",
        "KeyPairGenerator X25519",
        "KeyPairGenerator XDH",
        "KeyAgreement X25519",
        "KeyAgreement XDH",
    ).map { label ->
        val kind = label.substringBefore(' ')
        val algo = label.substringAfter(' ')
        val result = try {
            val provider = when (kind) {
                "KeyPairGenerator" -> java.security.KeyPairGenerator.getInstance(algo).provider.name
                "Signature" -> java.security.Signature.getInstance(algo).provider.name
                else -> javax.crypto.KeyAgreement.getInstance(algo).provider.name
            }
            "$provider✓"
        } catch (e: Exception) {
            "${e.javaClass.simpleName}"
        }
        label to result
    }

    private fun log(line: String) {
        // FR-49（W-M6 设备验证接线）：事件同步落盘（日期分区，510/514 的数据源；
        // 产品 SessionRuntime.appendEvent 同型——lab 经此与产品同口径供 510/514 实测）
        LogStoreWriter.appendLine(File(filesDir, "lab-509"), System::currentTimeMillis, line)
        runOnUiThread {
            findViewById<TextView>(R.id.lab_status).let { tv ->
                tv.text = "${tv.text}\n[${timeFmt.format(Date())}] $line".takeLast(4000)
            }
        }
    }

    override fun onDestroy() {
        // 2026-11-03 会话 6/7/8 根因修复：不再在 Activity 销毁时停 loop——
        // Activity 后台被 MIUI 回收 → onDestroy → loop.stop() → WS 断 → 指令永不投递。
        // 正确做法：loop 生命周期归进程级 SessionRuntimeHolder（SP-2 前台服务保活），
        // LabPairingActivity 只是配对入口+观测面。此修复 = lab 版对齐产品版架构。
        // loop 仍由本 Activity 持有引用，但销毁时**不停止**（服务端 45s 超时自然关闭）。
        super.onDestroy()
    }
}
