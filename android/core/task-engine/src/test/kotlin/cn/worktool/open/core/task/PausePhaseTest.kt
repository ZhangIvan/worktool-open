/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 暂停三分法（T4.1，docs/11 B2）：NOT_STARTED / PARTIALLY_EXECUTED / EFFECT_UNKNOWN
 * 三相分类、步骤证据、可恢复态与 exactly-once 续跑分发、引擎回执透传。
 */
class PausePhaseTest {

    private var now = 1_000L
    private val clock = { now }
    private val audits = ArrayList<PauseManager.AuditEvent>()
    private val pm = PauseManager(clock) { audits.add(it) }

    private fun interrupted(
        commandId: String? = "cmd-1",
        phase: PauseManager.PausePhase,
        steps: Int = 0,
        resumeState: String? = null,
    ) = PauseManager.InterruptedExecution(
        commandId = commandId,
        commandType = MessageTypes.SEND_MESSAGE,
        phase = phase,
        executedSteps = (1..steps).map {
            PauseManager.StepEvidence(stepId = "step-$it", description = "步骤$it", succeeded = true)
        },
        remainingStepIds = listOf("step-${steps + 1}"),
        resumeState = resumeState,
        pausedAtMillis = now,
        pauseSource = "floating-window",
    )

    // ---- 三相分类器（各 phase 至少一例） ----

    /** #1 NOT_STARTED：执行前拦截（原「只入队不执行」零副作用相）。 */
    @Test
    fun `分类器 - NOT_STARTED`() {
        assertEquals(PauseManager.PausePhase.NOT_STARTED, PauseManager.classifyPhase(false, false, false))
        assertEquals(PauseManager.PausePhase.NOT_STARTED, PauseManager.classifyPhase(false, true, false))
    }

    /** #2 EFFECT_UNKNOWN：动作已提交但效果未验证（禁盲重放相）。 */
    @Test
    fun `分类器 - EFFECT_UNKNOWN`() {
        assertEquals(PauseManager.PausePhase.EFFECT_UNKNOWN, PauseManager.classifyPhase(true, true, false))
    }

    /** #3 PARTIALLY_EXECUTED：有已完成步骤且无在途动作（可续跑相）。 */
    @Test
    fun `分类器 - PARTIALLY_EXECUTED`() {
        assertEquals(PauseManager.PausePhase.PARTIALLY_EXECUTED, PauseManager.classifyPhase(true, false, false))
        assertEquals(PauseManager.PausePhase.PARTIALLY_EXECUTED, PauseManager.classifyPhase(true, true, true))
    }

    // ---- 步骤证据 + 可恢复态 ----

    /** #4 记录被切断执行：证据快照含已完成步骤，审计留 execution_interrupted（SR-12）。 */
    @Test
    fun `证据 - 步骤证据与审计留痕`() {
        pm.pause(actor = "floating-window")
        pm.recordInterruptedExecution(interrupted(phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, steps = 2, resumeState = "plan#3"))
        val rec = pm.interruptedExecutions().single()
        assertEquals(2, rec.executedSteps.size)
        assertEquals(listOf("step-1", "step-2"), rec.executedSteps.map { it.stepId })
        assertEquals(listOf("step-3"), rec.remainingStepIds)
        assertEquals("plan#3", rec.resumeState)
        assertEquals("floating-window", rec.pauseSource)
        assertTrue(audits.any { it.type == "execution_interrupted" && it.reason!!.contains("PARTIALLY_EXECUTED") })
    }

    /** #5 isResumable 判定：仅 PARTIALLY_EXECUTED 且 resumeState 非空。 */
    @Test
    fun `可恢复判定 - 仅部分执行且有续跑令牌`() {
        assertTrue(!interrupted(phase = PauseManager.PausePhase.NOT_STARTED, resumeState = "x").isResumable)
        assertTrue(!interrupted(phase = PauseManager.PausePhase.EFFECT_UNKNOWN, resumeState = "x").isResumable)
        assertTrue(!interrupted(phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, resumeState = null).isResumable)
        assertTrue(interrupted(phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, resumeState = "x").isResumable)
    }

    /** #6 重复投递：同 commandId upsert 覆盖，证据不重复累计（T4.1 覆盖「重复投递」）。 */
    @Test
    fun `重复投递 - 同 commandId 上报覆盖不重复计`() {
        pm.recordInterruptedExecution(interrupted(phase = PauseManager.PausePhase.NOT_STARTED))
        now += 10
        pm.recordInterruptedExecution(
            interrupted(phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, steps = 3, resumeState = "plan#4"),
        )
        val all = pm.interruptedExecutions()
        assertEquals(1, all.size) // 覆盖而非追加
        assertEquals(3, all.single().executedSteps.size)
        assertEquals(2, audits.count { it.type == "execution_interrupted" }) // 每次上报都留审计
    }

    /** #7 commandId=null（本地规则意图）：追加不覆盖。 */
    @Test
    fun `本地规则意图 - 无 commandId 追加记录`() {
        pm.recordInterruptedExecution(interrupted(commandId = null, phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, resumeState = "a"))
        pm.recordInterruptedExecution(interrupted(commandId = null, phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, resumeState = "b"))
        assertEquals(2, pm.interruptedExecutions().size)
    }

