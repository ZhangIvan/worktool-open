package io.worktool.open.prototype.policy

/** Linearization point for accepting a pairing response; no disk/network work under this lock. */
class PairingAttemptGate {
    private var epoch=0L
    private var consumed=false
    @Synchronized fun begin():Long { epoch++; consumed=false; return epoch }
    @Synchronized fun invalidate() { epoch++; consumed=false }
    @Synchronized fun isCurrent(attempt:Long,foreground:Boolean):Boolean = attempt==epoch && foreground && !consumed
    @Synchronized fun consume(attempt:Long,foreground:Boolean):Boolean {
        if(attempt!=epoch || !foreground || consumed) return false
        consumed=true
        return true
    }
}
