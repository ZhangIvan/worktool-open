/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.MemberInfoWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 225 MODIFY_GROUP_MEMBER_INFO 执行器场景（每执行器 ≥5：成功/节点缺失/超时/暂停/干跑）。
 * FR-53：改备注/标签未列入 L1/L2 清单 → L0。
 */
class ModifyGroupMemberInfoExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val registry = ExecutorRegistry.of(ModifyGroupMemberInfoExecutor(timeouts = timeouts))

    private fun runner(world: MemberInfoWorld) = CommandRunner(world.driver, world.gate)

    private fun intent(world: MemberInfoWorld) = CommandIntent.ModifyGroupMemberInfo(
        targetGroup = world.targetGroup,
        memberName = world.memberName,
        markName = "重点客户-王五",
        tagList = listOf("VIP", "已交付"),
    )

    // ---- ① 成功 ----

    @Test
    fun `成功-定位成员改备注设标签全步骤走通`() {
        val world = MemberInfoWorld()
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, report.errorCode)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals(
            listOf(
                "open-conversation", "await-chat-ready", "open-group-settings",
                "open-member", "edit-member-mark", "set-member-tags",
                "confirm-member-edit", "back-to-chat",
            ),
            report.executedSteps.map { it.id },
        )
        assertEquals("备注已写入", "重点客户-王五", world.memberDetail.markInput?.textValue)
        assertEquals("标签已写入", "VIP,已交付", world.memberDetail.tagInput?.textValue)
    }

    // ---- ② 节点缺失 ----

    @Test
    fun `节点缺失-群内无目标成员则定位失败`() {
        val world = MemberInfoWorld(membersInGroup = listOf("赵六"))
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-member", report.errorStepId)
        assertEquals("备注未被写入", null, world.memberDetail.markInput?.textValue)
    }

    // ---- ③ 超时 ----

    @Test
    fun `超时-成员详情无备注输入框则等待超时`() {
        val world = MemberInfoWorld(withMarkInput = false)
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("edit-member-mark", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零副作用返回PAUSED`() {
        val world = MemberInfoWorld()
        world.gate.paused = true
        world.gate.source = "console"
        val report = runner(world).execute(intent(world), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("console", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, report.executedSteps.size)
        assertEquals(8, report.skippedStepIds.size)
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-报告含成员与标签且零界面动作`() {
        val world = MemberInfoWorld()
        val dry = runner(world).dryRun(intent(world), registry)

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertEquals("${world.targetGroup}/${world.memberName}", dry.target)
        assertTrue(dry.paramsSummary.contains("member=王五"))
        assertTrue(dry.plannedSteps.any { it.startsWith("edit-member-mark:") && it.contains("重点客户-王五") })
        assertTrue(dry.plannedSteps.any { it.startsWith("set-member-tags:") && it.contains("VIP") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(null, world.memberDetail.markInput?.textValue)
    }
}
