package io.worktool.open.prototype.policy

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** NOT_RUN: exact fixtures, not an assertion that a synthetic flag sanitizes arbitrary content. */
class LabFixtureCommandPolicyTest {
    private fun message(target:String,text:String)=JSONObject().put("capability","message.send_text").put("payload",JSONObject().put("targetId",target).put("text",text))
    @Test fun exactTargetAndFixedTextOrStrictNonceOnly(){val p=LabFixtureCommandPolicy();assertTrue(p.permits(message("synthetic_target_001","LAB FIXTURE PING")));assertTrue(p.permits(message("synthetic_target_001","LAB FIXTURE NONCE "+"a".repeat(32))));for(t in listOf("real user content","LAB FIXTURE PING\n","LAB FIXTURE PING secret","LAB FIXTURE NONCE "+"a".repeat(31),"LAB FIXTURE NONCE "+"G".repeat(32)))assertFalse(p.permits(message("synthetic_target_001",t)));assertFalse(p.permits(message("target_other","LAB FIXTURE PING")))}
    @Test fun onlyBuiltinNoteReferences(){val p=LabFixtureCommandPolicy();assertTrue(p.permitsReferences(listOf("synthetic_note_001")));assertTrue(p.permitsReferences(listOf("synthetic_note_002")));assertFalse(p.permitsReferences(emptyList()));assertFalse(p.permitsReferences(listOf("synthetic_demo")));assertFalse(p.permitsReferences(listOf("/data/private")));assertFalse(p.permits(JSONObject().put("capability","accessibility.click")))}
}
