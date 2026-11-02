package io.worktool.open.prototype.policy

/** LAB CLEARTEXT only. In-memory, one exact confirmed origin; never accepts DNS or URL normalization.
 * Private addresses are supplied at runtime only. No probing, DNS lookup, redirects or stored consent.
 */
class LabEndpointPolicy(override val budget: LabClientBudget = LabClientBudget()) : EndpointPolicy {
    override val requiresExplicitConnectionOrigin=true
    private var approved:Pair<Long,String>?=null
    init { budget.onInvalidated { epoch,_ -> synchronized(this) { if(approved?.first==epoch) approved=null } } }
    // Decimal octets forbid leading zeros; explicit decimal port also has a unique representation.
    private val syntax=Regex("http://([1-9][0-9]{0,2}|0)\\.([1-9][0-9]{0,2}|0)\\.([1-9][0-9]{0,2}|0)\\.([1-9][0-9]{0,2}|0):([1-9][0-9]{0,4})")
    @Synchronized override fun parse(raw:String):String {
        val m=syntax.matchEntire(raw) ?: error("LAB_CANONICAL_ORIGIN_REQUIRED")
        val octets=(1..4).map { m.groupValues[it].toInt() }
        require(octets.all { it in 0..255 }) { "LAB_IPV4_REQUIRED" }
        val port=m.groupValues[5].toInt();require(port in 1..65535) { "LAB_PORT_REQUIRED" }
        val privateRange=octets[0]==10 || (octets[0]==172 && octets[1] in 16..31) || (octets[0]==192 && octets[1]==168)
        require(privateRange) { "LAB_RFC1918_REQUIRED" }
        return raw
    }
    @Synchronized override fun confirm(raw:String):String {
        val parsed=parse(raw)
        require(approved==null || approved?.second==parsed) { "LAB_SECOND_ORIGIN_DENIED" }
        val existing=approved
        if(existing!=null){require(budget.valid(existing.first)){"LAB_LEASE_EXPIRED"};return parsed}
        approved=budget.grant() to parsed;return parsed
    }
    @Synchronized override fun authorize(raw:String):String {
        val parsed=parse(raw);val permit=approved;require(permit!=null && parsed==permit.second && budget.valid(permit.first)) { "LAB_ORIGIN_NOT_CONFIRMED" };return parsed
    }
    @Synchronized override fun clearConsent(){approved=null;budget.cancel()}
}
