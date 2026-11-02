package io.worktool.open.prototype.accessibility

import android.accessibilityservice.AccessibilityService
import android.os.Bundle
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo

/** Actual platform API adapter, intentionally NOT registered in Manifest.
 * IMPLEMENTED_NOT_BUILT / NOT_DEVICE_TESTED. Only own app FixtureActivity, never WeCom.
 * No remote capability maps to this adapter. Must remain default disabled.
 */
class FixtureAccessibilityAdapter(private val service: AccessibilityService) {
    companion object {
        const val OWN_PACKAGE = "io.worktool.open.prototype"
        const val FIXTURE_ACTIVITY = "$OWN_PACKAGE.ui.FixtureActivity"
        const val INPUT_ID = "$OWN_PACKAGE:id/ui_fixture_input"
        const val BUTTON_ID = "$OWN_PACKAGE:id/ui_fixture_send"
    }
    enum class Outcome { DISABLED, REQUIRES_PERMISSION, WRONG_WINDOW, AMBIGUOUS, UNSUPPORTED, APPLIED, NOT_APPLIED }
    private var locallyEnabled = false
    private var fixtureWindowId: Int? = null
    fun setLocalEnabled(enabled: Boolean) { locallyEnabled=enabled; if(!enabled) fixtureWindowId=null }
    fun onEvent(event: AccessibilityEvent?) {
        if(event==null) { fixtureWindowId=null; return }
        if(event.eventType==AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED) {
            fixtureWindowId=if(event.packageName?.toString()==OWN_PACKAGE && event.className?.toString()==FIXTURE_ACTIVITY) event.windowId else null
        }
        if(event.packageName?.toString()!=OWN_PACKAGE) fixtureWindowId=null
    }
    fun interrupted() { locallyEnabled=false;fixtureWindowId=null }
    fun setText(viewId: String, text: String): Outcome {
        if(viewId!=INPUT_ID || text.codePointCount(0,text.length)>512) return Outcome.UNSUPPORTED
        return act(viewId) { node ->
            if(!node.isEditable) return@act false
            val args=Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,text) }
            node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT,args)
        }
    }
    fun click(viewId: String): Outcome {
        if(viewId!=BUTTON_ID) return Outcome.UNSUPPORTED
        return act(viewId) { node -> node.isClickable && node.performAction(AccessibilityNodeInfo.ACTION_CLICK) }
    }
    @Suppress("DEPRECATION")
    private fun act(viewId: String, action: (AccessibilityNodeInfo)->Boolean): Outcome {
        if(!locallyEnabled) return Outcome.DISABLED
        val root=service.rootInActiveWindow ?: return Outcome.REQUIRES_PERMISSION
        try {
            val permittedWindow=fixtureWindowId ?: return Outcome.WRONG_WINDOW
            if(root.packageName?.toString()!=OWN_PACKAGE || root.windowId!=permittedWindow) return Outcome.WRONG_WINDOW
            val nodes=root.findAccessibilityNodeInfosByViewId(viewId)
            try {
                if(nodes.size!=1) return Outcome.AMBIGUOUS
                val node=nodes.single()
                if(node.packageName?.toString()!=OWN_PACKAGE || node.windowId!=permittedWindow ||
                    node.viewIdResourceName!=viewId || !node.isVisibleToUser || !node.isEnabled) return Outcome.WRONG_WINDOW
                return if(action(node)) Outcome.APPLIED else Outcome.NOT_APPLIED
            } finally { nodes.forEach { it.recycle() } }
        } finally { root.recycle() }
    }
}

/** Local-only facade. Remote enum intentionally remains exactly the two synthetic capabilities. */
class FixtureActionDispatcher(private val adapter: FixtureAccessibilityAdapter?) {
    fun state(): String = if(adapter==null) "requires_permission" else "disabled"
    fun setText(viewId: String,text: String): FixtureAccessibilityAdapter.Outcome =
        adapter?.setText(viewId,text) ?: FixtureAccessibilityAdapter.Outcome.REQUIRES_PERMISSION
    fun click(viewId: String): FixtureAccessibilityAdapter.Outcome =
        adapter?.click(viewId) ?: FixtureAccessibilityAdapter.Outcome.REQUIRES_PERMISSION
}
