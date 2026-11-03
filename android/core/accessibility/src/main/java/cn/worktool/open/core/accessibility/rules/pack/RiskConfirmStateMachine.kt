/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

/**
 * R7 风险确认流状态机（FR-56 首次开启；设计 §3.1）。
 *
 * ```
 *                 ┌────────────────────────────────────────────┐
 *                 ▼                                            │ 取消/返回
 *  DISABLED ──用户点「启用微信自动化」──▶ RISK_DISCLOSURE ─────────┤
 *  (默认)                              (风险声明弹窗)           │
 *                                           │ 勾选「我已知晓并自担风险」
 *                                           ▼ (checkbox 前确认钮置灰)
 *                                      ACK_CHECKED ──点「确认启用」──▶ ENABLED
 *                                           │                        │
 *                                           │ 取消                    │ FR-53 熔断
 *                                           └────────▶ DISABLED       ▼
 *                                                                    SUSPENDED ──重走全流程──▶ ENABLED
 * ```
 *
 * - **默认 DISABLED**；ENABLED 判定是 G6 开关门的唯一事实源——本状态机是纯逻辑核，
 *   持久化经 [persistSink] 端口交由 app 车道落本地存储（DataStore 等，**不依赖服务端下发**，
 *   防误开通；R7 要求）。重启后经 [restore] 重水化。
 * - 确认成功落 [RiskAckRecord]（ackVersion/timestamp/selectorsVersion）+ 审计回调——
 *   每条该包指令执行日志应携带 ackVersion（矩阵取证：微信列独立取证不与企微混标）。
 * - 非法迁移一律抛 [IllegalStateException]（fail-closed：宁可拒绝也不进未定义态）。
 */
