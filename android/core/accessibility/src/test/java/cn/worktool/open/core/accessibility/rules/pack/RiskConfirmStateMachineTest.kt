/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** R7 风险确认流状态机（FR-56 首次开启；设计 §3.1 迁移表逐行）。 */
class RiskConfirmStateMachineTest {

    private var now = 10_000L
    private val persisted = ArrayList<Pair<RiskConfirmStateMachine.State, RiskConfirmStateMachine.RiskAckRecord?>>()
    private val audits = ArrayList<String>()

    private fun sm() = RiskConfirmStateMachine(
        packId = WeChatRulePack.PACK_ID,
        ackVersion = WeChatRulePack.ACK_VERSION,
        persistSink = { s, r -> persisted.add(s to r) },
        audit = { type, _ -> audits.add(type) },
        clock = { now },
    )

    /** 走到 ACK_CHECKED 的快捷路径。 */
    private fun RiskConfirmStateMachine.toAckChecked() {
        requestEnable()
        checkAck()
    }

    /** #1 全流程：DISABLED→RISK_DISCLOSURE→ACK_CHECKED→ENABLED，落 ack 记录+持久化+审计。 */
    @Test
    fun `全流程 - 弹窗到勾选到启用`() {
        val m = sm()
        assertEquals(RiskConfirmStateMachine.State.DISABLED, m.state)
        assertFalse(m.isEnabled)
        m.requestEnable()
        assertEquals(RiskConfirmStateMachine.State.RISK_DISCLOSURE, m.state)
        m.checkAck()
        assertEquals(RiskConfirmStateMachine.State.ACK_CHECKED, m.state)
        now += 500
        val record = m.confirmEnable(selectorsVersion = 1)
        assertEquals(RiskConfirmStateMachine.State.ENABLED, m.state)
        assertTrue(m.isEnabled)
        assertEquals(WeChatRulePack.ACK_VERSION, record.ackVersion)
        assertEquals(10_500L, record.ackAtMillis)
        assertEquals(record, m.ackRecord)
        assertEquals(RiskConfirmStateMachine.State.ENABLED to record, persisted.last())
        assertTrue(audits.contains("risk_confirm:ACK_CHECKED→ENABLED"))
    }

    /** #2 取消（声明页）：RISK_DISCLOSURE → DISABLED，不落任何确认状态。 */
    @Test
    fun `取消 - 声明页返回 DISABLED`() {
        val m = sm()
        m.requestEnable()
        m.cancel()
        assertEquals(RiskConfirmStateMachine.State.DISABLED, m.state)
        assertFalse(m.isEnabled)
    }

    /** #3 取消（已勾选）：ACK_CHECKED → DISABLED（同样不落确认）。 */
    @Test
    fun `取消 - 已勾选态返回 DISABLED`() {
        val m = sm()
        m.toAckChecked()
        m.cancel()
        assertEquals(RiskConfirmStateMachine.State.DISABLED, m.state)
    }

    /** #4 跳过勾选直接确认 = 非法（checkbox 前确认钮置灰的状态机层同构拒绝）。 */
    @Test
    fun `两步确认 - 未勾选直接确认被拒绝`() {
        val m = sm()
        m.requestEnable()
        assertThrows(IllegalStateException::class.java) { m.confirmEnable() }
        assertEquals(RiskConfirmStateMachine.State.RISK_DISCLOSURE, m.state) // 状态未被破坏
    }

    /** #5 取消勾选回声明页（确认钮回到置灰）。 */
    @Test
    fun `勾选可逆 - uncheck 回 RISK_DISCLOSURE`() {
        val m = sm()
        m.toAckChecked()
        m.uncheckAck()
        assertEquals(RiskConfirmStateMachine.State.RISK_DISCLOSURE, m.state)
        assertFalse(m.isEnabled)
    }

    /** #6 FR-53 熔断：ENABLED → SUSPENDED；SUSPENDED 重走全流程才能回 ENABLED。 */
    @Test
    fun `熔断 - SUSPENDED 后须重走全流程`() {
        val m = sm()
        m.toAckChecked()
        m.confirmEnable()
        m.breakerTripped()
        assertEquals(RiskConfirmStateMachine.State.SUSPENDED, m.state)
        assertFalse(m.isEnabled)
        // SUSPENDED 直接确认=非法；必须 requestEnable 重走全流程
        assertThrows(IllegalStateException::class.java) { m.confirmEnable() }
        m.requestEnable()
        m.checkAck()
        val record = m.confirmEnable()
        assertEquals(RiskConfirmStateMachine.State.ENABLED, m.state)
        assertNotNull(record)
    }

