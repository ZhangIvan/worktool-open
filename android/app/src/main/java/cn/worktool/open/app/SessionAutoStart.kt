/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.DeviceCredentials

/**
 * 会话自启判定（BP-1 / SP-1，纯 JVM 可测）。
 *
 * 消费方（三处共用同一口径，保证「配对完=立即在线、重启 App 自动恢复」语义一致）：
 * - [MainActivity.onStart]：进入主界面时自动恢复会话（幂等：会话已在跑 → 不重启，
 *   避免每次 onStart 重拨 WS 造成断线抖动）；
 * - [SessionForegroundService]：START_STICKY 进程被杀后系统重启服务时的自愈路径；
 * - [BootCompletedReceiver]：开机恢复（有凭据则 start）。
 *
 * 判定顺序（先阻断后启动）：
 * 1. 凭据缺失/残缺（deviceId/token 空）→ [Decision.SKIP_NO_CREDENTIALS]；
 * 2. server 地址非法（复用 [OnboardingForms.validateServerAddress] 同一表单口径）
 *    → [Decision.SKIP_NO_SERVER]；
 * 3. 会话已在跑 → [Decision.KEEP_RUNNING]（幂等，isRunning 判定——重配对的新会话
 *    由 PairingActivity 成功回调显式 stop-then-start，不依赖本判定）；
 * 4. 凭据不可启动（wsPath 与 sessionId 双空——SessionRuntime.start 的装配前置同样
 *    拒绝，这里前置给出可判定结果）→ [Decision.SKIP_UNUSABLE_CREDENTIALS]；
 * 5. 其余 → [Decision.START]。
 */
object SessionAutoStart {

    enum class Decision {
        /** 拉起会话（SessionRuntime.start）。 */
        START,

        /** 幂等：会话已在跑，不重启（防 onStart/服务重启造成重连抖动）。 */
        KEEP_RUNNING,

        /** 无凭据（未配对或凭据残缺）——路由层负责导向配对引导。 */
        SKIP_NO_CREDENTIALS,

        /** server 地址缺失/非法——无法构造会话 URL。 */
        SKIP_NO_SERVER,

        /** 凭据缺 wsPath 且 sessionId 空（URL 不可构造）——需重新配对。 */
        SKIP_UNUSABLE_CREDENTIALS,
    }

    fun decide(
        isRunning: Boolean,
        credentials: DeviceCredentials?,
        serverAddress: String?,
    ): Decision {
        val creds = credentials ?: return Decision.SKIP_NO_CREDENTIALS
        if (creds.deviceTokenHex.isBlank() || creds.deviceId.isBlank()) {
            return Decision.SKIP_NO_CREDENTIALS
        }
        val addr = serverAddress?.trim().orEmpty()
        if (OnboardingForms.validateServerAddress(addr) !is OnboardingForms.ServerAddress.Valid) {
            return Decision.SKIP_NO_SERVER
        }
        if (isRunning) return Decision.KEEP_RUNNING
        if (creds.wsPath.isNullOrBlank() && creds.sessionId.isNullOrBlank()) {
            return Decision.SKIP_UNUSABLE_CREDENTIALS
        }
        return Decision.START
    }
}
