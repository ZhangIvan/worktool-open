package io.worktool.open.prototype.core

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/** NOT_RUN: frozen 1.1 shape/compatibility gate, not platform validation. */
class FixtureWireValidationTest {
    private fun command(text:String="synthetic fixture")=JSONObject().put("commandId","command_fixture").put("attemptId","attempt_fixture").put("capability","fixture.ui.submit_text").put("payloadDigest","a".repeat(64)).put("expiresAt","2030-01-01T00:02:00Z").put("policyVersion",1).put("requiresLocalConfirmation",true).put("payload",JSONObject().put("targetPackage","io.worktool.open.prototype").put("targetActivity","io.worktool.open.prototype.ui.FixtureActivity").put("text",text))
    private fun rejected(block:()->Unit){try{block();fail("must reject")}catch(_:IllegalArgumentException){}catch(_:IllegalStateException){}}
    @Test fun newCapabilityOnlyV11WithExplicitConfirmation(){WireValidation.command(command(),"1.1");rejected{WireValidation.command(command(),"1.0")};rejected{WireValidation.command(command().put("requiresLocalConfirmation",false),"1.1")}}
    @Test fun labTargetIsNotAnApprovedFixtureTarget(){rejected{val b=command();b.getJSONObject("payload").put("targetPackage","io.worktool.open.prototype.lab");WireValidation.command(b,"1.1")}}
    @Test fun foreignTargetsSelectorsAndMalformedUnicodeRejected(){rejected{val b=command();b.getJSONObject("payload").put("targetPackage","com.other.app");WireValidation.command(b,"1.1")};rejected{val b=command();b.getJSONObject("payload").put("viewId","arbitrary");WireValidation.command(b,"1.1")};rejected{WireValidation.command(command("\uD800"),"1.1")};rejected{WireValidation.command(command("x".repeat(501)),"1.1")};WireValidation.command(command("\uD83D\uDE00".repeat(500)),"1.1")}
}
