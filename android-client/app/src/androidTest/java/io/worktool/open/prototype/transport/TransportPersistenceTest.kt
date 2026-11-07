package io.worktool.open.prototype.transport

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import android.content.Context
import io.worktool.open.prototype.core.*
import org.json.JSONObject
import org.json.JSONArray
import org.junit.Test
import org.junit.Assert.*
import org.junit.runner.RunWith
import java.util.UUID

/** Android instrumentation only. NOT_RUN until a built APK and authorized emulator/device exist. */
@RunWith(AndroidJUnit4::class)
class TransportPersistenceTest {
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private fun command()=JSONObject().put("commandId","cmd_test").put("attemptId","attempt_test").put("payloadDigest","a".repeat(64))
    private fun result()=JSONObject().put("commandId","cmd_test").put("attemptId","attempt_test").put("status","succeeded").put("effectState","applied").put("code","SYNTHETIC_APPLIED").put("evidenceRefs",JSONArray().put("journal_test"))
    @Test fun durableResultEffectAckAndTenantIsolation() {
        val tenant="tenant_"+UUID.randomUUID().toString(); val j=SqliteJournal(context,tenant,"dev_test")
        assertTrue(j.accept(command())); j.markRunning("cmd_test")
        j.finish("cmd_test",result(),LocalEffect("synthetic_inbox","synthetic_demo","synthetic text")); j.close()
        val reopened=SqliteJournal(context,tenant,"dev_test")
        reopened.recoverInterrupted(); assertEquals(1,reopened.effectRows().size); assertEquals(1,reopened.pendingResults().size)
        assertFalse(reopened.accept(command())); reopened.acknowledge("cmd_test"); assertTrue(reopened.pendingResults().isEmpty()); reopened.close()
        val other=SqliteJournal(context,tenant+"_other","dev_test"); assertNull(other.command("cmd_test")); other.close()
    }
    @Test fun interruptedRunningBecomesUnknownWithoutEffect() {
        val tenant="tenant_"+UUID.randomUUID().toString(); val j=SqliteJournal(context,tenant,"dev_test")
        j.accept(command()); j.markRunning("cmd_test"); j.close()
        val reopened=SqliteJournal(context,tenant,"dev_test"); reopened.recoverInterrupted()
        assertEquals("execution_unknown",reopened.command("cmd_test")!!.stage); assertTrue(reopened.effectRows().isEmpty()); assertEquals(1,reopened.pendingResults().size); reopened.close()
    }
    @Test fun stopBeforeAcceptAndEventConflict() {
        val j=SqliteJournal(context,"tenant_"+UUID.randomUUID().toString(),"dev_test")
        j.requestStop("cmd_test","attempt_test"); j.accept(command()); assertTrue(j.command("cmd_test")!!.stopped)
        assertEquals(EventDecision.NEW,j.event("session_test","evt_test","a"))
        assertEquals(EventDecision.DUPLICATE,j.event("session_test","evt_test","a"))
        assertEquals(EventDecision.CONFLICT,j.event("session_test","evt_test","b")); j.close()
    }
    @Test fun cleartextEndpointRejectedWithoutNetwork() {
        var failed=false
        OkHttpDeviceLink().enroll("http://example.invalid","enroll_test",UUID.randomUUID().toString()) { failed=it.isFailure }
        assertTrue(failed)
    }
}
