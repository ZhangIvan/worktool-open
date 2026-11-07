package io.worktool.open.prototype.accessibility

import io.worktool.open.prototype.core.FixtureDriver
import io.worktool.open.prototype.core.FixtureSessionPermit

/** Compatibility accessor only. The former free-form viewId action surface is removed.
 * All actual platform actions now require FixtureServiceRegistry's consumed, epoch-bound ticket.
 */
class FixtureAccessibilityAdapter {
    fun driver(permit:FixtureSessionPermit):FixtureDriver=FixtureServiceRegistry.driver(permit)
}
