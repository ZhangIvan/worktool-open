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
 * 501 GET_GROUP_INFO 执行器场景（2026-11-04 会话 10 **对齐原版语义重写**：
 * selectList 按名单逐群「消息列表定位 → 进入 → 读群头」；空名单如实失败）。
 */
class GetGroupListExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val sink = RecordingQuerySink()

    private fun registry() = ExecutorRegistry.of(GetGroupListExecutor(sink, timeouts = timeouts))

    private fun runner(world: GroupListWorld) = CommandRunner(world.driver, world.gate)

    // ---- ① 成功：按名单进入群聊页并读取群显示名 ----

    @Test
    fun `成功-名单群进入并读群头输出结构化结果`() {
        val world = GroupListWorld(groupNames = listOf("成员甲、成员乙、成员丙、成员丁"), chatHeader = "群聊(4)")
        val report = runner(world).execute(
            CommandIntent.GetGroupList(groupNames = listOf("成员甲、成员乙、成员丙、成员丁")),
            registry(),
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(MessageTypes.GET_GROUP_INFO, report.commandType)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(
            listOf("群聊(4)"),
            (sink.results.single().second as QueryResult.GroupList).groups,
        )
        // 步骤序：消息 Tab → 定位群 → 读群头 → 返回 → 输出
        assertEquals(
            listOf("open-message-tab", "open-group-0", "read-group-header-0", "back-to-list-0", "emit-result"),
            report.executedSteps.map { it.id },
        )
    }

    // ---- ② 多群名单：逐群循环（Tab 仅开一次，返回后定位下一个） ----

    @Test
    fun `成功-双群名单逐群循环`() {
        val world = GroupListWorld(
            groupNames = listOf("交付群", "售后群"),
            chatHeader = "群聊(7)",
        )
        val report = runner(world).execute(
            CommandIntent.GetGroupList(groupNames = listOf("交付群", "售后群")),
            registry(),
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(listOf("群聊(7)", "群聊(7)"), (sink.results.single().second as QueryResult.GroupList).groups)
        assertEquals(
            listOf("open-message-tab", "open-group-0", "read-group-header-0", "back-to-list-0",
                "open-group-1", "read-group-header-1", "back-to-list-1", "emit-result"),
            report.executedSteps.map { it.id },
        )
    }

    // ---- ③ 空名单：如实失败（企微 5.0.x 无枚举入口） ----

    @Test
    fun `空名单-NODE_NOT_FOUND且说明枚举入口缺失`() {
        val world = GroupListWorld()
        val report = runner(world).execute(CommandIntent.GetGroupList(), registry())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("require-select-list", report.errorStepId)
        assertTrue(report.errorReason!!.contains("企微 5.0"))
        assertEquals("零结构化结果外发", 0, sink.results.size)
    }

    // ---- ④ 会话缺失：消息列表无目标会话 ----

    @Test
    fun `节点缺失-消息列表无目标会话则定位失败`() {
        val world = GroupListWorld(groupNames = listOf("不存在的群"), conversationPresent = false)
        val report = runner(world).execute(
            CommandIntent.GetGroupList(groupNames = listOf("不存在的群")),
            registry(),
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-group-0", report.errorStepId)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑤ 超时：点击后聊天页群头不出现 ----

    @Test
    fun `超时-会话进入后群头不出现`() {
        val world = GroupListWorld(groupNames = listOf("交付群"), chatOpens = false)
        val report = runner(world).execute(
            CommandIntent.GetGroupList(groupNames = listOf("交付群")),
            registry(),
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("read-group-header-0", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑥ 暂停拦截（FR-52）与干跑（FR-54） ----

    @Test
    fun `暂停拦截-执行前已暂停则零动作零结果PAUSED`() {
        val world = GroupListWorld()
        world.gate.paused = true
        world.gate.source = "console"
        val report = runner(world).execute(
            CommandIntent.GetGroupList(groupNames = listOf("交付群")),
            registry(),
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("console", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, sink.results.size)
    }

    @Test
    fun `干跑-报告名单计划与L0且零动作零结果`() {
        val world = GroupListWorld()
        val dry = runner(world).dryRun(
            CommandIntent.GetGroupList(groupNames = listOf("交付群", "售后群")),
            registry(),
        )

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue(dry.paramsSummary.contains("groups=2"))
        assertTrue(dry.plannedSteps.any { it.startsWith("open-group-0:") })
        assertTrue(dry.plannedSteps.any { it.startsWith("emit-result:") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("干跑不外发结果", 0, sink.results.size)
    }
}
