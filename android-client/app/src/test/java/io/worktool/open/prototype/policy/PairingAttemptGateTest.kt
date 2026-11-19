package io.worktool.open.prototype.policy

import org.junit.Assert.*
import org.junit.Test

class PairingAttemptGateTest {
    @Test fun networkResponseQueuedThenPauseCannotPersistIdentity() {
        val gate=PairingAttemptGate();val attempt=gate.begin();var persisted=0
        val delayedSerialCallback={ if(gate.consume(attempt,true)) persisted++ }
        gate.invalidate();delayedSerialCallback();assertEquals(0,persisted)
    }
    @Test fun leaveAndReturnDoesNotRestoreOldAttempt() {
        val gate=PairingAttemptGate();val old=gate.begin();gate.invalidate();val next=gate.begin()
        assertFalse(gate.consume(old,true));assertTrue(gate.consume(next,true));assertFalse(gate.consume(next,true))
    }
    @Test fun foregroundIsRequiredAndPauseCannotUndoAlreadyAcceptedResponse() {
        val gate=PairingAttemptGate();val attempt=gate.begin()
        assertFalse(gate.consume(attempt,false));assertTrue(gate.consume(attempt,true))
        gate.invalidate();assertFalse(gate.consume(attempt,true))
    }
    @Test fun queuedEnrollmentThenPauseCannotStartNetwork() {
        val gate=PairingAttemptGate();val attempt=gate.begin();var requests=0
        val queuedWorker={ if(gate.isCurrent(attempt,true)) requests++ }
        gate.invalidate();queuedWorker();assertEquals(0,requests)
    }
    @Test fun queuedPairAndResumeBothLoseIntentAfterForegroundPause() {
        val intents=ConnectionIntentGate();val pair=intents.capture();val resume=intents.capture()
        var requests=0
        val queuedPair={ if(intents.current(pair)) requests++ }
        val queuedResume={ if(intents.current(resume)) requests++ }
        intents.invalidate();queuedPair();queuedResume();assertEquals(0,requests)
        assertTrue(intents.current(intents.capture()))
    }
}
