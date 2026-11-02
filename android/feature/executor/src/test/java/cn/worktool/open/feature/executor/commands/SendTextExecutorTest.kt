/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.SendTextWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 203 SEND_MESSAGE 执行器场景（docs/03 T3.1：每执行器 ≥5 场景，JVM 纯测试）：
 * 成功 / 节点缺失 / 超时 / 暂停拦截 / 干跑（+ 点击被拒 / 步间断点两项加固）。
 */
class SendTextExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val registry = ExecutorRegistry.of(SendTextExecutor(timeouts = timeouts))

    private fun runner(world: SendTextWorld) = CommandRunner(world.driver, world.gate)

    private fun intent(world: SendTextWorld, at: List<String> = emptyList()) = CommandIntent.SendText(
        targetRoom = world.targetRoom,
        content = world.content,
        atList = at,
    )

    // ---- ① 成功：全步骤走通，消息上屏 ----

    @Test
    fun `成功-会话定位输入发送全步骤走通且消息上屏`() {
        val world = SendTextWorld()
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, report.errorCode)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(
            listOf("open-conversation", "await-chat-ready", "input-message", "send-message", "verify-sent"),
            report.executedSteps.map { it.id },
        )
        assertTrue("消息应已上屏", world.chat.messageTexts().contains(world.content))
        assertTrue("应有点击输入框动作", world.driver.actionLog.any { it.startsWith("click(") })
    }

    @Test
    fun `成功-atList 场景输入文本包含被@人`() {
        val world = SendTextWorld()
        val report = runner(world).execute(intent(world, at = listOf("张三")), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(
            world.chat.messageTexts().any { it != null && it.contains("@张三") && it.contains(world.content) },
        )
    }

    // ---- ② 节点缺失 ----

    @Test
    fun `节点缺失-会话列表无目标会话则首步失败`() {
        val world = SendTextWorld(conversationPresent = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-conversation", report.errorStepId)
        assertEquals(1, report.executedSteps.size)
        assertEquals(
            listOf("await-chat-ready", "input-message", "send-message", "verify-sent"),
            report.skippedStepIds,
        )
    }

    @Test
    fun `节点缺失-发送按钮不可点击则动作被拒`() {
        val world = SendTextWorld(sendClickable = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.ACTION_REJECTED, report.errorCode)
        assertEquals("send-message", report.errorStepId)
    }

    // ---- ③ 超时 ----

    @Test
    fun `超时-点击会话后聊天页不出现则等待步骤超时`() {
        val world = SendTextWorld(chatOpens = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("await-chat-ready", report.errorStepId)
        // 假时钟推进量 ≥ 等待上限（确定性超时，非真实睡眠）
        assertTrue("时钟应推进到超时阈值", world.clock.now >= timeouts.awaitNodeMs)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零副作用返回PAUSED`() {
        val world = SendTextWorld()
        world.gate.paused = true
        world.gate.source = "floating-window"
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("floating-window", report.pausedBySource)
        assertEquals("零步骤执行", 0, report.executedSteps.size)
        assertEquals("全部步骤记为跳过", 5, report.skippedStepIds.size)
        assertEquals("零界面动作（FR-52 无新副作用）", 0, world.driver.actionLog.size)
    }

    @Test
    fun `暂停拦截-执行中接管则于步间安全断点停止`() {
        val world = SendTextWorld()
        // 第 1 次点击（进入聊天页）后用户接管 → 暂停门在下一步边界生效
        world.driver.onAfterAction = {
            if (world.driver.actionLog.count { it.startsWith("click(") } >= 1) {
                world.gate.paused = true
                world.gate.source = "console"
            }
        }
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals("console", report.pausedBySource)
        assertEquals(
            listOf("open-conversation"),
            report.executedSteps.map { it.id },
        )
        assertEquals(
            listOf("await-chat-ready", "input-message", "send-message", "verify-sent"),
            report.skippedStepIds,
        )
        assertTrue("输入框未被写入（副作用停止于断点）", world.chat.inputNode.textValue == null)
        assertTrue("消息未发送", world.chat.messageTexts().isEmpty())
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-产出将要做什么报告且零界面动作`() {
        val world = SendTextWorld()
        val dry = runner(world).dryRun(intent(world), registry)

        assertEquals(CommandIntent.SendText(world.targetRoom, world.content).commandType, dry.commandType)
        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertEquals(world.targetRoom, dry.target)
        assertEquals(5, dry.plannedSteps.size)
        assertTrue(
            "干跑报告应含目标会话描述",
            dry.plannedSteps.any { it.contains("外部群测试群") },
        )
        assertTrue(
            "干跑报告应含参数摘要",
            dry.paramsSummary.contains("content.length=${world.content.length}"),
        )
        assertEquals("干跑零界面动作", 0, world.driver.actionLog.size)
        assertEquals("界面无变更", null, world.chat.inputNode.textValue)
        assertTrue("消息未上屏", world.chat.messageTexts().isEmpty())
    }
}
