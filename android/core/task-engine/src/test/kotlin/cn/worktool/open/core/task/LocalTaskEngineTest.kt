/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.protocol.RoomTypes
import cn.worktool.open.core.protocol.TextTypes
import cn.worktool.open.core.protocol.WeworkMessageBean
import cn.worktool.open.core.protocol.transport.Cancellable
import cn.worktool.open.core.protocol.transport.Scheduler
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDateTime
import java.time.ZoneId

/**
 * 引擎端到端（模式 A 全链路：规则 → 门禁/去重 → 执行/干跑 → 台账 → source=local 上报）。
 */
class LocalTaskEngineTest {

    private val zone = ZoneId.of("Asia/Shanghai")
    private var now = LocalDateTime.of(2026, 11, 2, 8, 0).atZone(zone).toInstant().toEpochMilli()
    private val clock = { now }

    /** 假执行器：记录调用，可编程结果。 */
    private class FakeExecutor(
        override val supportedType: Int,
        var outcome: ExecOutcome = ExecOutcome.OK,
    ) : ActionExecutor {
        val calls = ArrayList<ActionIntent>()
        override fun execute(intent: ActionIntent): ExecOutcome {
            calls.add(intent)
            return outcome
        }
    }

    /** 假上报通道：记录载荷，可编程成败。 */
    private class FakeReporter(var accept: Boolean = true) : LocalEventReporter {
        val reported = ArrayList<List<WeworkMessageBean>>()
        override fun report(beans: List<WeworkMessageBean>): Boolean {
            reported.add(beans)
            return accept
        }
    }

    private fun rules(vararg cron: CronJob) = RuleSetConfig(
        keywordRules = listOf(
            KeywordRule(
                ruleId = "kr-price",
                match = KeywordMatch(keywords = listOf("价格")),
                action = SendMessageAction(content = "报价请咨询客服"),
            ),
        ),
        friendAcceptRules = listOf(FriendAcceptRule(ruleId = "fr-all", action = PassFriendAction())),
        cronJobs = cron.toList(),
    )

    private fun message(text: String = "价格多少", time: Long = now) = MessageEvent(
        roomType = RoomTypes.EXTERNAL_CONTACT,
        textType = TextTypes.TEXT,
        targetRoom = "客户A",
        senderNames = listOf("张三"),
        text = text,
        eventTimeMillis = time,
    )

