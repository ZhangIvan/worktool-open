package io.worktool.open.prototype.accessibility

import android.accessibilityservice.AccessibilityService
import android.view.accessibility.AccessibilityEvent

/** Source only: deliberately absent from Manifest and accessibility settings.
 * No auto-enable, no permission clicking, no boot service, no external app package access.
 * IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED.
 */
class OwnFixtureAccessibilityService : AccessibilityService() {
    private val adapter by lazy { FixtureAccessibilityAdapter(this) }
    override fun onAccessibilityEvent(event: AccessibilityEvent?) { adapter.onEvent(event) }
    override fun onInterrupt() { adapter.interrupted() }
    override fun onDestroy() { adapter.interrupted();super.onDestroy() }
}
