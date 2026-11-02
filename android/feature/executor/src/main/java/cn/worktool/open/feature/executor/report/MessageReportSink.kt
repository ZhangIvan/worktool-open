/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.report

import cn.worktool.open.core.protocol.WeworkMessageBean

/**
 * 消息上报出口端口（FR-01「经 core:protocol 上报」的接缝）。
 *
 * 生产接线：app 装配层把本端口实现为 `ProtocolClient.V2Session.sendReport`
 * （core:protocol ProtocolClient.kt —— sendReport 构造 socketType=2 帧并经
 * AtLeastOnce 队列先落盘后发送，即**持久化**的离线暂存重发归协议 journal 层）；
 * 本模块经端口保持 JVM 可测（Fake 注入），不依赖网络实现。
 *
 * 返回 false = 当前不可投递（离线/会话未建）——调用方
 * [MessageEventReporter] 以内存暂存兜底重试（无 journal 部署形态）。
 */
fun interface MessageReportSink {

    /** 投递一批上报载荷；false = 本轮不可投递（调用方负责重试）。 */
    fun sendReport(beans: List<WeworkMessageBean>): Boolean
}
