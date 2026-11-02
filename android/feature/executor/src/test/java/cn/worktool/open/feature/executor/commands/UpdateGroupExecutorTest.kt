/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.UpdateGroupWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 207 UPDATE_GROUP 执行器场景（每执行器 ≥5：成功/节点缺失/超时/暂停/干跑）。
 * FR-53：拉人 = L1；仅公告/备注 = L0（风险级随载荷动态细化）。
 */
class UpdateGroupExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val registry = ExecutorRegistry.of(UpdateGroupExecutor(timeouts = timeouts))

    private fun runner(world: UpdateGroupWorld) = CommandRunner(world.driver, world.gate)

    private fun fullIntent(world: UpdateGroupWorld) = CommandIntent.UpdateGroup(
        targetGroup = world.targetGroup,
        announcement = "新公告：每周五交付",
        groupRemark = "重点客户",
        addMembers = listOf("张三"),
    )

    // ---- ① 成功（含拉人 → L1） ----

    @Test
    fun `成功-公告备注拉人全变更走通且成员入群`() {
        val world = UpdateGroupWorld()
        val report = runner(world).execute(fullIntent(world), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, report.errorCode)
        assertEquals("含拉人应为 L1（FR-53）", RiskLevel.L1, report.riskLevel)
        assertEquals(
            listOf(
                "open-conversation", "await-chat-ready", "open-group-settings",
                "edit-announcement", "verify-announcement-saved",
                "edit-group-remark", "verify-group-remark-saved",
                "open-add-members", "select-add-member:张三",
                "confirm-add-members", "verify-members-added",
                "back-to-chat",
            ),
            report.executedSteps.map { it.id },
        )
        assertTrue(
            "公告预览已更新",
            world.settings.announcementPreview?.textValue?.contains("新公告") == true,
        )
        assertTrue(
            "拉人后成员条含新成员（选择器全树检索命中）",
            WeworkScreenRules().findByText(world.settings.screen.root, "张三") != null,
        )
    }

    @Test
    fun `成功-仅公告变更为L0且无拉人步骤`() {
        val world = UpdateGroupWorld()
        val intent = CommandIntent.UpdateGroup(
            targetGroup = world.targetGroup,
            announcement = "只有公告",
        )
        val report = runner(world).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals("无拉人应为 L0", RiskLevel.L0, report.riskLevel)
        assertTrue(report.executedSteps.none { it.id.startsWith("select-add-member") })
    }

    // ---- ② 节点缺失 ----

    @Test
    fun `节点缺失-拉人时无添加成员入口则失败`() {
        val world = UpdateGroupWorld(withAddMemberEntry = false)
        val report = runner(world).execute(fullIntent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-add-members", report.errorStepId)
    }

    @Test
    fun `节点缺失-选择页无目标成员则拉人中断未确认`() {
        val world = UpdateGroupWorld(missingMembers = setOf("张三"))
        val report = runner(world).execute(fullIntent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("select-add-member:张三", report.errorStepId)
        assertTrue(
            "失败原因应说明未确认添加",
            report.errorReason?.contains("未确认添加") == true,
        )
    }

    /** CR-t31-T31-R1：备注编辑页缺「确定」按钮 = 变更未保存，必须失败（不得静默 Success）。 */
    @Test
    fun `节点缺失-备注编辑页缺确定按钮则失败上报未保存`() {
        val world = UpdateGroupWorld(withRemarkConfirm = false)
        val report = runner(world).execute(
            CommandIntent.UpdateGroup(
                targetGroup = world.targetGroup,
                groupRemark = "重点客户",
                addMembers = listOf("张三"),
            ),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("edit-group-remark", report.errorStepId)
        assertTrue(
            "失败原因应说明备注未保存（旧代码静默 Success）",
            report.errorReason?.contains("备注未保存") == true,
        )
        assertEquals(
            "备注预览保持占位（未保存）",
            "（暂无备注）",
            world.settings.remarkPreview?.textValue,
        )
    }

    // ---- ③ 超时 ----

    @Test
    fun `超时-进入群聊后聊天页不出现则等待超时`() {
        val world = UpdateGroupWorld(groupOpens = false)
        val report = runner(world).execute(fullIntent(world), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("await-chat-ready", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零副作用返回PAUSED`() {
        val world = UpdateGroupWorld()
        world.gate.paused = true
        val report = runner(world).execute(fullIntent(world), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals(0, report.executedSteps.size)
    }

    /**
     * CR-t31-T31-R2：拉人循环每个成员一步——首人勾选后接管翻转，
     * 下一名成员**勾选前**即安全断点停止，确定（不可逆）不再发生。
     */
    @Test
    fun `暂停-首名成员勾选后接管翻转则下一名前安全断点停止`() {
        val world = UpdateGroupWorld(addMembers = listOf("张三", "李四"))
        world.driver.onAfterAction = {
            if (world.driver.actionLog.lastOrNull()?.contains("text=\"张三\"") == true) {
                world.gate.paused = true // 首人勾选生效后立即接管
            }
        }
        val report = runner(world).execute(
            CommandIntent.UpdateGroup(
                targetGroup = world.targetGroup,
                addMembers = listOf("张三", "李四"),
            ),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("已勾选步骤留痕", "select-add-member:张三", report.executedSteps.last().id)
        assertEquals("断点定位在下一成员前", "select-add-member:李四", report.skippedStepIds.first())
        assertTrue(
            "确定（不可逆）未发生——成员条未追加任何人",
            WeworkScreenRules()
                .findByText(world.settings.screen.root, "张三") == null &&
                WeworkScreenRules().findByText(world.settings.screen.root, "李四") == null,
        )
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-变更项逐条呈现风险级随载荷切换`() {
        val world = UpdateGroupWorld()
        val dry = runner(world).dryRun(fullIntent(world), registry)

        assertEquals(RiskLevel.L1, dry.riskLevel)
        assertEquals(world.targetGroup, dry.target)
        assertTrue(dry.paramsSummary.contains("addMembers=1"))
        assertTrue(dry.plannedSteps.any { it.startsWith("edit-announcement:") })
        assertTrue(dry.plannedSteps.any { it.startsWith("edit-group-remark:") })
        assertTrue(dry.plannedSteps.any { it.startsWith("select-add-member:张三:") })
        assertTrue("逐对象一步（CR-t31-R2）：确定单列一步", dry.plannedSteps.any { it.startsWith("confirm-add-members:") })
        assertEquals("干跑零界面动作", 0, world.driver.actionLog.size)
    }

    @Test
    fun `干跑-仅备注变更则无公告与拉人步骤且L0`() {
        val world = UpdateGroupWorld()
        val dry = runner(world).dryRun(
            CommandIntent.UpdateGroup(targetGroup = world.targetGroup, groupRemark = "备注"),
            registry,
        )

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue(dry.plannedSteps.none { it.startsWith("edit-announcement:") })
        assertTrue(dry.plannedSteps.none { it.startsWith("add-members:") })
    }
}
