/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.runtime

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.SendTextWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 执行引擎（CommandRunner）门序与门禁语义：
 * 未注册指令拒绝 / 暂停与干跑的边界组合 / 报告留痕完整性。
 */
class CommandRunnerTest {

    private val registry = ExecutorRegistry.defaultP0()

    // ---- 未注册指令 ----

    @Test
    fun `未注册指令号-FAILED且错误码COMMAND_UNSUPPORTED`() {
        val world = SendTextWorld()
        val report = CommandRunner(world.driver, world.gate)
            .execute(CommandIntent.PassFriend(), registryOfOnly203())

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.COMMAND_UNSUPPORTED, report.errorCode)
        assertEquals("零界面动作", 0, world.driver.actionLog.size)
        assertTrue(report.plannedStepIds.isEmpty())
    }

    // ---- 干跑与暂停的边界组合（FR-54 vs FR-52） ----

    @Test
    fun `干跑-暂停期间仍可用（无副作用不受暂停门拦截）`() {
        val world = SendTextWorld()
        world.gate.paused = true
        val runner = CommandRunner(world.driver, world.gate)
        val dry = runner.dryRun(
            CommandIntent.SendText(world.targetRoom, world.content),
            registry,
        )

        assertEquals(5, dry.plannedSteps.size)
        assertEquals("干跑零界面动作（暂停态同样零副作用）", 0, world.driver.actionLog.size)
    }

    @Test
    fun `真实执行-暂停期间被拦截与干跑同引擎不同结果`() {
        val world = SendTextWorld()
        world.gate.paused = true
        val runner = CommandRunner(world.driver, world.gate)
        val intent = CommandIntent.SendText(world.targetRoom, world.content)

        val dry = runner.dryRun(intent, registry)
        val real = runner.execute(intent, registry)

        assertEquals("干跑照常产出计划", 5, dry.plannedSteps.size)
        assertEquals(ExecutionStatus.PAUSED, real.status)
        assertEquals(0, real.executedSteps.size)
        assertEquals(0, world.driver.actionLog.size)
    }

    // ---- 报告留痕 ----

    @Test
    fun `报告-成功时含全部步骤与时间戳且errorStep为空`() {
        val world = SendTextWorld()
        val report = CommandRunner(world.driver, world.gate)
            .execute(CommandIntent.SendText(world.targetRoom, world.content), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(MessageTypes.SEND_MESSAGE, report.commandType)
        assertEquals(5, report.executedSteps.size)
        assertTrue(report.executedSteps.all { it.succeeded })
        assertEquals(null, report.errorStepId)
        assertEquals(null, report.errorReason)
        assertTrue("结束时间 ≥ 开始时间", report.finishedAtMillis >= report.startedAtMillis)
        assertTrue("skipped 为空", report.skippedStepIds.isEmpty())
    }

    @Test
    fun `报告-失败时errorStep定位与剩余步骤记为跳过`() {
        val world = SendTextWorld(conversationPresent = false)
        val report = CommandRunner(world.driver, world.gate)
            .execute(CommandIntent.SendText("不存在的会话", "内容"), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals("open-conversation", report.errorStepId)
        assertEquals(4, report.skippedStepIds.size)
        assertEquals(
            "失败步骤自身计入 executedSteps（succeeded=false）",
            false,
            report.executedSteps.last().succeeded,
        )
    }

    private fun registryOfOnly203(): ExecutorRegistry = ExecutorRegistry.of(
        cn.worktool.open.feature.executor.commands.SendTextExecutor(
            timeouts = StepTimeouts(awaitNodeMs = 200, pollMs = 100),
        ),
    )
}
