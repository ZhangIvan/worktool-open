/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** 仲裁器与优先级常量（docs/02 §5.2：用户接管 > 服务端指令 > 本地规则）。 */
class CommandArbiterTest {

    private fun intent(source: Source, type: Int = MessageTypes.SEND_MESSAGE) =
        ActionIntent(commandType = type, content = "x", source = source)

    @Test
    fun `优先级常量冻结 - 用户接管大于服务端指令大于本地规则`() {
        assertTrue(SourcePriority.USER_TAKEOVER > SourcePriority.SERVER_COMMAND)
        assertTrue(SourcePriority.SERVER_COMMAND > SourcePriority.LOCAL_RULE)
        assertEquals(300, SourcePriority.USER_TAKEOVER)
        assertEquals(200, SourcePriority.SERVER_COMMAND)
        assertEquals(100, SourcePriority.LOCAL_RULE)
    }

    @Test
    fun `用户接管压倒服务端指令`() {
        val user = CommandArbiter.Candidate(Source.USER_TAKEOVER, intent(Source.USER_TAKEOVER))
        val server = CommandArbiter.Candidate(Source.SERVER_COMMAND, intent(Source.SERVER_COMMAND))
        val winner = CommandArbiter.arbitrate(listOf(server, user))!!.winner
        assertEquals(Source.USER_TAKEOVER, winner.source)
    }

    @Test
    fun `服务端指令压倒本地规则`() {
        val server = CommandArbiter.Candidate(Source.SERVER_COMMAND, intent(Source.SERVER_COMMAND))
        val local = CommandArbiter.Candidate(Source.LOCAL_RULE, intent(Source.LOCAL_RULE))
        val winner = CommandArbiter.arbitrate(listOf(local, server))!!.winner
        assertEquals(Source.SERVER_COMMAND, winner.source)
    }

    @Test
    fun `本地规则之间平级按入参顺序稳定取胜`() {
        val a = CommandArbiter.Candidate(Source.LOCAL_RULE, intent(Source.LOCAL_RULE).copy(ruleId = "a"))
        val b = CommandArbiter.Candidate(Source.LOCAL_RULE, intent(Source.LOCAL_RULE).copy(ruleId = "b"))
        val result = CommandArbiter.arbitrate(listOf(a, b))!!
        assertEquals("a", result.winner.intent.ruleId)
        assertEquals(listOf("b"), result.suppressed.map { it.intent.ruleId })
    }

    @Test
    fun `来源内加权可调整同源排序但不越级`() {
        val localBoosted = CommandArbiter.Candidate(
            Source.LOCAL_RULE, intent(Source.LOCAL_RULE), priorityBoost = 99,
        ) // 100+99=199 < 200
        val server = CommandArbiter.Candidate(Source.SERVER_COMMAND, intent(Source.SERVER_COMMAND))
        assertEquals(Source.SERVER_COMMAND, CommandArbiter.arbitrate(listOf(localBoosted, server))!!.winner.source)
    }

    @Test
    fun `空候选返回 null`() {
        assertNull(CommandArbiter.arbitrate(emptyList()))
    }

    @Test
    fun `暂停门禁 - 服务端只入队 本地规则阻断 用户接管放行（02 §5·2 + FR-52）`() {
        assertEquals(CommandArbiter.GateDecision.ENQUEUE_ONLY, CommandArbiter.gate(true, Source.SERVER_COMMAND))
        assertEquals(CommandArbiter.GateDecision.BLOCKED, CommandArbiter.gate(true, Source.LOCAL_RULE))
        assertEquals(CommandArbiter.GateDecision.EXECUTE, CommandArbiter.gate(true, Source.USER_TAKEOVER))
    }

    @Test
    fun `运行态全部放行`() {
        Source.values().forEach {
            assertEquals(CommandArbiter.GateDecision.EXECUTE, CommandArbiter.gate(false, it))
        }
    }
}
