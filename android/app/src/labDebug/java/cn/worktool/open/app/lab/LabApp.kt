/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app.lab

import android.app.Application
import cn.worktool.open.core.protocol.transport.EndpointPolicy

/**
 * lab 变体进程入口（会话 21 P0 根因修复，2026-11-04）：进程启动即恢复 lab 裸 IP
 * 主机例外注册。
 *
 * 根因：`EndpointPolicy.allowLabRawIpHosts` 是**进程内**易失注册表，仅在
 * [LabPairingActivity] 配对流程中注册——重装/进程重建后的新进程里注册表为空，
 * 而会话自启走产品主界面（MainActivity → SessionRuntime）直连 lab 裸 IP 服务器 →
 * `OkHttpWsTransport.connect` 的 `EndpointPolicy.check` 以 `bare_ip_host` **本地
 * 拒拨**（零网络包、服务端零痕迹、UI 恒「重连中(第 N 次)」——会话 21 真机 .78
 * 实锤：tcpdump 无包 + 服务端日志无记录 + 重装后新进程永不在线）。
 *
 * 修复：Application.onCreate（早于一切 Activity/Service/Receiver 的连接动作）从
 * 已存 server 地址（AppSettings.serverAddress，配对时写入）提取 host 重新注册。
 * 仅 labDebug 构型装载本类（源集隔离，G12：产品变体零行为差异——产品从不注册
 * 裸 IP 例外，EndpointPolicy 对产品仍默认拒绝）。
 */
class LabApp : Application() {

    override fun onCreate() {
        super.onCreate()
        restoreLabHostException()
    }

    private fun restoreLabHostException() {
        runCatching {
            val addr = cn.worktool.open.app.AppSettings(this).serverAddress ?: return
            val host = java.net.URI(addr).host ?: return
            EndpointPolicy.allowLabRawIpHosts(setOf(host))
        }
        // 注册失败（无地址/解析异常）= 维持空注册表：裸 IP 连接按 SR-09 默认拒绝，
        // 用户重新配对（LabPairingActivity 注册）即恢复——fail-closed 不放行。
    }
}
