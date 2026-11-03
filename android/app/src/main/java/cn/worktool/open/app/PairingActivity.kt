/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Activity
import android.content.Intent
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import cn.worktool.open.core.protocol.DeviceCredentials
import cn.worktool.open.core.protocol.android.AndroidKeystoreCredentialStore
import cn.worktool.open.core.protocol.crypto.Ed25519Keys
import cn.worktool.open.core.protocol.crypto.Hex
import cn.worktool.open.core.protocol.crypto.X25519
import cn.worktool.open.core.protocol.pair.PairingClient
import cn.worktool.open.core.protocol.pair.PairingException
import cn.worktool.open.core.protocol.pair.PairRequest
import java.util.Locale

/**
 * 配对引导流（产品冲刺 A 车道，首次启动入口）：
 *
 * 步骤 1：输入 server 地址（wss://host[:port]）→ 表单校验（[OnboardingForms]）
 *         + SR-09 安全终审（EndpointPolicy：拒裸 IP / 非 loopback 明文）→ 下一步；
 * 步骤 2：输入 6 位一次性配对码 → PairingClient.pair（Ed25519 签名公钥 +
 *         X25519 临时公钥，spec §5.1/§5.3；产品面不注册 lab 裸 IP 例外）；
 * 步骤 3：配对成功 → 凭据经 AndroidKeystoreCredentialStore 加密落盘（SR-05/G5）
 *         →「进入主界面」。
 *
 * 配对成功后 server 地址存 [AppSettings]（启动路由判据之一）。
 * 失败路径全量可见（403=配对码无效/过期/已用；400=请求被拒），不静默重试。
 */
class PairingActivity : Activity() {

    private lateinit var stepAddress: View
    private lateinit var stepCode: View
    private lateinit var stepDone: View

    private lateinit var addressInput: EditText
    private lateinit var addressError: TextView
    private lateinit var codeInput: EditText
    private lateinit var codeError: TextView
    private lateinit var pairButton: Button

