/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.session

/**
 * 入向 seq 检查（protocol/spec.md §5.4.2：每会话每方向严格递增，从 1 起）。
 *
 * - 期望 seq == lastReceived+1；
 * - seq ≤ lastReceived = 重复/回退；
 * - seq > lastReceived+1 = 跳号（触发 903 重新同步）。
 * 篡改 seq 在 HMAC 步已失败（C-9）；本类只判「真实签名的乱序/回退」→ 4405+903。
 */
class SessionSequencer {

    private var lastReceived = 0L

    val nextExpected: Long get() = lastReceived + 1

    sealed interface Verdict {
        /** seq == 期望值（已推进计数）。 */
        object Expected : Verdict

        /** seq ≤ lastReceived：重复/回退帧。 */
        data class Stale(val seq: Long, val lastReceived: Long) : Verdict

        /** seq > lastReceived+1：跳号，需 903 重新同步。 */
        data class Gap(val seq: Long, val expected: Long) : Verdict
    }

    /** 检查并推进（仅 Expected 时推进）。 */
    fun checkAndAdvance(seq: Long): Verdict = when {
        seq == lastReceived + 1 -> {
            lastReceived = seq
            Verdict.Expected
        }
        seq <= lastReceived -> Verdict.Stale(seq, lastReceived)
        else -> Verdict.Gap(seq, lastReceived + 1)
    }

    /** 重连后新连接 seq 重新从 1 起（spec §5.4.2：重连不重置持久确认语义，仅计数复位）。 */
    fun resetForReconnect() {
        lastReceived = 0
    }

    fun lastReceived(): Long = lastReceived
}

/**
 * 出向 seq 分配（每会话每方向严格递增，从 1 起；与 [SessionSequencer] 分方向实例化）。
 * 重连后新连接从 1 重新起算（spec §5.4.2）。
 */
class SendSequencer {
    private var last = 0L

    @Synchronized
    fun next(): Long {
        last += 1
        return last
    }

    @Synchronized
    fun reset() {
        last = 0
    }

    @Synchronized
    fun last(): Long = last
}

/**
 * nonce 双向 5 分钟去重窗口（protocol/spec.md §5.2.3）。
 * 每方向独立实例化；窗口内重复 → nonce 重放（关闭码 4402）。
 */
class NonceWindow(
    private val windowMillis: Long = 300_000,
    private val clock: () -> Long,
) {

    private val seen = LinkedHashMap<String, Long>()

    /**
     * 检查并记录。返回 false = 窗口内重放。
     * 记录发生在检查时（而非帧完全通过后）：防同 nonce 重放竞态；
     * 后续步骤失败时会话按鉴权类失败关闭（spec §5.2.4），不产生误放行。
     */
    @Synchronized
    fun checkAndRecord(nonce: String, now: Long = clock()): Boolean {
        prune(now)
        if (seen.containsKey(nonce)) return false
        seen[nonce] = now
        return true
    }

    @Synchronized
    fun prune(now: Long = clock()) {
        val it = seen.entries.iterator()
        while (it.hasNext()) {
            val e = it.next()
            if (now - e.value > windowMillis) it.remove() else break // LinkedHashMap 插入序 = 时间序
        }
    }

    @Synchronized
    fun size(): Int = seen.size
}

/**
 * eventId 去重（protocol/spec.md §5.4.3）：
 * 重复 eventId 同 payloadDigest → 幂等回执原确认（不得重复副作用）；
 * 异 payloadDigest → 冲突，关闭 4405。
 */
class EventDeduplicator(
    private val retentionMillis: Long = 300_000,
    private val clock: () -> Long,
) {

    private val seen = LinkedHashMap<String, Entry>()

    private data class Entry(val digest: String, val at: Long)

    sealed interface Verdict {
        object First : Verdict
        data class DuplicateSameDigest(val digest: String) : Verdict
        data class Conflict(val observed: String, val declared: String) : Verdict
    }

    @Synchronized
    fun observe(eventId: String, payloadDigest: String, now: Long = clock()): Verdict {
        prune(now)
        val prior = seen[eventId]
        return if (prior == null) {
            seen[eventId] = Entry(payloadDigest, now)
            Verdict.First
        } else if (prior.digest == payloadDigest) {
            Verdict.DuplicateSameDigest(prior.digest)
        } else {
            Verdict.Conflict(prior.digest, payloadDigest)
        }
    }

    @Synchronized
    private fun prune(now: Long) {
        val it = seen.entries.iterator()
        while (it.hasNext()) {
            val e = it.next()
            if (now - e.value.at > retentionMillis) it.remove() else break
        }
    }

    @Synchronized
    fun size(): Int = seen.size
}
