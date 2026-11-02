/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.ActionExecutor
import cn.worktool.open.core.task.ActionIntent
import cn.worktool.open.core.task.ExecOutcome
import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.task.MessageEvent
import cn.worktool.open.core.protocol.RoomTypes
import cn.worktool.open.core.protocol.TextTypes
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

/** app 接线（T3.3）：LocalMode 装配面可用、T3.1 执行器注册入口生效。 */
class LocalModeTest {

    @After
    fun tearDown() {
        LocalMode.reset()
    }

    @Test
    fun `默认装配 - 模式B安全默认且幂等`() {
        val engine = LocalMode.engine(
            rulesText = """
                {"cronJobs": [{"jobId": "cron-9", "expression": "0 9 * * *",
                  "action": {"type": "SEND_MESSAGE", "targetRoom": "早报群", "content": "早上好"}}]}
            """.trimIndent(),
        )
        assertSame(engine, LocalMode.engine()) // 幂等：同一实例
        assertTrue(engine.rules.keywordRules.isEmpty())
        assertFalse(engine.config.fallbackLocalScheduling) // 02 §5.2 默认关
        // CR-t33-R-04：默认 = 模式 B（非纯本地）——即使规则带 cron，本地调度也被
        // fallback 仲裁门禁（可达且未开 fallback → 事实源=服务端），到点零执行。
        assertTrue(engine.cronTick().isEmpty())
    }

    @Test
    fun `配置与规则注入 - 解析进引擎`() {
        val engine = LocalMode.engine(
            modeConfigText = """{"fallbackLocalScheduling": true, "unreachableThresholdMillis": 60000}""",
            rulesText = """
                {"keywordRules": [{"ruleId": "kr-1",
                  "match": {"keywords": ["价格"]},
                  "action": {"type": "SEND_MESSAGE", "content": "已收到"}}]}
            """.trimIndent(),
        )
        assertTrue(engine.config.fallbackLocalScheduling)
        assertEquals(60_000L, engine.config.unreachableThresholdMillis)
        assertEquals(1, engine.rules.keywordRules.size)

        // 注册 T3.1 风格执行器后本地规则可执行（FR-39 模式A 链路打通）
        var executed: ActionIntent? = null
        LocalMode.registerExecutor(object : ActionExecutor {
            override val supportedType = MessageTypes.SEND_MESSAGE
            override fun execute(intent: ActionIntent): ExecOutcome {
                executed = intent
                return ExecOutcome.OK
            }
        })
        val outcome = engine.onMessageEvent(
            MessageEvent(
                roomType = RoomTypes.EXTERNAL_CONTACT,
                textType = TextTypes.TEXT,
                targetRoom = "客户A",
                senderNames = listOf("张三"),
                text = "价格？",
                eventTimeMillis = 1L,
            ),
        )
        assertTrue(outcome.single() is cn.worktool.open.core.task.LocalTaskEngine.Outcome.Executed)
        assertEquals("kr-1", executed?.ruleId)
    }

    @Test(expected = IllegalStateException::class)
    fun `未装配先注册执行器被拒绝`() {
        LocalMode.reset()
        LocalMode.registerExecutor(object : ActionExecutor {
            override val supportedType = 203
            override fun execute(intent: ActionIntent) = ExecOutcome.OK
        })
    }
}
