package io.worktool.open.prototype.policy

import org.json.JSONObject

/** Cleartext lab must never carry arbitrary text, even when the wire says synthetic. */
class LabFixtureCommandPolicy : CommandPolicy {
    private val nonceTemplate=Regex("LAB FIXTURE NONCE [a-f0-9]{32}")
    override fun permits(body:JSONObject):Boolean = when(body.optString("capability")) {
        "message.send_text" -> {
            val p=body.optJSONObject("payload")
            p!=null && p.optString("targetId")=="synthetic_target_001" &&
                (p.optString("text")=="LAB FIXTURE PING" || nonceTemplate.matches(p.optString("text")))
        }
        "file.export" -> true // core requires exact approved scope and explicit local note-hash confirmation
        else -> false
    }
    override fun permitsReferences(references:List<String>) = references.isNotEmpty() &&
        references.all { it in setOf("synthetic_note_001","synthetic_note_002") }
}
