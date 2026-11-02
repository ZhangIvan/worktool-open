/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fake

import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.feature.executor.query.FileInfo
import cn.worktool.open.feature.executor.query.LocalFilePort
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.query.QueryResultSink
import cn.worktool.open.feature.executor.report.MessageReportSink

/**
 * FR-01 / 查询类执行器共用测试假件（端口注入面）。
 */

/** 上报出口假件：记录全部投递尝试；[accept]=false 模拟离线/会话未建。 */
class FakeReportSink(
    var accept: Boolean = true,
) : MessageReportSink {

    /** 一次投递尝试（被拒也记录，断言重试/保序语义用）。 */
    data class Attempt(val beans: List<WeworkMessageBean>, val accepted: Boolean)

    val attempts = mutableListOf<Attempt>()

    override fun sendReport(beans: List<WeworkMessageBean>): Boolean {
        attempts += Attempt(beans, accept)
        return accept
    }

    /** 全部尝试的 bean（含被拒轮次）。 */
    fun allBeans(): List<WeworkMessageBean> = attempts.flatMap { it.beans }

    /** 仅成功接受的 bean（顺序 = 接受序）。 */
    fun acceptedBeans(): List<WeworkMessageBean> =
        attempts.filter { it.accepted }.flatMap { it.beans }
}

/** 查询结果端口假件：按序记录结构化结果。 */
class RecordingQuerySink : QueryResultSink {

    val results = mutableListOf<Pair<Int, QueryResult>>()

    override fun onQueryResult(commandType: Int, result: QueryResult) {
        results += commandType to result
    }
}

/** 本地文件端口假件：路径 → 大小表 + 可选默认日志路径。 */
class FakeLocalFiles(
    private val sizes: Map<String, Long>,
    private val defaultLog: String? = null,
) : LocalFilePort {

    override fun defaultLogFilePath(): String =
        defaultLog ?: error("测试未配置默认日志路径")

    override fun stat(path: String): FileInfo? = sizes[path]?.let { size ->
        FileInfo(
            name = path.substringAfterLast('/'),
            path = path,
            sizeBytes = size,
        )
    }
}
