package io.worktool.open.prototype.policy

import java.util.concurrent.atomic.AtomicLong

/** Cancel epoch is captured at the bridge boundary, never recreated by delayed worker work. */
class ConnectionIntentGate {
    private val epoch=AtomicLong()
    fun capture():Long=epoch.get()
    fun invalidate(){epoch.incrementAndGet()}
    fun current(ticket:Long):Boolean=ticket==epoch.get()
}
