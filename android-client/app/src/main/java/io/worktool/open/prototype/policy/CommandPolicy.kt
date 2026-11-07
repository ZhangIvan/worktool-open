package io.worktool.open.prototype.policy

import org.json.JSONObject

/** Additional build-variant restrictions, never an extension of the remote capability enum. */
interface CommandPolicy {
    fun permits(body:JSONObject):Boolean
    fun permitsReferences(references:List<String>):Boolean
}
object StandardCommandPolicy : CommandPolicy {
    override fun permits(body:JSONObject)=true // frozen wire validation and core authorization still apply
    override fun permitsReferences(references:List<String>)=true // core still checks app-owned fixture allowlist
}
