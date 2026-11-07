package io.worktool.open.prototype.core

/** Pure gate shared by real registry and JVM negative tests. No approval or permission is inferred. */
object FixtureTicketGate {
    fun permits(ticket:FixtureTicket,status:FixtureDriverStatus,target:FixtureTarget,windowId:Int,nowElapsedMs:Long):Boolean =
        status.serviceConnected&&status.locallyEnabled&&status.foreground&&ticket.target==target&&
            ticket.windowId==windowId&&ticket.serviceEpoch==status.serviceEpoch&&ticket.foregroundEpoch==status.foregroundEpoch&&
            nowElapsedMs<ticket.deadlineElapsedMs
    fun interrupted(inputRequested:Boolean,inputChanged:Boolean,inputVerified:Boolean,clickRequested:Boolean,code:String):FixtureDriverResult = when {
        clickRequested->FixtureDriverResult("execution_unknown","unknown",code,emptyList())
        inputChanged->FixtureDriverResult("partially_succeeded","partially_applied",code,emptyList())
        inputRequested&&!inputVerified->FixtureDriverResult("execution_unknown","unknown",code,emptyList())
        else->FixtureDriverResult("cancelled","not_applied",code,emptyList())
    }
}
