package io.worktool.open.prototype.core

import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean

/** In-memory revocation only, never server authorization. No invalidated object can be revived.
 * A final-gate-passed/system-submitted action may complete after invalidation: retain unknown/evidence.
 */
class FixtureSessionPermit {
    private val valid = AtomicBoolean(true)
    private val operations = ConcurrentHashMap<String, FixtureOperationPermit>()
    fun isValid(): Boolean = valid.get()
    fun invalidate() { valid.set(false) }
    fun operation(commandId: String): FixtureOperationPermit =
        operations.computeIfAbsent(commandId) { FixtureOperationPermit(this) }
    fun cancel(commandId: String) { operation(commandId).invalidate() }
}

class FixtureOperationPermit internal constructor(private val session: FixtureSessionPermit) {
    private val valid = AtomicBoolean(true)
    fun isValid(): Boolean = session.isValid() && valid.get()
    fun invalidate() { valid.set(false) }
}
