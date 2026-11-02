/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.task

/**
 * 离线/在线动作仲裁器（docs/02 §5.2：优先级 **用户接管 > 服务端指令 > 本地规则**）。
 *
 * 两个职责：
 * 1. [arbitrate]：多个来源竞争同一执行窗口时选出唯一胜者（数值大者胜出；
 *    [Candidate.priorityBoost] 允许来源内部再分层，如服务端加急指令）；
 * 2. [gate]：暂停态（FR-52）下按来源裁定处置——用户接管动作照常执行（接管即用户
 *    亲自主导）、服务端在线指令**只入队不执行**、本地规则直接阻断（暂停生效后无新副作用）。
 */
object CommandArbiter {

    /** 参与仲裁的候选（来源 + 意图 + 来源内加权）。 */
    data class Candidate(
        val source: Source,
        val intent: ActionIntent,
        /** 来源优先级基础上的加权（可为负；0 = 不加权）。 */
        val priorityBoost: Int = 0,
    ) {
        val effectivePriority: Int get() = SourcePriority.of(source) + priorityBoost
    }

    /** 仲裁结果：胜者 + 被压制的候选（按优先级从高到低）。 */
    data class Arbitration(val winner: Candidate, val suppressed: List<Candidate>)

    /**
     * 选出胜者。平级时按入参顺序稳定取先者（确定性，避免依赖随机/时间）。
     * 空输入返回 null。
     */
    fun arbitrate(candidates: List<Candidate>): Arbitration? {
        if (candidates.isEmpty()) return null
        val ordered = candidates.sortedByDescending { it.effectivePriority } // stable
        return Arbitration(winner = ordered.first(), suppressed = ordered.drop(1))
    }

    /** 暂停门禁裁定（FR-52 + docs/02 §5.2「暂停态下在线指令同样只入队」）。 */
    enum class GateDecision {
        /** 放行执行。 */
        EXECUTE,

        /** 只入队不执行（暂停态下的服务端在线指令）。 */
        ENQUEUE_ONLY,

        /** 阻断（暂停态下的本地规则产物——不得产生任何新副作用）。 */
        BLOCKED,
    }

    fun gate(paused: Boolean, source: Source): GateDecision {
        if (!paused) return GateDecision.EXECUTE
        return when (source) {
            Source.USER_TAKEOVER -> GateDecision.EXECUTE
            Source.SERVER_COMMAND -> GateDecision.ENQUEUE_ONLY
            Source.LOCAL_RULE -> GateDecision.BLOCKED
        }
    }
}
