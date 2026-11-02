package io.worktool.open.prototype.policy

import org.junit.Assert.*
import org.junit.Test

/** Ordinary variant's factory remains HTTPS-only; NOT_RUN. */
class OrdinaryVariantIsolationTest {
    @Test fun ordinaryFactoryNeverPermitsCleartext(){val p=VariantFactory.endpointPolicy();assertTrue(p is HttpsEndpointPolicy);assertEquals("https://example.test",p.confirm("https://example.test"));for(raw in listOf("http://example.test:8080","ws://example.test:8080")){try{p.confirm(raw);fail("cleartext accepted")}catch(_:IllegalArgumentException){}};assertEquals(null,VariantFactory.presentation.confirmationWarning)}
}
