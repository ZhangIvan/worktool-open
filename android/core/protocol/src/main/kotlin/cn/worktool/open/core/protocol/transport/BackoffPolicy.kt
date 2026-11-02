/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

/**
 * 断线重连指数退避（protocol/spec.md §3）：
 * - v1 兼容通道（上游还原）：起步 5s，上限 600s，无限循环；
 * - v2 安全会话：同指数算法，起步 5s，上限默认 120s（可配）。
 */
class BackoffPolicy(
    private val initialMillis: Long = 5_000,
    private val maxMillis: Long = 120_000,
    private val multiplier: Double = 2.0,
    /** 可注入抖动（默认无，保证测试确定性）。 */
    private val jitter: (Long) -> Long = { it },
) {

    private var current = initialMillis

    /** 取本次退避时长并推进到下一档（封顶 maxMillis）。 */
    fun nextDelayMillis(): Long {
        val delay = jitter(current.coerceAtMost(maxMillis))
        current = (current * multiplier).toLong().coerceAtMost(maxMillis)
        return delay
    }

    /** 成功连接后复位到起步档。 */
    fun reset() {
        current = initialMillis
    }

    companion object {
        /** v1 兼容通道（上游还原：5s→600s 上限无限循环）。 */
        fun v1(): BackoffPolicy = BackoffPolicy(initialMillis = 5_000, maxMillis = 600_000)

        /** v2 安全会话（默认上限 120s，可配）。 */
        fun v2(maxMillis: Long = 120_000): BackoffPolicy = BackoffPolicy(initialMillis = 5_000, maxMillis = maxMillis)
    }
}
