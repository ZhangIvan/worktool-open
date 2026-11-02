/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fixture.PassFriendWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 212 PASS_ALL_FRIEND_REQUEST 执行器场景（每执行器 ≥5：成功/节点缺失/超时/暂停/干跑）。
 * spec §4.2：212 上游开源版为空实现，本执行器为我方 FR-15 语义落地。
 */
class PassFriendExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val registry = ExecutorRegistry.of(PassFriendExecutor(timeouts = timeouts))

    private fun runner(world: PassFriendWorld) = CommandRunner(world.driver, world.gate)

    // ---- ① 成功 ----

    @Test
    fun `成功-逐个接受全部好友请求至清空`() {
        val world = PassFriendWorld(requesters = listOf("新好友甲", "新好友乙"))
        val report = runner(world).execute(CommandIntent.PassFriend(), registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(0, report.errorCode)
        assertEquals(RiskLevel.L0, report.riskLevel)
        // CR-t31-R2：每个「接受」一步（accept-request-1..limit，已清空则空过）+ 收尾校验
        val ids = report.executedSteps.map { it.id }
        assertEquals(listOf("open-contacts-tab", "open-new-friends"), ids.take(2))
        assertEquals((1..50).map { "accept-request-$it" }, ids.subList(2, 2 + 50))
        assertEquals("verify-no-pending", ids.last())
        assertEquals("全部请求行已消失", 0, world.requests.remaining().size)
        assertEquals("接受点击次数=请求数", 2, world.driver.actionLog.count { it.contains("text=\"接受\"") })
    }

    // ---- ② 节点缺失 ----

    @Test
    fun `节点缺失-主界面无通讯录Tab则首步失败`() {
        val world = PassFriendWorld(withContactsTab = false)
        val report = runner(world).execute(CommandIntent.PassFriend(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals("open-contacts-tab", report.errorStepId)
        assertEquals("未产生任何接受动作", 0, world.requests.rows.count { it.rowNode.parent == null })
    }

    // ---- ③ 超时 ----

    @Test
    fun `超时-通讯录页新的朋友入口不出现则等待超时`() {
        // 通讯录 Tab 存在但点击不跳转（Tab 未注册跳转）→ 入口永不出现
        val world = PassFriendWorld(withNewFriendsEntry = false)
        val report = runner(world).execute(CommandIntent.PassFriend(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.STEP_TIMEOUT, report.errorCode)
        assertEquals("open-new-friends", report.errorStepId)
        assertTrue(world.clock.now >= timeouts.awaitNodeMs)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零副作用返回PAUSED`() {
        val world = PassFriendWorld()
        world.gate.paused = true
        world.gate.source = "floating-window"
        val report = runner(world).execute(CommandIntent.PassFriend(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("floating-window", report.pausedBySource)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("请求行全部保留", 2, world.requests.remaining().size)
    }

    // ---- ⑤ 干跑（FR-54） ----

    @Test
    fun `干跑-报告含上限与步骤且零界面动作`() {
        val world = PassFriendWorld()
        val dry = runner(world).dryRun(CommandIntent.PassFriend(), registry)

        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue(dry.paramsSummary.contains("acceptLimit=50"))
        // CR-t31-R2：接受步骤逐人呈现（accept-request-N，N=1..limit）
        assertEquals(50, dry.plannedSteps.count { it.startsWith("accept-request-") })
        assertTrue(dry.plannedSteps.any { it.startsWith("accept-request-1:") && it.contains("接受") })
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("请求行未被处理", 2, world.requests.remaining().size)
    }

    // ---- 加固：单轮上限防无界循环 ----

    @Test
    fun `上限-请求数超单轮上限则报VERIFY_FAILED待分批`() {
        val world = PassFriendWorld(requesters = listOf("甲", "乙", "丙"))
        val report = runner(world).execute(CommandIntent.PassFriend(limit = 2), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.VERIFY_FAILED, report.errorCode)
        assertEquals("verify-no-pending", report.errorStepId)
        assertEquals("仅接受上限人数", 1, world.requests.remaining().size)
        assertEquals("接受点击次数=上限", 2, world.driver.actionLog.count { it.contains("text=\"接受\"") })
    }

    /**
     * CR-t31-T31-R2：每个「接受」一步——首人接受后接管翻转，下一人**接受前**
     * 即安全断点停止（FR-52：暂停生效后无新副作用，不可逆接受不再继续）。
     */
    @Test
    fun `暂停-首人接受后接管翻转则下一人前安全断点停止`() {
        val world = PassFriendWorld(requesters = listOf("新好友甲", "新好友乙"))
        var acceptedClicks = 0
        world.driver.onAfterAction = {
            if (world.driver.actionLog.lastOrNull()?.contains("text=\"接受\"") == true) {
                acceptedClicks++
                if (acceptedClicks == 1) world.gate.paused = true // 首人接受生效后立即接管
            }
        }
        val report = runner(world).execute(CommandIntent.PassFriend(), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("仅 1 次不可逆接受副作用", 1, acceptedClicks)
        assertEquals("已接受步骤留痕", "accept-request-1", report.executedSteps.last().id)
        assertEquals("断点定位在下一人前", "accept-request-2", report.skippedStepIds.first())
        assertEquals("第二人未被接受", 1, world.requests.remaining().size)
    }
}
