/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.GroupListWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.fake.RecordingQuerySink
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 501 GET_GROUP_INFO 执行器场景（每执行器 ≥5：分页成功/入口缺失/超时/暂停/
 * 干跑/无界翻页护栏；docs/05 P0 首批行「501/502 群/好友列表查询」）。
 */
class GetGroupListExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingQuerySink()

    private fun registry() = ExecutorRegistry.of(GetGroupListExecutor(sink, timeouts = timeouts))

    private fun runner(world: GroupListWorld) = CommandRunner(world.driver, world.gate)

    // ---- ① 成功：导航 + 分页读取至收敛 + 结构化结果 ----

    @Test
    fun `成功-三页群名全量收集并输出结构化结果`() {
        val world = GroupListWorld()
        val report = runner(world).execute(CommandIntent.GetGroupList(), registry())

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(MessageTypes.GET_GROUP_INFO, report.commandType)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(
            listOf("交付群", "客户群", "外部合作群", "测试群", "售后群", "最后一批群"),
            (sink.results.single().second as QueryResult.GroupList).groups,
        )
        assertEquals(MessageTypes.GET_GROUP_INFO, sink.results.single().first)
        // 页边界：2 步导航 + 50 页读取（3 页实读 + 收敛空过）+ 校验 + 输出
        val ids = report.executedSteps.map { it.id }
        assertEquals(listOf("open-contacts-tab", "open-group-list"), ids.take(2))
        assertEquals((1..50).map { "collect-page-$it" }, ids.subList(2, 2 + 50))
        assertEquals(listOf("verify-converged", "emit-result"), ids.takeLast(2))
        // 滚动次数 = 实际翻页数（第 3 页滚动后第 4 页无新增即收敛）
        assertEquals(3, world.driver.actionLog.count { it.startsWith("scroll(") })
    }

    // ---- ② 节点缺失：无通讯录 Tab ----

    @Test
    fun `节点缺失-主界面无通讯录Tab则首步失败零结果`() {
        val world = GroupListWorld(withContactsTab = false)
        val report = runner(world).execute(CommandIntent.GetGroupList(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-contacts-tab", report.errorStepId)
        assertEquals("零结构化结果外发", 0, sink.results.size)
    }

    // ---- ③ 超时：群聊入口不出现 ----

    @Test
    fun `超时-通讯录页无群聊入口则等待超时`() {
        val world = GroupListWorld(withGroupChatEntry = false)
        val report = runner(world).execute(CommandIntent.GetGroupList(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("open-group-list", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
        assertEquals(0, sink.results.size)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零动作零结果PAUSED`() {
        val world = GroupListWorld()
        world.gate.paused = true
        world.gate.source = "console"
        val report = runner(world).execute(CommandIntent.GetGroupList(), registry())

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("console", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑤ 干跑（FR-54）：只读 L0 查询零界面动作 ----

    @Test
    fun `干跑-报告分页计划与L0且零动作零结果`() {
        val world = GroupListWorld()
        val dry = runner(world).dryRun(CommandIntent.GetGroupList(), registry())

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue(dry.paramsSummary.contains("maxPages=50"))
        assertTrue(dry.plannedSteps.any { it.startsWith("collect-page-1:") })
        assertTrue(dry.plannedSteps.any { it.startsWith("emit-result:") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("干跑不外发结果", 0, sink.results.size)
    }

    // ---- ⑥ 无界翻页护栏（FR-53 审慎默认同源） ----

    @Test
    fun `护栏-翻页永不收敛则VERIFY_FAILED且部分结果不外发`() {
        val world = GroupListWorld(infiniteScroll = true)
        val report = runner(world).execute(CommandIntent.GetGroupList(maxPages = 3), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.VERIFY_FAILED, report.errorCode)
        assertEquals("verify-converged", report.errorStepId)
        assertEquals("部分读取结果不外发（宁缺勿错）", 0, sink.results.size)
        assertEquals("护栏页数即滚动上限", 3, world.driver.actionLog.count { it.startsWith("scroll(") })
    }
}
