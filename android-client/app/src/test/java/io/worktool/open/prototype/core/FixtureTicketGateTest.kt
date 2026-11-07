package io.worktool.open.prototype.core

import org.junit.Assert.*
import org.junit.Test

/** Authored tests only: NOT_RUN. They do not represent Android node/permission execution. */
class FixtureTicketGateTest {
    private val target=FixtureTarget("io.worktool.open.prototype.lab","io.worktool.open.prototype.ui.FixtureActivity")
    private val status=FixtureDriverStatus(true,true,true,3,8,"READY")
    private val ticket=FixtureTicket("ticket_demo","command_demo","attempt_demo","a".repeat(64),"b".repeat(64),target,7,3,8,5000)
    @Test fun everyGateIndependentlyDenies(){assertTrue(FixtureTicketGate.permits(ticket,status,target,7,100));for(s in listOf(status.copy(serviceConnected=false),status.copy(locallyEnabled=false),status.copy(foreground=false),status.copy(serviceEpoch=4),status.copy(foregroundEpoch=9)))assertFalse(FixtureTicketGate.permits(ticket,s,target,7,100));assertFalse(FixtureTicketGate.permits(ticket,status,target.copy(targetPackage="other.app"),7,100));assertFalse(FixtureTicketGate.permits(ticket,status,target.copy(targetActivity="other.Activity"),7,100));assertFalse(FixtureTicketGate.permits(ticket,status,target,8,100));assertFalse(FixtureTicketGate.permits(ticket,status,target,7,5000))}
    @Test fun changedInputIsPartialButAttemptedClickIsUnknown(){assertEquals("partially_succeeded",FixtureTicketGate.interrupted(true,true,true,false,"STOP").status);assertEquals("execution_unknown",FixtureTicketGate.interrupted(true,true,true,true,"STOP").status);assertEquals("execution_unknown",FixtureTicketGate.interrupted(true,false,false,false,"STOP").status);assertEquals("cancelled",FixtureTicketGate.interrupted(false,false,false,false,"STOP").status);assertEquals("cancelled",FixtureTicketGate.interrupted(true,false,true,false,"STOP").status)}
}
