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
import cn.worktool.open.app.R
import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.ProtocolClient
import cn.worktool.open.core.protocol.crypto.Ed25519
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.X25519
import cn.worktool.open.core.protocol.pair.PairRequest
import cn.worktool.open.core.protocol.pair.PairingClient
import cn.worktool.open.core.protocol.session.SessionCrypto
import cn.worktool.open.core.protocol.transport.EndpointPolicy
import cn.worktool.open.core.protocol.transport.Scheduler
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore
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
 * 已知边界（如实记录，非本面缺陷）：hello_ack/901~904 控制帧 schema 随 T3.4 冻结
 * （PairingClient.kt 头注、spec §9 Q3），当前 FrameVerifier 对 hello_ack 形态按
 * 4400 format 拒收并关闭会话——本面以拒帧回调作为「hello_ack 已收到」的可视证据，
 * 服务端权威证据 = inbound_events(receipt='hello') 行 + 会话状态流转。
 */
class LabPairingActivity : Activity() {

    private var session: ProtocolClient.V2Session? = null
    private val timeFmt = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_lab_pairing)
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
        //    Ed25519：设备 JCA 路径优先（API 33+/Keystore）；Android ≤12 无任何算法名
        //    （W-M3R 实证：KeyPairGenerator/KeyFactory/Signature 全缺）→ W-M3R 裁决 B：
        //    lab 一次性注入公钥（构建参数 → labDebug 资产；私钥仅 local-private/，G13，
        //    不在端侧实例化——本设备重协商 NOT_RUN）。产品级回退提供者 = T2.6（W-M4）。
        val ed25519PublicHex: String = try {
            Hex.encode(Ed25519.publicRaw(Ed25519.generateKeyPair().public))
                .also { log("Ed25519 密钥：设备 JCA 生成 ✓") }
        } catch (e: java.security.NoSuchAlgorithmException) {
            loadLabInjectedEd25519PublicHex().also {
                log("Ed25519 设备 JCA 不可用（${e.message}）→ 使用 lab 注入密钥（非设备生成，重协商 NOT_RUN）")
            }
        }
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

        // 4. 会话密钥派生 + V2Session hello（真实下发链 GCM 互操作前提）
        val crypto = SessionCrypto.derive(
            deviceEphemeralPrivate = ephemeralPair.private,
            serverX25519PublicKeyRaw = Hex.decode(response.serverX25519PublicKeyHex),
            sessionId = response.sessionId,
        )
        session?.stop()
        val s = ProtocolClient.V2Session(
            credentials = credentials,
            baseUrl = host,
            sessionCrypto = crypto,
            journal = null, // lab 配对面无出向 AtLeastOnce 需求（上报/journal 为后续车道）
            scheduler = Scheduler.shared(),
            clock = System::currentTimeMillis,
            capabilities = ProtocolClient.Capabilities(
                clientVersion = "0.1.0-lab",
                encryptTypeMax = 2,
                commands = emptyList(), // 本面只证明配对+hello 协商；指令能力由执行器车道装配
            ),
            lastEpoch = { 0L },
            lastReceivedSeq = { 0L },
            onCommand = { frame, _, _ ->
                runOnUiThread { log("收到服务端帧 socketType=${frame.socketType}") }
            },
            onRejected = { r ->
                runOnUiThread {
                    log("拒帧 rule=${r.rule} detail=${r.detail}（hello_ack 为 T3.4 冻结项 → 关闭会话）")
                }
            },
        )
        s.start()
        session = s
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

    /**
     * 裁决 B：读取构建参数注入的 lab 一次性 Ed25519 公钥（hex）。
     * fail-closed：资产缺失/形状非法（非 64 位小写 hex）即抛，绝不兜底。
     * 私钥不进入端侧（本设备无 Ed25519 JCA，重协商路径 NOT_RUN）。
     */
    private fun loadLabInjectedEd25519PublicHex(): String {
        val text = assets.open("lab_ed25519_key.json").bufferedReader().use { it.readText() }
        val hex = org.json.JSONObject(text).getString("ed25519PublicKeyHex")
        require(hex.matches(Regex("^[0-9a-f]{64}$"))) { "lab 注入公钥形状非法" }
        return hex
    }

    private fun log(line: String) {
        runOnUiThread {
            findViewById<TextView>(R.id.lab_status).let { tv ->
                tv.text = "${tv.text}\n[${timeFmt.format(Date())}] $line".takeLast(4000)
            }
        }
    }

    override fun onDestroy() {
        session?.stop()
        session = null
        super.onDestroy()
    }
}
