/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.RecordingQuerySink
import cn.worktool.open.feature.executor.fixture.FriendListWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 502 GET_FRIEND_INFO 执行器场景（每执行器 ≥5：分页成功/空列表合法/Tab 缺失/
 * 暂停/干跑/无界翻页护栏）。
 */
class GetFriendListExecutorTest {

    private val sink = RecordingQuerySink()

    private fun registry() = ExecutorRegistry.of(GetFriendListExecutor(sink))

    private fun runner(world: FriendListWorld) = CommandRunner(world.driver, world.gate)

    // ---- ① 成功：两页好友全量收集 ----

    @Test
    fun `成功-两页好友全量收集并输出结构化结果`() {
        val world = FriendListWorld()
        val report = runner(world).execute(CommandIntent.GetFriendList(), registry())

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(MessageTypes.GET_FRIEND_INFO, report.commandType)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(
            listOf("张三", "李四", "王五", "赵六"),
            (sink.results.single().second as QueryResult.FriendList).friends,
        )
        // 滚动次数 = 2（第 1 页滚动载入第 2 页 + 第 2 页尾部滚动确认无更多即收敛）
        assertEquals(2, world.driver.actionLog.count { it.startsWith("scroll(") })
        assertEquals("emit-result", report.executedSteps.last().id)
    }

    // ---- ② 空列表 = 合法结构化结果（非错误） ----

    @Test
    fun `空列表-通讯录无联系人则收敛输出空FriendList`() {
        val world = FriendListWorld(friendPages = listOf(emptyList()))
        val report = runner(world).execute(CommandIntent.GetFriendList(), registry())

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val result = sink.results.single().second
        assertTrue("空列表照常输出（合法结果）", result is QueryResult.FriendList && result.friends.isEmpty())
        assertEquals("零滚动", 0, world.driver.actionLog.count { it.startsWith("scroll(") })
    }

    // ---- ③ 节点缺失：无通讯录 Tab ----

    @Test
    fun `节点缺失-主界面无通讯录Tab则首步失败零结果`() {
        val world = FriendListWorld(withContactsTab = false)
        val report = runner(world).execute(CommandIntent.GetFriendList(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-contacts-tab", report.errorStepId)
        assertEquals(0, sink.results.size)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零动作零结果PAUSED`() {
        val world = FriendListWorld()
        world.gate.paused = true
        world.gate.source = "floating-window"
        val report = runner(world).execute(CommandIntent.GetFriendList(), registry())

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("floating-window", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-报告分页计划与L0且零动作零结果`() {
        val world = FriendListWorld()
        val dry = runner(world).dryRun(CommandIntent.GetFriendList(), registry())

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue(dry.plannedSteps.any { it.startsWith("collect-page-1:") })
        assertTrue(dry.plannedSteps.any { it.startsWith("emit-result:") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑥ 无界翻页护栏 ----

    @Test
    fun `护栏-翻页永不收敛则VERIFY_FAILED且部分结果不外发`() {
        val world = FriendListWorld(infiniteScroll = true)
        val report = runner(world).execute(CommandIntent.GetFriendList(maxPages = 2), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.VERIFY_FAILED, report.errorCode)
        assertEquals("verify-converged", report.errorStepId)
        assertEquals(0, sink.results.size)
        assertEquals(2, world.driver.actionLog.count { it.startsWith("scroll(") })
    }
}