    /** #7 major bump 且策略要求重确认：ENABLED → RISK_DISCLOSURE，ackVersion 更新。 */
    @Test
    fun `major bump - 强制回到声明页并更新 ackVersion`() {
        val m = sm()
        m.toAckChecked()
        m.confirmEnable(selectorsVersion = 1)
        m.requireReconfirmOnMajorBump(policyReconfirm = true, newAckVersion = 2, newSelectorsVersion = 2)
        assertEquals(RiskConfirmStateMachine.State.RISK_DISCLOSURE, m.state)
        assertFalse(m.isEnabled) // 下次指令前强制重新确认
        // 重走流程后新 ack 记录版本=2
        m.checkAck()
        val record = m.confirmEnable()
        assertEquals(2, record.ackVersion)
    }

    /** #8 major bump 但策略未要求 → no-op（保持 ENABLED）。 */
    @Test
    fun `major bump - 策略未要求时保持 ENABLED`() {
        val m = sm()
        m.toAckChecked()
        m.confirmEnable()
        m.requireReconfirmOnMajorBump(policyReconfirm = false, newAckVersion = 2)
        assertEquals(RiskConfirmStateMachine.State.ENABLED, m.state)
        assertTrue(m.isEnabled)
    }

    /** #9 非法迁移全面拒绝：默认态勾选/确认/熔断均不可（fail-closed 不进未定义态）。 */
    @Test
    fun `非法迁移 - 默认态全部拒绝且状态不变`() {
        val m = sm()
        assertThrows(IllegalStateException::class.java) { m.checkAck() }
        assertThrows(IllegalStateException::class.java) { m.confirmEnable() }
        assertThrows(IllegalStateException::class.java) { m.breakerTripped() }
        assertThrows(IllegalStateException::class.java) { m.uncheckAck() }
        assertEquals(RiskConfirmStateMachine.State.DISABLED, m.state)
    }

    /** #10 ENABLED 态重复「点启用入口」= 非法（已开启无需再走流）。 */
    @Test
    fun `非法迁移 - ENABLED 态不可重复发起启用`() {
        val m = sm()
        m.toAckChecked()
        m.confirmEnable()
        assertThrows(IllegalStateException::class.java) { m.requestEnable() }
        assertEquals(RiskConfirmStateMachine.State.ENABLED, m.state)
    }

    /** #11 重启重水化：restore 恢复 ENABLED 与 ack 记录（G6 事实源=本地持久标志）。 */
    @Test
    fun `重水化 - restore 恢复开启态与确认记录`() {
        val first = sm()
        first.toAckChecked()
        val record = first.confirmEnable(selectorsVersion = 1)

        val reborn = sm()
        assertEquals(RiskConfirmStateMachine.State.DISABLED, reborn.state) // 新实例默认关
        reborn.restore(RiskConfirmStateMachine.State.ENABLED, WeChatRulePack.ACK_VERSION, record)
        assertTrue(reborn.isEnabled)
        assertEquals(record, reborn.ackRecord)
        assertTrue(audits.contains("risk_confirm:RESTORE(ENABLED)"))
    }

    /** #12 G6 联动：未 ENABLED 时对微信包断言 → PackDisabledException（101002）。 */
    @Test
    fun `G6 联动 - 未开启断言抛 PackDisabledException`() {
        val pack = WeChatRulePack.parse()
        val m = sm()
        val e = assertThrows(PackDisabledException::class.java) { m.assertEnabledFor(pack) }
        assertTrue(e.message!!.contains("101002"))
        m.toAckChecked()
        m.confirmEnable()
        m.assertEnabledFor(pack) // ENABLED 后放行（不抛）
    }

    /** #13 确认流产出的 ack 记录供执行日志携带（R7：微信列独立取证口径）。 */
    @Test
    fun `取证 - ack 记录含 packId 与 selectorsVersion`() {
        val m = sm()
        m.toAckChecked()
        val record = m.confirmEnable(selectorsVersion = 3)
        assertEquals(WeChatRulePack.PACK_ID, record.packId)
        assertEquals(3, record.selectorsVersion)
    }
}
