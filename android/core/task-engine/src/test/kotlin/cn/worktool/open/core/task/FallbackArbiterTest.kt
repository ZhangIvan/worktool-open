/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import cn.worktool.open.core.protocol.MessageTypes
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/** fallback 仲裁（docs/02 §5.2：fallbackLocalScheduling 默认关 + 不可达超阈值 + 纯本地模式）。 */
class FallbackArbiterTest {

    private var now = 0L
    private val clock = { now }

    @Test
    fun `默认关 - 即使不可达超阈值本地调度也不启用`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = false, unreachableThresholdMillis = 120_000),
            clock,
        )
        arbiter.onServerUnreachable()
        now = 10 * 60_000 // 失联 10 分钟
        assertTrue(!arbiter.localSchedulingEnabled(pureLocalMode = false))
    }

    @Test
    fun `纯本地模式恒启用（模式 A）`() {
        val arbiter = FallbackArbiter(LocalModeConfig(), clock)
        assertTrue(arbiter.localSchedulingEnabled(pureLocalMode = true))
    }

    @Test
    fun `fallback 开启 - 未达阈值不启用`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            clock,
        )
        arbiter.onServerUnreachable()
        now = 119_999
        assertTrue(!arbiter.localSchedulingEnabled(pureLocalMode = false))
        assertEquals(119_999, arbiter.unreachableDurationMillis())
    }

    @Test
    fun `fallback 开启 - 达阈值启用`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            clock,
        )
        arbiter.onServerUnreachable()
        now = 120_000
        assertTrue(arbiter.localSchedulingEnabled(pureLocalMode = false))
    }

    @Test
    fun `重连后计时清零 - 本地调度退出（定时事实源回到服务端）`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            clock,
        )
        arbiter.onServerUnreachable()
        now = 300_000
        assertTrue(arbiter.localSchedulingEnabled(pureLocalMode = false))
        arbiter.onServerReachable()
        now += 1_000
        assertEquals(0, arbiter.unreachableDurationMillis())
        assertTrue(!arbiter.localSchedulingEnabled(pureLocalMode = false))
    }

    @Test
    fun `重复失联通知不重置计时起点`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 120_000),
            clock,
        )
        arbiter.onServerUnreachable() // since=0
        now = 60_000
        arbiter.onServerUnreachable() // 不重置
        now = 120_000
        assertTrue(arbiter.localSchedulingEnabled(pureLocalMode = false)) // 若重置则此刻只 60s
    }

    @Test
    fun `从未失联 - 时长为 0 且不启用（阈值 0 也不得兜底，须确实处于不可达态）`() {
        val arbiter = FallbackArbiter(
            LocalModeConfig(fallbackLocalScheduling = true, unreachableThresholdMillis = 0),
            clock,
        )
        assertEquals(0, arbiter.unreachableDurationMillis())
        assertTrue(!arbiter.localSchedulingEnabled(pureLocalMode = false))
    }
}

/** 干跑报告器（FR-54）。 */
class DryRunRunnerTest {

    private var now = 42L
    private val runner = DryRunRunner { now }

    @Test
    fun `干跑报告含目标-参数-风险级三要素`() {
        val report = runner.report(
            ActionIntent(
                commandType = MessageTypes.SEND_MESSAGE,
                targetRoom = "外部群A",
                content = "报价内容",
                at = listOf("张三"),
                ruleId = "kr-price",
            ),
        )
        assertEquals("外部群A", report.target)
        assertEquals("报价内容", report.params["content"])
        assertEquals("张三", report.params["at"])
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("kr-price", report.ruleId)
        assertEquals(42L, report.atMillis)
    }

    @Test
    fun `风险级映射对齐 FR-53 分级表`() {
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.SEND_MESSAGE))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.PASS_ALL_FRIEND_REQUEST))
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.INIT_GROUP))
        // CR-t31-T31-R3：207 改群（含拉人）类型级兜底须与服务端 riskClassByType 一致
        // （载荷级动态分级以执行器侧为准：仅公告/备注时执行器判 L0）
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.UPDATE_GROUP))
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.SEND_MULTI_MESSAGE))
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.RELAY_MERGE_MESSAGE))
        assertEquals(RiskLevel.L2, RiskLevels.of(MessageTypes.DISMISS_GROUP))
        assertEquals(RiskLevel.L2, RiskLevels.of(MessageTypes.DELETE_CONTACT))
        assertEquals(RiskLevel.L2, RiskLevels.of(MessageTypes.ROBOT_QUEUE_CLEAR))
    }

    @Test
    fun `未登记类型 fail-closed 为 L2 - 端侧已实现普通指令为 L0（AUD-WM3-02，FR-53 裁决②）`() {
        // 未登记类型（不在 FR-53 分级表且端侧未实现）→ L2 fail-closed，
        // 与服务端 scheduler guard.go riskUnregistered→RiskL2 同口径
        //（231 VOICE_CALL 不在 server plannedActionNames，属真未登记；9999 = 未知类型）
        assertEquals(RiskLevel.L2, RiskLevels.of(MessageTypes.VOICE_CALL))
        assertEquals(RiskLevel.L2, RiskLevels.of(9999))
        // 端侧已实现普通指令（T3.1 P0 执行器集 203/212/225）→ L0（裁决③其余已实现=L0）
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.SEND_MESSAGE))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.PASS_ALL_FRIEND_REQUEST))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.MODIFY_GROUP_MEMBER_INFO))
        // 查询面 501/502/509（defaultFirstBatch 执行器已实现）→ L0（CR W-M3R
        // exec-gap 修正：server 侧 dryrun.go plannedActionNames 已登记、
        // guard.go ClassifyRisk→L0，端侧补齐即同指令同级；503~508 未实现仍 L2）
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.GET_GROUP_INFO))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.GET_FRIEND_INFO))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.GET_LOCAL_FILE))
        assertEquals(RiskLevel.L2, RiskLevels.of(MessageTypes.GET_MY_INFO))
    }
}