    @Test
    fun `关键词消息 - 命中规则执行并通过好友执行器`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val pass = FakeExecutor(MessageTypes.PASS_ALL_FRIEND_REQUEST)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(),
            rules = rules(),
            pureLocalMode = true,
            clock = clock,
            zone = zone,
        )
        engine.registerExecutor(send)
        engine.registerExecutor(pass)

        val outcomes = engine.onMessageEvent(message())
        assertEquals(1, outcomes.size)
        val executed = outcomes[0] as LocalTaskEngine.Outcome.Executed
        assertEquals("kr-price", executed.ruleId)
        assertEquals(0, executed.errorCode)
        assertEquals("客户A", send.calls.single().targetRoom) // 回复来源会话

        // FR-15：新好友事件 → PASS_FRIEND 意图 → 212 执行器
        val friend = engine.onFriendRequest(FriendRequestEvent("王五", "你好", now))
        assertEquals(MessageTypes.PASS_ALL_FRIEND_REQUEST, pass.calls.single().commandType)
        assertEquals(1, friend.size)
    }

    @Test
    fun `同一事件重复观测 - (ruleId,eventHash) 去重不双发`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(LocalModeConfig(), rules(), pureLocalMode = true, clock = clock, zone = zone)
        engine.registerExecutor(send)

        val event = message()
        engine.onMessageEvent(event)
        val dup = engine.onMessageEvent(event) // 同一观测（同 eventTime）重放
        assertEquals(1, send.calls.size)
        assertTrue(dup.single() is LocalTaskEngine.Outcome.DuplicateSkipped)
    }

    @Test
    fun `不同发生（新消息）各自执行`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(LocalModeConfig(), rules(), pureLocalMode = true, clock = clock, zone = zone)
        engine.registerExecutor(send)
        engine.onMessageEvent(message(time = now))
        now += 1_000
        engine.onMessageEvent(message(time = now))
        assertEquals(2, send.calls.size)
    }

    @Test
    fun `暂停态 - 本地规则阻断（FR-52 无新副作用）服务端指令只入队`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(LocalModeConfig(), rules(), pureLocalMode = true, clock = clock, zone = zone)
        engine.registerExecutor(send)
        engine.pauseManager().pause(reason = "用户接管")

        val blocked = engine.onMessageEvent(message())
        assertTrue(blocked.single() is LocalTaskEngine.Outcome.BlockedPaused)
        assertTrue(send.calls.isEmpty())

        val deferred = engine.onServerCommand(
            ServerCommand("cmd-1", MessageTypes.SEND_MESSAGE, "群B", "服务端内容", now),
        )
        assertTrue(deferred is LocalTaskEngine.Outcome.Deferred)
        assertTrue(send.calls.isEmpty()) // 只入队未执行

        // 显式确认恢复后按序执行暂存指令
        engine.pauseManager().requestResume()
        val flushed = engine.pauseManager().confirmResume()
        assertEquals(listOf("cmd-1"), flushed.map { it.commandId })
        val after = engine.onServerCommand(
            ServerCommand("cmd-2", MessageTypes.SEND_MESSAGE, "群B", "x", now),
        )
        assertTrue(after is LocalTaskEngine.Outcome.Executed) // 恢复后直接执行
        assertEquals(1, send.calls.size)
    }

    @Test
    fun `干跑 - 同路径产出报告且零副作用（FR-54）`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(LocalModeConfig(), rules(), pureLocalMode = true, clock = clock, zone = zone)
        engine.registerExecutor(send)

        val outcomes = engine.onMessageEvent(message(), dryRun = true)
        val report = (outcomes.single() as LocalTaskEngine.Outcome.DryRun).report
        assertEquals(MessageTypes.SEND_MESSAGE, report.commandType)
        assertEquals("客户A", report.target)
        assertEquals("报价请咨询客服", report.params["content"])
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertTrue(send.calls.isEmpty()) // 无副作用
    }

    @Test
    fun `cron - 纯本地模式到点执行，fallback 关且可达时不执行`() {
        val cron = CronJob(
            jobId = "cron-9",
            expression = "0 9 * * *",
            action = SendMessageAction(targetRoom = "早报群", content = "早上好"),
        )
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)

        // 模式 A（纯本地）：到点执行
        val local = LocalTaskEngine(
            LocalModeConfig(), rules(cron), pureLocalMode = true, clock = clock, zone = zone,
        )
        local.registerExecutor(send)
        assertTrue(local.cronTick().isEmpty()) // 08:00 未到点
        now = LocalDateTime.of(2026, 11, 2, 9, 0).atZone(zone).toInstant().toEpochMilli()
        val fired = local.cronTick()
        assertEquals(1, fired.size)
        assertEquals("早报群", send.calls.single().targetRoom)

        // 模式 B（配置了服务端）：fallback 默认关 → 任务到点也 gated（事实源=服务端）
        send.calls.clear()
        now = LocalDateTime.of(2026, 11, 3, 8, 0).atZone(zone).toInstant().toEpochMilli()
        val modeB = LocalTaskEngine(
            LocalModeConfig(), rules(cron), pureLocalMode = false, clock = clock, zone = zone,
        )
        modeB.registerExecutor(send)
        now = LocalDateTime.of(2026, 11, 3, 9, 0).atZone(zone).toInstant().toEpochMilli()
        assertTrue(modeB.cronTick().isEmpty()) // 09:00 已到点，但 fallback 关 → 不本地执行
        assertTrue(send.calls.isEmpty())
    }

    @Test
    fun `cron fallback - 不可达超阈值后本地接管，重连即退出`() {
        val cron = CronJob("cron-9", "0 9 * * *", action = SendMessageAction(targetRoom = "群", content = "x"))
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            rules = rules(cron),
            pureLocalMode = false,
            clock = clock,
            zone = zone,
        )
        engine.registerExecutor(send)

        engine.fallbackArbiter().onServerUnreachable(now) // 08:00 失联
        now = LocalDateTime.of(2026, 11, 2, 9, 0).atZone(zone).toInstant().toEpochMilli() // 失联 1h ≥ 120s 且到点
        assertEquals(1, engine.cronTick().size) // 阈值已过 → 本地接管执行
        assertEquals(1, send.calls.size)

        now = LocalDateTime.of(2026, 11, 3, 8, 50).atZone(zone).toInstant().toEpochMilli()
        engine.fallbackArbiter().onServerReachable(now)
        now = LocalDateTime.of(2026, 11, 3, 9, 0).atZone(zone).toInstant().toEpochMilli()
        assertTrue(engine.cronTick().isEmpty()) // 任务到点但已重连 → 事实源回服务端
    }

    @Test
    fun `重连合并 - source=local 上报含 ruleId 与 eventHash 且不重复上报`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val reporter = FakeReporter()
        val engine = LocalTaskEngine(
            LocalModeConfig(), rules(), pureLocalMode = false, reporter = reporter,
            clock = clock, zone = zone,
        )
        engine.registerExecutor(send)
        engine.onMessageEvent(message())

        val reported = engine.onReconnected()
        assertEquals(1, reported.size)
        val bean = reporter.reported.single().single()
        assertEquals("local", bean.source) // spec §5.4.5 冻结值
        assertEquals(MessageTypes.SEND_MESSAGE, bean.type)
        assertEquals("kr-price", bean.extras["ruleId"]?.let { (it as kotlinx.serialization.json.JsonPrimitive).content })
        assertEquals(64, bean.extras["eventHash"]!!.let { (it as kotlinx.serialization.json.JsonPrimitive).content }.length)

        // 已标记上报：再次重连不重复上报（服务端 (ruleId,eventHash) 去重之外的端侧收敛）
        assertTrue(engine.onReconnected().isEmpty())
        assertEquals(1, reporter.reported.size)
    }

    @Test
    fun `上报通道不可用 - 保留待上报态下次重连重试`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val reporter = FakeReporter(accept = false)
        val engine = LocalTaskEngine(
            LocalModeConfig(), rules(), pureLocalMode = false, reporter = reporter,
            clock = clock, zone = zone,
        )
        engine.registerExecutor(send)
        engine.onMessageEvent(message())

        assertTrue(engine.onReconnected().isEmpty()) // 未提交成功
        reporter.accept = true
        now += 5_000
        assertEquals(1, engine.onReconnected().size) // 重试成功
    }

    @Test
    fun `未注册执行器 - NoExecutor 可见（T3·1 未合并时的诚实结果）`() {
        val engine = LocalTaskEngine(LocalModeConfig(), rules(), pureLocalMode = true, clock = clock, zone = zone)
        val outcome = engine.onMessageEvent(message()).single()
        assertTrue(outcome is LocalTaskEngine.Outcome.NoExecutor)
        assertEquals(MessageTypes.SEND_MESSAGE, (outcome as LocalTaskEngine.Outcome.NoExecutor).commandType)
    }

    @Test
    fun `cron 驱动循环 - 经协议层 Scheduler 抽象唤醒`() {
        val cron = CronJob("cron-9", "0 9 * * *", action = SendMessageAction(content = "x"))
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(
            LocalModeConfig(), rules(cron), pureLocalMode = true, clock = clock, zone = zone,
        )
        engine.registerExecutor(send)

        // 手动调度器：捕获任务与延时
        var scheduled: Pair<Long, () -> Unit>? = null
        val scheduler = object : Scheduler {
            override fun schedule(delayMillis: Long, task: () -> Unit): Cancellable {
                scheduled = delayMillis to task
                return object : Cancellable {
                    override fun cancel() {}
                }
            }
        }
        engine.startCronLoop(scheduler)
        val (delay, task) = scheduled!!
        assertEquals(60 * 60_000L, delay) // 08:00 → 09:00 一小时

        now = LocalDateTime.of(2026, 11, 2, 9, 0).atZone(zone).toInstant().toEpochMilli()
        task() // 到点唤醒
        assertEquals(1, send.calls.size)
        engine.stopCronLoop()
    }

    /**
     * CR-t33-R-02 回归：模式 B（fallback 开、起步在线）下，唤醒落在可达期被门禁
     * 挡住时驱动环不得自灭——失联超阈值后仍能接管执行（docs/02 §5.2 兜底语义）。
     */
    @Test
    fun `cron 驱动循环 - 模式B可达期唤醒被门禁挡住后失联超阈值仍接管`() {
        val cron = CronJob("cron-9", "0 9 * * *", action = SendMessageAction(targetRoom = "群", content = "x"))
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(
            config = LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            rules = rules(cron),
            pureLocalMode = false,
            clock = clock,
            zone = zone,
        )
        engine.registerExecutor(send)

        // 捕获全部排程（不只最后一次）：驱动环存活性 = 唤醒后仍有新排程
        val scheduleCalls = ArrayList<Pair<Long, () -> Unit>>()
        val scheduler = object : Scheduler {
            override fun schedule(delayMillis: Long, task: () -> Unit): Cancellable {
                scheduleCalls.add(delayMillis to task)
                return object : Cancellable {
                    override fun cancel() {}
                }
            }
        }
        engine.startCronLoop(scheduler)
        assertEquals(1, scheduleCalls.size)
        assertEquals(60 * 60_000L, scheduleCalls[0].first) // 08:00 → 09:00

        // 09:00 唤醒（仍可达）：tick 被门禁挡住、水位不推进——循环必须重排（短轮询）
        now = LocalDateTime.of(2026, 11, 2, 9, 0).atZone(zone).toInstant().toEpochMilli()
        scheduleCalls.removeAt(0).second()
        assertEquals("门禁挡住时零本地执行", 0, send.calls.size)
        assertEquals("唤醒后必须重排（旧代码在此取 null 自灭）", 1, scheduleCalls.size)
        assertEquals("门禁期按轮询间隔重查（非 0 延迟忙转）", 1_000L, scheduleCalls[0].first)

        // 失联并超阈值：轮询唤醒 → 本地接管执行
        engine.fallbackArbiter().onServerUnreachable(now)
        now = LocalDateTime.of(2026, 11, 2, 10, 2).atZone(zone).toInstant().toEpochMilli() // 失联 62min ≥ 120s
        scheduleCalls.removeAt(0).second()
        assertEquals("失联超阈值后到点任务应补跑", 1, send.calls.size)
        assertEquals("执行后按下次到期重排（明日 09:00）", 1, scheduleCalls.size)
        val nextDelay = scheduleCalls[0].first
        val expected = LocalDateTime.of(2026, 11, 3, 9, 0).atZone(zone).toInstant().toEpochMilli() - now
        assertEquals(expected, nextDelay)
        engine.stopCronLoop()
    }

    /** CR-t33-R-03 回归（a）：干跑预检后同事件真实分派应 Executed（去重键不得吞掉）。 */
    @Test
    fun `干跑后真实分派 - 同事件不被去重键吞掉`() {
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(
            LocalModeConfig(), rules(), pureLocalMode = true, reporter = FakeReporter(),
            clock = clock, zone = zone,
        )
        engine.registerExecutor(send)

        val event = message()
        val dry = engine.onMessageEvent(event, dryRun = true)
        assertTrue(dry.single() is LocalTaskEngine.Outcome.DryRun)
        assertTrue(send.calls.isEmpty()) // 干跑零副作用

        val real = engine.onMessageEvent(event) // 同一观测真实执行
        assertTrue(
            "干跑不记账：真实分派应 Executed（旧代码落 DuplicateSkipped）",
            real.single() is LocalTaskEngine.Outcome.Executed,
        )
        assertEquals(1, send.calls.size)
    }

    /** CR-t33-R-03 回归（b）：纯干跑后 onReconnected 零上报（台账未被未发生的执行污染）。 */
    @Test
    fun `纯干跑 - onReconnected 零上报`() {
        val reporter = FakeReporter()
        val send = FakeExecutor(MessageTypes.SEND_MESSAGE)
        val engine = LocalTaskEngine(
            LocalModeConfig(), rules(), pureLocalMode = true, reporter = reporter,
            clock = clock, zone = zone,
        )
        engine.registerExecutor(send)

        engine.onMessageEvent(message(), dryRun = true)
        assertTrue("干跑不产生已执行事件，无可上报", engine.onReconnected().isEmpty())
        assertTrue(reporter.reported.isEmpty())
    }
}