class RiskConfirmStateMachine(
    val packId: String,
    /** 当前风险声明文案版本（文案升级 → 旧 ack 失效须重走确认流）。 */
    private var ackVersion: Int,
    /** 持久化端口（状态+最新确认记录；app 车道绑定 DataStore，本层不落盘）。 */
    private val persistSink: (State, RiskAckRecord?) -> Unit = { _, _ -> },
    /** 审计端口（SR-12 口径：type=「risk_confirm:FROM→TO」）。 */
    private val audit: (String, Long) -> Unit = { _, _ -> },
    private val clock: () -> Long = System::currentTimeMillis,
) {

    enum class State { DISABLED, RISK_DISCLOSURE, ACK_CHECKED, ENABLED, SUSPENDED }

    /** 确认落档记录（审计/执行日志携带 ackVersion 的数据源）。 */
    data class RiskAckRecord(
        val packId: String,
        val ackVersion: Int,
        /** 确认时刻（millis）。 */
        val ackAtMillis: Long,
        /** 确认时包选择器版本（major bump 后强制重确认的比对基准；未知=null）。 */
        val selectorsVersion: Int? = null,
    )

    private var stateValue: State = State.DISABLED
    private var ackRecordValue: RiskAckRecord? = null

    val state: State get() = stateValue

    /** G6 开关门判定（唯一事实源视图：仅 ENABLED 为 true）。 */
    val isEnabled: Boolean get() = stateValue == State.ENABLED

    /** 最近一次确认记录（未确认=null；断电重水化用）。 */
    val ackRecord: RiskAckRecord? get() = ackRecordValue

    // ---- 状态迁移（设计 §3.1 迁移表） ----

    /** 用户点「启用」入口：DISABLED/SUSPENDED → RISK_DISCLOSURE（SUSPENDED 重走全流程）。 */
    fun requestEnable() = transition(State.RISK_DISCLOSURE, from = setOf(State.DISABLED, State.SUSPENDED)) {
        "仅 DISABLED/SUSPENDED 可发起启用（当前 $stateValue）"
    }

    /** 勾选「我已知晓并自担风险」：RISK_DISCLOSURE → ACK_CHECKED。 */
    fun checkAck() = transition(State.ACK_CHECKED, from = setOf(State.RISK_DISCLOSURE)) {
        "仅风险声明页可勾选确认（当前 $stateValue）"
    }

    /** 取消勾选：ACK_CHECKED → RISK_DISCLOSURE（确认钮回到置灰）。 */
    fun uncheckAck() = transition(State.RISK_DISCLOSURE, from = setOf(State.ACK_CHECKED)) {
        "仅已勾选态可取消勾选（当前 $stateValue）"
    }

    /**
     * 点「确认启用」：ACK_CHECKED → ENABLED，落 [RiskAckRecord] 并经持久化/审计端口外发。
     * 从 RISK_DISCLOSURE 直接确认（未勾选）= 非法（确认钮置灰的语义在状态机层同样拒绝）。
     */
    fun confirmEnable(selectorsVersion: Int? = null): RiskAckRecord {
        transition(State.ENABLED, from = setOf(State.ACK_CHECKED)) {
            "确认启用须先勾选风险确认（当前 $stateValue，R7 两步确认）"
        }
        val record = RiskAckRecord(
            packId = packId,
            ackVersion = ackVersion,
            ackAtMillis = clock(),
            selectorsVersion = selectorsVersion,
        )
        ackRecordValue = record
        persistSink(stateValue, record)
        audit("risk_confirm:ACK(version=${record.ackVersion},at=${record.ackAtMillis})", record.ackAtMillis)
        return record
    }

    /** 取消/返回：RISK_DISCLOSURE/ACK_CHECKED → DISABLED（不落任何确认状态）。 */
    fun cancel() = transition(State.DISABLED, from = setOf(State.RISK_DISCLOSURE, State.ACK_CHECKED)) {
        "仅声明/已勾选态可取消（当前 $stateValue）"
    }

    /** FR-53 熔断触发：ENABLED → SUSPENDED（通知用户动作归 UI 层）。 */
    fun breakerTripped() = transition(State.SUSPENDED, from = setOf(State.ENABLED)) {
        "仅 ENABLED 可被熔断（当前 $stateValue）"
    }

    /**
     * pack major bump：`reconfirmOnMajorBump=true`（manifest.riskConfirm）时 ENABLED →
     * RISK_DISCLOSURE（下次指令前强制回到声明页），ackVersion 更新为新声明版本；
     * 策略未要求重确认（false）→ no-op（保持 ENABLED）。
     */
    fun requireReconfirmOnMajorBump(
        policyReconfirm: Boolean,
        newAckVersion: Int,
        newSelectorsVersion: Int? = null,
    ) {
        if (!policyReconfirm) return
        transition(State.RISK_DISCLOSURE, from = setOf(State.ENABLED)) {
            "仅 ENABLED 触发 major bump 重确认（当前 $stateValue）"
        }
        ackVersion = newAckVersion
        ackRecordValue?.let { audit("risk_confirm:BUMPED(old=${it.ackVersion},new=$newAckVersion)", clock()) }
        persistSink(stateValue, ackRecordValue?.copy(ackVersion = newAckVersion, selectorsVersion = newSelectorsVersion))
    }

    /** 重启重水化（app 车道从本地存储恢复；恢复后照常走 G6 判定）。 */
    fun restore(state: State, ackVersion: Int, record: RiskAckRecord? = null) {
        stateValue = state
        this.ackVersion = ackVersion
        ackRecordValue = record
        audit("risk_confirm:RESTORE($state)", clock())
    }

    /** G6 断言：`required=true` 的包未 ENABLED → [PackDisabledException]（101002 语义）。 */
    fun assertEnabledFor(pack: RulePack) {
        RulePackGate.assertEnabled(pack, isEnabled)
    }

    private inline fun transition(to: State, from: Set<State>, lazyError: () -> String) {
        if (stateValue !in from) throw IllegalStateException(lazyError())
        val fromState = stateValue
        stateValue = to
        persistSink(to, ackRecordValue)
        audit("risk_confirm:$fromState→$to", clock())
    }
}
