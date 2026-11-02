/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.InitGroupWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 206 INIT_GROUP 执行器场景（每执行器 ≥5：成功/节点缺失/超时/暂停/干跑）。
 * FR-53：建群 = L1 风险级（清单明列）。
 */
class InitGroupExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val registry = ExecutorRegistry.of(InitGroupExecutor(timeouts = timeouts))

    private fun runner(world: InitGroupWorld) = CommandRunner(world.driver, world.gate)

    private fun intent(world: InitGroupWorld) = CommandIntent.InitGroup(
        groupName = world.groupName,
        members = listOf("张三", "李四"),
        announcement = if (world.dialog.announcementInput != null) "本群用于客户交付" else null,
    )

    // ---- ① 成功 ----

    @Test
    fun `成功-建群全步骤走通并进入新群聊天页`() {
        val world = InitGroupWorld(withAnnouncementField = true)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, report.errorCode)
        assertEquals("建群应为 L1 风险级（FR-53）", RiskLevel.L1, report.riskLevel)
        assertEquals(
            listOf(
                "open-create-menu", "choose-init-group", "input-group-name",
                "select-member:张三", "select-member:李四",
                "input-group-announcement", "confirm-create", "verify-group-created",
            ),
            report.executedSteps.map { it.id },
        )
        assertEquals("群名已填入", world.groupName, world.dialog.nameInput.textValue)
        assertEquals("公告已填入", "本群用于客户交付", world.dialog.announcementInput?.textValue)
        assertEquals("已进入新群聊天页", world.groupName, world.chatAfterCreate.titleNode.textValue)
    }

    // ---- ② 节点缺失 ----

    @Test
    fun `节点缺失-可选成员不在选择列表则该步失败且未建群`() {
        val world = InitGroupWorld(missingMembers = setOf("李四"))
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("select-member:李四", report.errorStepId)
        assertEquals("未走到确认建群步骤", 0, report.executedSteps.count { it.id == "confirm-create" })
        assertTrue("未进入新群聊天页", world.driver.current !== world.chatAfterCreate.screen)
    }

    @Test
    fun `节点缺失-主界面无加号入口则首步失败`() {
        val world = InitGroupWorld(withPlusButton = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-create-menu", report.errorStepId)
    }

    // ---- ③ 超时 ----

    @Test
    fun `超时-加号点击后菜单不出现则等待超时`() {
        val world = InitGroupWorld(menuOpens = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("choose-init-group", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零副作用返回PAUSED`() {
        val world = InitGroupWorld()
        world.gate.paused = true
        world.gate.source = "auto-breaker"
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("auto-breaker", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, report.executedSteps.size)
        assertEquals("计划步骤全部跳过（默认世界无公告步骤共 7 步）", 7, report.skippedStepIds.size)
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-逐成员步骤出现在报告中且零界面动作`() {
        val world = InitGroupWorld(withAnnouncementField = true)
        val dry = runner(world).dryRun(intent(world), registry)

        assertEquals(RiskLevel.L1, dry.riskLevel)
        assertEquals(world.groupName, dry.target)
        assertTrue(dry.paramsSummary.contains("members=2"))
        assertTrue("干跑报告逐条列出拉人对象", dry.plannedSteps.any { it.contains("select-member:张三") })
        assertTrue(dry.plannedSteps.any { it.contains("select-member:李四") })
        assertTrue(dry.plannedSteps.any { it.contains("公告") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("群名未被填入", null, world.dialog.nameInput.textValue)
    }

    @Test
    fun `干跑-不带公告则计划中无公告步骤`() {
        val world = InitGroupWorld(withAnnouncementField = false)
        val dry = runner(world).dryRun(intent(world), registry)

        assertTrue(dry.plannedSteps.none { it.contains("input-group-announcement") })
        assertEquals(7, dry.plannedSteps.size)
    }
}
