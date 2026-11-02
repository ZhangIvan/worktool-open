package io.worktool.open.prototype.accessibility

import io.worktool.open.prototype.policy.VariantFeatures
import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.AccessibilityServiceInfo
import android.view.accessibility.AccessibilityEvent

/** Candidate real service; only explicit user system-settings enablement, never automatic. */
class OwnFixtureAccessibilityService:AccessibilityService(){
    override fun onServiceConnected(){
        if(!VariantFeatures.ownFixtureAccessibility){disableSelf();return}
        val own=applicationContext.packageName
        if(own.isBlank()){disableSelf();return}
        val config=serviceInfo ?: AccessibilityServiceInfo()
        config.packageNames=arrayOf(own)
        config.eventTypes=AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED or AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED
        config.flags=AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS
        config.feedbackType=AccessibilityServiceInfo.FEEDBACK_GENERIC
        config.notificationTimeout=100
        serviceInfo=config
        if(serviceInfo.packageNames?.contentEquals(arrayOf(own))!=true){disableSelf();return}
        FixtureServiceRegistry.connected(this)
    }
    override fun onAccessibilityEvent(event:AccessibilityEvent?){FixtureServiceRegistry.event(this,event)}
    override fun onInterrupt(){FixtureServiceRegistry.interrupted(this)}
    override fun onUnbind(intent:android.content.Intent?):Boolean{FixtureServiceRegistry.disconnected(this,"SERVICE_UNBOUND");return super.onUnbind(intent)}
    override fun onDestroy(){FixtureServiceRegistry.disconnected(this,"SERVICE_DESTROYED");super.onDestroy()}
}
