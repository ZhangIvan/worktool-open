package io.worktool.open.prototype.policy

import java.net.URI

/** Variant-selected boundary. No cleartext-specific implementation is present in main. */
interface EndpointPolicy {
    val requiresExplicitConnectionOrigin: Boolean
    fun parse(raw: String): String
    fun confirm(raw: String): String
    fun authorize(raw: String): String
    fun clearConsent()
}

class HttpsEndpointPolicy : EndpointPolicy {
    override val requiresExplicitConnectionOrigin=false
    override fun parse(raw:String):String {
        require(raw==raw.trim() && !raw.contains('\\')) { "INVALID_ORIGIN" }
        val u=URI(raw)
        require(u.scheme=="https" && !u.host.isNullOrBlank() && u.rawUserInfo==null && u.rawQuery==null && u.rawFragment==null &&
            u.rawPath in listOf("","/") && u.port in -1..65535 && u.port!=0) { "HTTPS_ORIGIN_REQUIRED" }
        return "https://${u.rawAuthority}"
    }
    override fun confirm(raw:String)=parse(raw)
    override fun authorize(raw:String)=parse(raw)
    override fun clearConsent()=Unit
}

data class VariantPresentation(val banner:String,val endpointHint:String,val description:String,val confirmationWarning:String?,val fixtureExample:String?=null)
