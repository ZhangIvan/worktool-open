/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

import java.time.ZoneId

/**
 * 本地 cron 调度器（FR-38 模式A：本地定时独立跑；docs/02 §2）。
 *
 * 纯粹的「时刻 → 到期任务」计算器：由 [LocalTaskEngine] 在 tick 时驱动（事件源 =
 * 协议层 Scheduler 或引擎持有者），不自行起线程——JVM 单测直接推进时钟断言。
 *
 * 补跑语义：停机/离线期间错过的多次触发**只补一次**（catch-up 合并为单次，
 * 定时任务幂等语义优先，避免恢复瞬间风暴）。
 */
class LocalCronScheduler(
    jobs: List<CronJob>,
    private val zone: ZoneId = ZoneId.systemDefault(),
    private val clock: () -> Long = System::currentTimeMillis,
) {

    /** 到期执行单元：任务 + 本次到期对应的计划时刻。 */
    data class DueExecution(val job: CronJob, val scheduledForMillis: Long)

    private data class State(
        val job: CronJob,
        val expression: CronExpression,
        var nextFireMillis: Long,
    )

    private val states: List<State> = jobs.map { j ->
        State(
            job = j,
            expression = CronExpression.parse(j.expression), // 构造期校验（fail fast）
            nextFireMillis = 0L,
        )
    }

    init {
        // 初始化水位：以构造时刻为基准计算各自下一次触发
        val now = clock()
        states.forEach { it.nextFireMillis = it.expression.nextFire(now, zone) ?: Long.MAX_VALUE }
    }

    /** 取出到期任务（nextFire ≤ now 的启用任务），并推进各自水位。 */
    fun due(now: Long): List<DueExecution> {
        val out = ArrayList<DueExecution>()
        for (s in states) {
            if (!s.job.enabled || s.nextFireMillis == Long.MAX_VALUE || s.nextFireMillis > now) continue
            out.add(DueExecution(s.job, s.nextFireMillis))
            // 错过多次只补一次：水位直接跳到 now 之后的下一次
            s.nextFireMillis = s.expression.nextFire(now, zone) ?: Long.MAX_VALUE
        }
        return out.sortedBy { it.scheduledForMillis }
    }

    /**
     * 最近一次到期时刻（调度休眠时长计算输入）；无启用任务返回 null。
     *
     * **含已到期未消费的水位（≤ now 也返回，CR-t33-R-02）**：门禁挡住 [due] 时水位
     * 不推进，若在此过滤掉到期时刻，唤醒回调触发时将取到 null → 驱动环死
     * （模式 B 可达→失联超阈值后本地 cron 永不触发）。调用方
     * （[LocalTaskEngine.scheduleNext]）负责区分「立即消费」与「门禁期短轮询」。
     */
    fun nextWakeMillis(now: Long): Long? =
        states.asSequence()
            .filter { it.job.enabled && it.nextFireMillis != Long.MAX_VALUE }
            .map { it.nextFireMillis }
            .minOrNull()
}
