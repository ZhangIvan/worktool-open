/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.accessibility.tree.ScrollDirection
import cn.worktool.open.core.accessibility.tree.UiNode
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.RoomTypes
import cn.worktool.open.core.protocol.TextTypes
import cn.worktool.open.core.task.ActionIntent
import cn.worktool.open.core.task.KeywordMatch
import cn.worktool.open.core.task.KeywordRule
import cn.worktool.open.core.task.LocalModeConfig
import cn.worktool.open.core.task.LocalTaskEngine
import cn.worktool.open.core.task.MessageEvent
import cn.worktool.open.core.task.RuleSetConfig
import cn.worktool.open.core.task.SendMessageAction
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.NeverPausedPauseManager
import cn.worktool.open.feature.executor.runtime.UiDriver
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * T3.1 ↔ T3.3 装配适配器测试（app 层合流点；JVM 纯逻辑，经最小 UiDriver 桩）。
 * 真机 UiDriver 归后续任务卡（W-M3R），本测试验证意图映射/暂停门一致性/结果回传。
 */
class FeatureExecutorBindingTest {

    /** 最小 UiDriver 桩：无界面（currentRoot=null → T3.1 计划首步失败）。 */
    private class StubDriver : UiDriver {
        var now = 0L
        override fun currentRoot(): UiNode? = null
        override fun click(node: UiNode): Boolean = true
        override fun inputText(node: UiNode, text: String) = true
        override fun scroll(node: UiNode, direction: ScrollDirection) = true
        override fun goBack() = true
        override fun sleep(ms: Long) { now += ms }
        override fun nowMillis(): Long = now
    }

    @Test
    fun `意图映射 - 203 转 SendText 212 转 PassFriend`() {
        assertEquals(
            CommandIntent.SendText("客户A", "报价", listOf("张三")),
            FeatureExecutorBinding.mapIntent(
                ActionIntent(
                    commandType = MessageTypes.SEND_MESSAGE,
                    targetRoom = "客户A",
                    content = "报价",
                    at = listOf("张三"),
                ),
            ),
        )
        assertTrue(
            FeatureExecutorBinding.mapIntent(
                ActionIntent(commandType = MessageTypes.PASS_ALL_FRIEND_REQUEST, content = "你好"),
            ) is CommandIntent.PassFriend,
        )
    }

    @Test
    fun `意图映射 - targetRoom 缺失或未覆盖指令号返回 null`() {
        assertNull(
            FeatureExecutorBinding.mapIntent(
                ActionIntent(commandType = MessageTypes.SEND_MESSAGE, content = "x", targetRoom = null),
            ),
        )
        assertNull(
            FeatureExecutorBinding.mapIntent(ActionIntent(commandType = MessageTypes.INIT_GROUP, targetRoom = "g")),
        )
    }

    @Test
    fun `引擎暂停态 - T3·1 只读暂停门一致（EnginePauseGate）`() {
        val engine = LocalTaskEngine(LocalModeConfig(), RuleSetConfig(), pureLocalMode = true)
        val gate = FeatureExecutorBinding.EnginePauseGate(engine)
        assertFalse(gate.isPaused())
        assertNull(gate.pauseSource())
        engine.pauseManager().pause(actor = "floating-window")
        assertTrue(gate.isPaused())
        assertEquals("floating-window", gate.pauseSource())
    }

    @Test
    fun `RunnerActionExecutor - 未映射指令号在执行面前拦截（101010）`() {
        val executor = FeatureExecutorBinding.RunnerActionExecutor(
            CommandRunner(StubDriver(), NeverPausedPauseManager()),
            ExecutorRegistry.defaultP0(),
            MessageTypes.INIT_GROUP,
        )
        val outcome = executor.execute(ActionIntent(commandType = MessageTypes.INIT_GROUP, targetRoom = "g"))
        assertFalse(outcome.success)
        assertEquals(101010, outcome.errorCode)
    }

    @Test
    fun `RunnerActionExecutor - T3·1 执行结果回传（无界面 → FAILED 透传）`() {
        val executor = FeatureExecutorBinding.RunnerActionExecutor(
            CommandRunner(StubDriver(), NeverPausedPauseManager()),
            ExecutorRegistry.defaultP0(),
            MessageTypes.PASS_ALL_FRIEND_REQUEST,
        )
        val outcome = executor.execute(
            ActionIntent(commandType = MessageTypes.PASS_ALL_FRIEND_REQUEST),
        )
        assertFalse(outcome.success)
        assertTrue(outcome.errorCode != 101010) // 已进 T3.1 执行面（首步无界面失败），非映射拦截
    }

    @Test
    fun `bind - 注册面全指令号进引擎 本地规则经同一执行面落地`() {
        val engine = LocalTaskEngine(
            LocalModeConfig(),
            RuleSetConfig(
                keywordRules = listOf(
                    KeywordRule(
                        ruleId = "kr-1",
                        match = KeywordMatch(keywords = listOf("价格")),
                        action = SendMessageAction(content = "已收到"),
                    ),
                ),
            ),
            pureLocalMode = true,
        )
        FeatureExecutorBinding.bind(
            engine,
            CommandRunner(StubDriver(), FeatureExecutorBinding.EnginePauseGate(engine)),
            ExecutorRegistry.defaultP0(),
        )
        val outcome = engine.onMessageEvent(
            MessageEvent(
                roomType = RoomTypes.EXTERNAL_CONTACT,
                textType = TextTypes.TEXT,
                targetRoom = "客户A",
                senderNames = listOf("张三"),
                text = "价格？",
                eventTimeMillis = 1L,
            ),
        ).single()
        // 绑定前为 NoExecutor；绑定后进入 T3.1 执行面（桩驱动无界面 → FAILED 回传）
        assertTrue(outcome is LocalTaskEngine.Outcome.Executed)
        assertFalse((outcome as LocalTaskEngine.Outcome.Executed).errorCode == 101010)
    }
}