    private var pairingBaseUrl: String = ""
    private val mainHandler = Handler(Looper.getMainLooper())

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_pairing)
        stepAddress = findViewById(R.id.pair_step_address)
        stepCode = findViewById(R.id.pair_step_code)
        stepDone = findViewById(R.id.pair_step_done)
        addressInput = findViewById(R.id.pair_address_input)
        addressError = findViewById(R.id.pair_address_error)
        codeInput = findViewById(R.id.pair_code_input)
        codeError = findViewById(R.id.pair_code_error)
        pairButton = findViewById(R.id.pair_button)

        // 预填上次地址（重配对便利；修改 server 地址入口在设置页）
        AppSettings(this).serverAddress?.let { addressInput.setText(it) }

        findViewById<Button>(R.id.pair_address_next).setOnClickListener { onAddressNext() }
        pairButton.setOnClickListener { onPair() }
        findViewById<Button>(R.id.pair_done).setOnClickListener {
            startActivity(
                Intent(this, MainActivity::class.java).apply {
                    flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TASK
                },
            )
        }
        showStep(stepAddress)
    }

    private fun showStep(step: View) {
        for (s in listOf(stepAddress, stepCode, stepDone)) {
            s.visibility = if (s == step) View.VISIBLE else View.GONE
        }
    }

    // ---- 步骤 1：server 地址 ----

    private fun onAddressNext() {
        addressError.text = ""
        val input = addressInput.text.toString()
        val verdict = OnboardingForms.validateServerAddress(input)
        if (verdict is OnboardingForms.ServerAddress.Invalid) {
            addressError.text = addressErrorText(verdict.reasonCode)
            return
        }
        val normalized = (verdict as OnboardingForms.ServerAddress.Valid).normalized
        val securityRule = OnboardingForms.securityCheck(normalized)
        if (securityRule != null) {
            addressError.text = securityErrorText(securityRule)
            return
        }
        pairingBaseUrl = normalized
        showStep(stepCode)
    }

    // ---- 步骤 2：6 位配对码 → POST /api/v2/pair ----

    private fun onPair() {
        codeError.text = ""
        val code = when (val v = OnboardingForms.validatePairingCode(codeInput.text.toString())) {
            is OnboardingForms.PairingCode.Valid -> v.code
            is OnboardingForms.PairingCode.Invalid -> {
                codeError.text = getString(R.string.pair_code_error)
                return
            }
        }
        pairButton.isEnabled = false
        pairButton.text = getString(R.string.pair_button_running)
        Thread { tryPair(pairingBaseUrl, code) }.start()
    }

    private fun tryPair(baseUrl: String, code: String) {
        try {
            // 1. Ed25519 签名密钥（JCA 优先；API 24~32 纯 JVM 回退，种子加密持久化 → 公钥稳定）
            val seedStore = SigningSeedStore(this)
            val signingKey = Ed25519Keys.resolve(persistedFallbackSeedHex = seedStore.loadSeedHex())
            signingKey.fallbackSeedHex?.let { seedStore.saveSeedHex(it) }

            // 2. X25519 临时对（spec §5.1：每次配对新生成）
            val ephemeralPair = X25519.generateKeyPair()

            // 3. 设备标识（SR-10：无伪装命名；ANDROID_ID 仅本地/发往用户自己的 server）
            val androidId = Settings.Secure.getString(contentResolver, Settings.Secure.ANDROID_ID)
            val deviceId = "wt-" + (androidId ?: "unknown").lowercase(Locale.US).take(16)
            val deviceName = "WorkTool ${Build.MANUFACTURER} ${Build.MODEL}".take(64)

            // 4. 配对（一次性配对码 + 双公钥）
            val response = PairingClient(baseUrl).pair(
                PairRequest(
                    pairingCode = code,
                    deviceId = deviceId,
                    deviceName = deviceName,
                    ed25519PublicKeyHex = signingKey.publicHex,
                    x25519EphemeralPublicKeyHex = Hex.encode(X25519.publicRaw(ephemeralPair.public)),
                ),
            )

            // 5. 凭据加密落盘 + 地址持久化 → 成功页
            AndroidKeystoreCredentialStore(this).save(
                DeviceCredentials(
                    deviceId = response.deviceId,
                    deviceTokenHex = response.deviceTokenHex,
                    tokenVersion = response.tokenVersion,
                    sessionId = response.sessionId,
                    serverX25519PublicKeyHex = response.serverX25519PublicKeyHex,
                    rotationIntervalDays = response.rotationIntervalDays,
                    heartbeatIntervalSeconds = response.heartbeatIntervalSeconds,
                    sessionTimeoutSeconds = response.sessionTimeoutSeconds,
                    wsPath = response.wsPath,
                ),
            )
            AppSettings(this).serverAddress = baseUrl
            mainHandler.post {
                findViewById<TextView>(R.id.pair_done_detail).text =
                    getString(R.string.pair_done_detail, response.deviceId)
                showStep(stepDone)
            }
        } catch (e: PairingException) {
            postPairingError(pairingErrorText(e))
        } catch (e: Exception) {
            postPairingError("${e.javaClass.simpleName}: ${e.message}")
        }
    }

    private fun postPairingError(message: String) {
        mainHandler.post {
            codeError.text = message
            pairButton.isEnabled = true
            pairButton.text = getString(R.string.pair_button)
        }
    }

    private fun pairingErrorText(e: PairingException): String = when (e.httpCode) {
        403 -> getString(R.string.pair_err_403)
        400 -> getString(R.string.pair_err_400)
        else -> getString(R.string.pair_err_generic, e.message)
    }

    private fun addressErrorText(reasonCode: String): String = when (reasonCode) {
        OnboardingForms.REASON_EMPTY,
        OnboardingForms.REASON_SCHEME,
        -> getString(R.string.pair_addr_error_format)
        OnboardingForms.REASON_HOST -> getString(R.string.pair_addr_error_host)
        OnboardingForms.REASON_PORT -> getString(R.string.pair_addr_error_port)
        OnboardingForms.REASON_USERINFO,
        OnboardingForms.REASON_FRAGMENT,
        -> getString(R.string.pair_addr_error_extra)
        else -> getString(R.string.pair_addr_error_format)
    }

    private fun securityErrorText(rule: String): String = when (rule) {
        "bare_ip_host" -> getString(R.string.pair_addr_error_bare_ip)
        "plain_text_not_loopback" -> getString(R.string.pair_addr_error_plain)
        else -> getString(R.string.pair_addr_error_security, rule)
    }
}