    /** #8 drainResumable exactly-once：取走即出队，重复调用不重复分发（覆盖「恢复」）。 */
    @Test
    fun `续跑分发 - exactly-once 取走后不再分发`() {
        pm.recordInterruptedExecution(interrupted(commandId = "r-1", phase = PauseManager.PausePhase.PARTIALLY_EXECUTED, steps = 1, resumeState = "plan#2"))
        pm.recordInterruptedExecution(interrupted(commandId = "n-1", phase = PauseManager.PausePhase.NOT_STARTED))
        pm.recordInterruptedExecution(interrupted(commandId = "u-1", phase = PauseManager.PausePhase.EFFECT_UNKNOWN))

        val drained = pm.drainResumable()
        assertEquals(listOf("r-1"), drained.map { it.commandId }) // 只取可恢复项
        assertTrue(pm.drainResumable().isEmpty()) // 二次取走=空
        assertEquals(2, pm.interruptedExecutions().size) // 证据（不可恢复项）保留
    }

    // ---- 回执透传（原回执保留 + phase additive） ----

    /** #9 ExecOutcome.pausedByTakeover：101003 + phase（不再统一 success=false 掩盖进度）。 */
    @Test
    fun `回执 - pausedByTakeover 携带三相`() {
        val o = ExecOutcome.pausedByTakeover(PauseManager.PausePhase.PARTIALLY_EXECUTED)
        assertTrue(!o.success)
        assertEquals(101003, o.errorCode)
        assertEquals(PauseManager.PausePhase.PARTIALLY_EXECUTED, o.pausePhase)
        assertTrue(o.errorReason!!.contains("PARTIALLY_EXECUTED"))
        // 原回执构造形态不受影响（原回执保留）
        val legacy = ExecOutcome(success = true, errorCode = 0)
        assertNull(legacy.pausePhase)
    }

    /** #10 引擎端到端：执行器在执行中被暂停切断 → Outcome.Executed 透传 phase。 */
    @Test
    fun `引擎端到端 - 执行中被切断透传 PARTIALLY_EXECUTED`() {
        /** 假执行器：执行到一半触发暂停并上报三分法（模拟 T3.1 执行器安全断点行为）。 */
        class MidPauseExecutor(private val pm: PauseManager) : ActionExecutor {
            override val supportedType = MessageTypes.SEND_MESSAGE
            override fun execute(intent: ActionIntent): ExecOutcome {
                pm.pause(actor = "user-takeover")
                val phase = PauseManager.classifyPhase(started = true, lastActionCommitted = false, effectVerified = false)
                pm.recordInterruptedExecution(
                    PauseManager.InterruptedExecution(
                        commandId = null,
                        commandType = intent.commandType,
                        phase = phase,
                        executedSteps = listOf(PauseManager.StepEvidence("s1", "打开会话", true)),
                        remainingStepIds = listOf("s2", "s3"),
                        resumeState = "after-open-conversation",
                        pausedAtMillis = now,
                        pauseSource = "user-takeover",
                    ),
                )
                return ExecOutcome.pausedByTakeover(phase)
            }
        }

        val engine = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = RuleSetConfig(
                keywordRules = listOf(
                    KeywordRule(
                        ruleId = "kr",
                        match = KeywordMatch(keywords = listOf("价格")),
                        action = SendMessageAction(content = "回复"),
                    ),
                ),
            ),
            pureLocalMode = true,
            clock = clock,
        )
        engine.registerExecutor(MidPauseExecutor(engine.pauseManager()))

        val outcomes = engine.onMessageEvent(
            MessageEvent(
                roomType = 1,
                textType = 1,
                targetRoom = "客户A",
                senderNames = listOf("张三"),
                text = "价格多少",
                eventTimeMillis = now,
            ),
            dryRun = false,
        )
        val executed = outcomes.filterIsInstance<LocalTaskEngine.Outcome.Executed>().single()
        assertEquals(101003, executed.errorCode)
        assertEquals(PauseManager.PausePhase.PARTIALLY_EXECUTED, executed.pausePhase)

        // 恢复闭环：confirmResume 后取走可恢复态（步骤证据 + 续跑令牌）
        val pm = engine.pauseManager()
        pm.requestResume()
        pm.confirmResume()
        val resumable = pm.drainResumable().single()
        assertEquals("after-open-conversation", resumable.resumeState)
        assertEquals(listOf("s1"), resumable.executedSteps.map { it.stepId })
    }

    /** #11 暂停期本地规则仍按 BlockedPaused 阻断（三分法不放松 FR-52 门禁）。 */
    @Test
    fun `门禁保留 - 暂停期本地规则阻断不变`() {
        val e = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = RuleSetConfig(
                keywordRules = listOf(
                    KeywordRule(
                        ruleId = "kr",
                        match = KeywordMatch(keywords = listOf("价格")),
                        action = SendMessageAction(content = "回复"),
                    ),
                ),
            ),
            pureLocalMode = true,
            clock = clock,
        )
        e.pauseManager().pause(actor = "user-takeover")
        assertTrue(e.pauseManager().isPaused)
        val outcome = e.onMessageEvent(
            MessageEvent(1, 1, "房间", listOf("张三"), "价格", now),
        ).single()
        assertTrue(outcome is LocalTaskEngine.Outcome.BlockedPaused)
    }
}
