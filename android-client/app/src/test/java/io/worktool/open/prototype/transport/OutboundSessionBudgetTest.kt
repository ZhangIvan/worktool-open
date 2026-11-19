package io.worktool.open.prototype.transport

import org.junit.Assert.*
import org.junit.Test

/** Pure accounting tests, not an OkHttp socket/permit integration test. */
class OutboundSessionBudgetTest {
    @Test fun helloAndEveryKindShareFourTokens() {
        val b=OutboundSessionBudget(0)
        listOf("hello","accepted","started","execution_result").forEach { assertTrue(it,b.take(0,100)) }
        assertFalse(b.take(0,100));assertFalse(b.take(249,100));assertTrue(b.take(250,100))
    }
    @Test fun lifetimeFrameCapIncludesHelloAndCannotRefill() {
        val b=OutboundSessionBudget(0)
        repeat(240){assertTrue(b.take(it*1000L,100))}
        assertFalse(b.take(999999,100))
    }
    @Test fun lifetimeByteCapUsesEncodedUtf8NotCharacters() {
        val b=OutboundSessionBudget(0)
        val multibyte="中".repeat(4096).toByteArray(Charsets.UTF_8)
        assertEquals(12288,multibyte.size)
        repeat(40){assertTrue(b.take(it*1000L,multibyte.size))}
        assertFalse(b.take(999999,1))
    }
    @Test fun oversizeAndNegativeLengthsAreRejected() {
        assertFalse(OutboundSessionBudget(0).take(0,65537))
        assertFalse(OutboundSessionBudget(0).take(0,-1))
        assertTrue(OutboundSessionBudget(0).take(0,65536))
    }
    @Test fun clockRollbackDoesNotMintTokens() {
        val b=OutboundSessionBudget(1000)
        assertFalse(b.take(999,100));repeat(4){assertTrue(b.take(1000,100))};assertFalse(b.take(1000,100))
    }
    @Test fun freshSessionHasSeparateBudget() {
        val old=OutboundSessionBudget(0);repeat(240){assertTrue(old.take(it*1000L,100))}
        assertFalse(old.take(240000,100));assertTrue(OutboundSessionBudget(240000).take(240000,100))
    }
    @Test fun helloCountsAgainstByteBurst() {
        val b=OutboundSessionBudget(0)
        assertTrue(b.take(0,128));assertTrue(b.take(0,65536))
        assertFalse(b.take(0,65536))
    }
    @Test fun byteRateRefillsAt32KiBPerSecondAndBurstIsCapped() {
        val b=OutboundSessionBudget(0)
        assertTrue(b.take(0,65536));assertTrue(b.take(0,65536))
        assertFalse(b.take(1999,65536));assertTrue(b.take(2000,65536))
        assertTrue(b.take(999999,65536));assertTrue(b.take(999999,65536))
        assertFalse(b.take(999999,1))
    }

}
