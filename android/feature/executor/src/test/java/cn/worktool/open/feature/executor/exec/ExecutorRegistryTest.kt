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
import cn.worktool.open.feature.executor.fake.FakeLocalFiles
import cn.worktool.open.feature.executor.fake.RecordingQuerySink
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

    // ---- v0.1 首批全量注册表（docs/05 P0 首批行：五指令 + 501/502/509） ----

    @Test
    fun `首批注册表-八指令覆盖defaultP0超集`() {
        val registry = ExecutorRegistry.defaultFirstBatch(
            querySink = RecordingQuerySink(),
            files = FakeLocalFiles(mapOf(), null),
        )
        assertEquals(
            setOf(
                MessageTypes.SEND_MESSAGE,
                MessageTypes.INIT_GROUP,
                MessageTypes.UPDATE_GROUP,
                MessageTypes.MODIFY_GROUP_MEMBER_INFO,
                MessageTypes.PASS_ALL_FRIEND_REQUEST,
                MessageTypes.GET_GROUP_INFO,
                MessageTypes.GET_FRIEND_INFO,
                MessageTypes.GET_LOCAL_FILE,
            ),
            registry.supportedTypes,
        )
        assertTrue("defaultP0 是首批子集", ExecutorRegistry.defaultP0().supportedTypes.all { registry.supports(it) })
        // 503/505 未实现 → 首批之外（fail-closed）
        assertFalse(registry.supports(MessageTypes.GET_MY_INFO))
        assertFalse(registry.supports(MessageTypes.GET_RECENT_LIST))
    }

    /** FR-53：查询类只读指令 = L0（裁决③「其余已实现指令=L0」；501/502/509 未入 L1/L2 基线表）。 */
    @Test
    fun `风险级矩阵-查询类501_502_509均L0`() {
        val registry = ExecutorRegistry.defaultFirstBatch(
            querySink = RecordingQuerySink(),
            files = FakeLocalFiles(mapOf(), null),
        )
        assertEquals(RiskLevel.L0, registry.executorFor(MessageTypes.GET_GROUP_INFO)!!.riskLevel(CommandIntent.GetGroupList()))
        assertEquals(RiskLevel.L0, registry.executorFor(MessageTypes.GET_FRIEND_INFO)!!.riskLevel(CommandIntent.GetFriendList()))
        assertEquals(RiskLevel.L0, registry.executorFor(MessageTypes.GET_LOCAL_FILE)!!.riskLevel(CommandIntent.GetLocalFile()))
        assertEquals(RiskLevel.L0, registry.executorFor(MessageTypes.GET_LOCAL_FILE)!!.riskLevel(CommandIntent.GetLocalFile(filePath = "/x/y.log")))
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
