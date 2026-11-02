package io.worktool.open.prototype.policy

import org.junit.Assert.*
import org.junit.Test

/** Candidate table-driven JVM tests. NOT_RUN; no sockets, DNS, devices or actual lab address. */
class LabEndpointPolicyTest {
    private fun origin(a:Int,b:Int,c:Int,d:Int,port:Int=8080)="http://$a.$b.$c.$d:$port"
    private fun denied(action:()->Unit){try{action();fail("must reject")}catch(_:IllegalArgumentException){}catch(_:IllegalStateException){}}
    @Test fun rfc1918EdgesAndExplicitPorts(){
        val valid=listOf(listOf(10,0,0,0),listOf(10,255,255,255),listOf(172,16,0,0),listOf(172,31,255,255),listOf(192,168,0,0),listOf(192,168,255,255))
        for(o in valid) for(port in listOf(1,80,8080,65535)){val s=origin(o[0],o[1],o[2],o[3],port);assertEquals(s,LabEndpointPolicy().parse(s))}
        val outside=listOf(listOf(9,255,255,255),listOf(11,0,0,0),listOf(172,15,255,255),listOf(172,32,0,0),listOf(192,167,255,255),listOf(192,169,0,0),listOf(127,0,0,1),listOf(169,254,1,1),listOf(100,64,1,1),listOf(0,0,0,0),listOf(224,0,0,1),listOf(255,255,255,255))
        outside.forEach{o->denied{LabEndpointPolicy().parse(origin(o[0],o[1],o[2],o[3]))}}
    }
    @Test fun allAmbiguousSpellingsFailBeforeAnyConsent(){
        val s=origin(10,20,30,40)
        val host=s.removePrefix("http://").substringBefore(':')
        val rejected=listOf("",s+"/",s+"/path",s+"?x=1",s+"#fragment",s+"\n"," $s","$s ",s.replace("http://","HTTP://"),s.replace("http://","https://"),s.replace("http://","ws://"),s.replace("http://","wss://"),
            "http://example.test:8080","http://localhost:8080","http://[::1]:8080","http://[::ffff:$host]:8080", "http://$host", "http://$host:0","http://$host:65536","http://$host:08080","http://$host:+8080","http://$host:8080.",
            "http://$host.:8080","http://user@$host:8080","http://user:pass@$host:8080","http://$host\\:8080","http://$host:8080\\evil",s.replace("10.","010."),s.replace("10.","0x0a."),"http://169090600:8080","http://0x0a141e28:8080",s.replace("10.","%31%30."),s.replace(".","%2e"),s.replace("10.","10%00."),s.replace("30.","256."),s.replace("30.","-1."),s.replace("10.","１０."))
        rejected.forEach { raw->denied{LabEndpointPolicy().confirm(raw)} }
    }
    @Test fun unsetThenConfirmedSingleOriginAndExplicitClear(){val p=LabEndpointPolicy();val first=origin(10,20,30,40);val second=origin(10,20,30,41);denied{p.authorize(first)};p.confirm(first);assertEquals(first,p.authorize(first));denied{p.authorize(second)};denied{p.confirm(second)};denied{p.authorize(first.replace(":8080",":8081"))};p.clearConsent();denied{p.authorize(first)};assertEquals(second,p.confirm(second))}
    @Test fun httpAndWebsocketHandshakeCannotSelectDifferentOrigin(){val p=LabEndpointPolicy();val raw=origin(10,20,30,40);val routes=AuthorizedRoutes(p);denied{routes.url(raw,AuthorizedRoutes.ENROLL)};p.confirm(raw);val enroll=routes.url(raw,AuthorizedRoutes.ENROLL);val session=routes.url(raw,AuthorizedRoutes.SESSION);val ws=routes.webSocketHandshake(raw);for(u in listOf(enroll,session,ws)){assertEquals("http",u.scheme);assertEquals(enroll.host,u.host);assertEquals(enroll.port,u.port)};assertEquals(session,ws);denied{routes.webSocketHandshake(origin(10,20,30,41))};denied{routes.url(raw,"/redirect")};p.clearConsent();denied{routes.webSocketHandshake(raw)}}
    @Test fun noFactoryDefaultOriginAndVariantIsRestricted(){val p=VariantFactory.endpointPolicy();assertTrue(p.requiresExplicitConnectionOrigin);denied{p.authorize(origin(10,20,30,40))};assertTrue(VariantFactory.commandPolicy() is LabFixtureCommandPolicy)}
}
