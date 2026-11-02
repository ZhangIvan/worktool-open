package io.worktool.open.prototype.ui

import io.worktool.open.prototype.core.FixtureSessionPermit
import org.junit.Assert.*
import org.junit.Test

/** Production permit + stop coordinator, queued fake actions only; NOT_RUN/platform NOT_TESTED. */
class FixtureStopIntegrationTest {
    @Test fun queuedOldSetTextAndClickCannotPassFinalGateAfterStopAndNewMode() {
        val old=FixtureSessionPermit();val op=old.operation("command_old");var actions=0
        val queue=listOf<()->Unit>({if(op.isValid())actions++},{if(op.isValid())actions++})
        old.invalidate() // synchronous bridge boundary, before cleanup is enqueued
        var snapshot=ClientUiSnapshot(busy=true,paused=false)
        val stopped=ClientStopCoordinator().stop(false,{}, {}, {}, {}, {snapshot=it.snapshot(snapshot)})
        assertTrue(stopped.succeeded);val fresh=FixtureSessionPermit()
        queue.forEach{it()};assertEquals(0,actions);assertFalse(op.isValid());assertTrue(fresh.isValid())
        assertTrue(snapshot.paused);assertFalse(snapshot.busy)
    }
    @Test fun commandCancellationIsSynchronousAndDoesNotReviveOnRepeatedLookup() {
        val session=FixtureSessionPermit();val first=session.operation("command_one");val other=session.operation("command_two")
        session.cancel("command_one");assertFalse(first.isValid());assertSame(first,session.operation("command_one"));assertTrue(other.isValid())
    }
    @Test fun cancellationBeforeExecuteRegistrationStillBlocksThatOperation() {
        val session=FixtureSessionPermit();session.cancel("command_queued")
        assertFalse(session.operation("command_queued").isValid())
        session.invalidate();assertFalse(session.operation("command_new").isValid())
    }
    @Test fun cleanupExceptionKeepsPermitDeadAndBlocksNextConnection() {
        val session=FixtureSessionPermit();val op=session.operation("command_old");session.invalidate()
        val attempts=mutableListOf<String>();var snapshot=ClientUiSnapshot(busy=true,paused=false);var connects=0
        val result=ClientStopCoordinator().stop(true,{attempts.add("pause");error("sensitive fake cancellation")},
            {attempts.add("disconnect")},{attempts.add("consent")},{attempts.add("closed")},{snapshot=it.snapshot(snapshot).copy(fixtureProbe=null,fixtureConfirmation=null)})
        if(result.succeeded)connects++
        assertEquals(0,connects);assertFalse(op.isValid());assertTrue(attempts.containsAll(listOf("pause","disconnect","consent","closed")))
        assertFalse(snapshot.busy);assertTrue(snapshot.paused);assertEquals("STOP_CLEANUP_FAILED",snapshot.errorCode)
    }
    @Test fun lateCompletionCannotReenableOldPermit() {
        val old=FixtureSessionPermit();val operation=old.operation("command_old");old.invalidate()
        val fresh=FixtureSessionPermit();val lateEvidence=mutableListOf<String>();lateEvidence.add("receipt_unknown")
        assertFalse(operation.isValid());assertFalse(old.isValid());assertTrue(fresh.isValid());assertEquals(1,lateEvidence.size)
    }
    @Test fun alreadyFinalGatePassedActionIsNotClaimedRetractable() {
        val session=FixtureSessionPermit();val op=session.operation("command_old")
        val admitted=op.isValid();session.invalidate()
        var effects=0;if(admitted)effects++ // allowed residual: the system action was already submitted
        assertEquals(1,effects);assertFalse(op.isValid())
    }
}
