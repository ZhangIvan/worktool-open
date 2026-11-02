/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.commands.InitGroupExecutor
import cn.worktool.open.feature.executor.commands.ModifyGroupMemberInfoExecutor
import cn.worktool.open.feature.executor.commands.PassFriendExecutor
import cn.worktool.open.feature.executor.commands.SendTextExecutor
import cn.worktool.open.feature.executor.commands.UpdateGroupExecutor
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 执行器注册表（路由/支持集/重复注册拒绝）与 FR-53 风险级标注矩阵。
 */
class ExecutorRegistryTest {

    @Test
    fun `默认P0注册表-恰好覆盖任务卡T3_1五指令`() {
        val registry = ExecutorRegistry.defaultP0()
        assertEquals(
            setOf(
                MessageTypes.SEND_MESSAGE,
                MessageTypes.INIT_GROUP,
                MessageTypes.UPDATE_GROUP,
                MessageTypes.MODIFY_GROUP_MEMBER_INFO,
                MessageTypes.PASS_ALL_FRIEND_REQUEST,
            ),
            registry.supportedTypes,
        )
        assertTrue(registry.executorFor(MessageTypes.SEND_MESSAGE) is SendTextExecutor)
        assertTrue(registry.executorFor(MessageTypes.INIT_GROUP) is InitGroupExecutor)
        assertTrue(registry.executorFor(MessageTypes.UPDATE_GROUP) is UpdateGroupExecutor)
        assertTrue(registry.executorFor(MessageTypes.MODIFY_GROUP_MEMBER_INFO) is ModifyGroupMemberInfoExecutor)
        assertTrue(registry.executorFor(MessageTypes.PASS_ALL_FRIEND_REQUEST) is PassFriendExecutor)
    }

    @Test
    fun `未注册指令号-返回null且supports为false`() {
        val registry = ExecutorRegistry.defaultP0()
        assertNull(registry.executorFor(MessageTypes.DISMISS_GROUP))
        assertFalse(registry.supports(MessageTypes.DISMISS_GROUP))
        assertFalse(registry.supports(MessageTypes.DELETE_CONTACT))
    }

    @Test(expected = IllegalArgumentException::class)
    fun `重复注册同一指令号-构造即拒绝`() {
        ExecutorRegistry.of(SendTextExecutor(), SendTextExecutor())
    }

    /** FR-53 风险级矩阵（docs/01 FR-53 清单原文映射）。 */
    @Test
    fun `风险级矩阵-203L0_206L1_207随拉人_225L0_212L0`() {
        val registry = ExecutorRegistry.defaultP0()

        assertEquals(
            RiskLevel.L0,
            registry.executorFor(MessageTypes.SEND_MESSAGE)!!
                .riskLevel(CommandIntent.SendText("外部群测试群", "hi")),
        )
        assertEquals(
            RiskLevel.L1,
            registry.executorFor(MessageTypes.INIT_GROUP)!!
                .riskLevel(CommandIntent.InitGroup("新群")),
        )
        val updateExecutor = registry.executorFor(MessageTypes.UPDATE_GROUP)!!
        assertEquals(
            "仅公告 = L0",
            RiskLevel.L0,
            updateExecutor.riskLevel(CommandIntent.UpdateGroup("群", announcement = "a")),
        )
        assertEquals(
            "拉人 = L1",
            RiskLevel.L1,
            updateExecutor.riskLevel(CommandIntent.UpdateGroup("群", addMembers = listOf("张三"))),
        )
        assertEquals(
            RiskLevel.L0,
            registry.executorFor(MessageTypes.MODIFY_GROUP_MEMBER_INFO)!!
                .riskLevel(CommandIntent.ModifyGroupMemberInfo("群", "成员", markName = "m")),
        )
        assertEquals(
            RiskLevel.L0,
            registry.executorFor(MessageTypes.PASS_ALL_FRIEND_REQUEST)!!
                .riskLevel(CommandIntent.PassFriend()),
        )
    }

    @Test(expected = IllegalStateException::class)
    fun `类型不匹配意图-计划构建即失败（接线错误fail-fast）`() {
        val registry = ExecutorRegistry.defaultP0()
        // 把 203 意图喂给 206 执行器 = 接线错误
        registry.executorFor(MessageTypes.INIT_GROUP)!!
            .buildPlan(CommandIntent.SendText("群", "文本"))
    }
}
