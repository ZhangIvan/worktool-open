/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

/**
 * core:protocol 占位对象（T1.2）。
 *
 * T2.2 将在本模块落地：WS 客户端（`{host}/webserver/wework/{robotId}` v1 兼容路径
 * 与 v2 安全会话）、信封编解码（docs/02 §5）、心跳/重连、AtLeastOnce 队列、离线暂存。
 * 安全红线（实现时遵守）：SR-01 无 trust-all、SR-09 默认无明文 ws:// 回退、
 * G12 禁 hook/动态加载。
 */
object ProtocolClientPlaceholder {

    /** 模块状态标记：T2.2 之前保持占位。 */
    const val MODULE_STATUS: String = "placeholder-for-T2.2"
}
