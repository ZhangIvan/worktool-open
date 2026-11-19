package io.worktool.open.prototype.policy

import org.junit.Assert.*
import org.junit.Test

/** Fake-clock/queue source tests. NOT_RUN here; no sockets, actual timers or Android device. */
class LabClientBudgetTest {
    private class Time:LeaseTimer {
        var now=0L
        data class Task(val due:Long,val work:()->Unit,var cancelled:Boolean=false)
        val tasks=mutableListOf<Task>()
        override fun schedule(delayMs:Long,work:()->Unit):AutoCloseable {val t=Task(now+delayMs,work);tasks.add(t);return AutoCloseable{t.cancelled=true}}
        fun advance(value:Long){now=value;while(true){val task=tasks.firstOrNull{!it.cancelled&&it.due<=now}?:break;task.cancelled=true;task.work()}}
        fun budget()=LabClientBudget(clock={now},timer=this)
    }
    private fun denied(block:()->Unit){try{block();fail("expected rejection")}catch(_:IllegalArgumentException){}catch(_:IllegalStateException){}}
    private fun origin()="http://"+listOf(10,20,30,40).joinToString(".")+":8080"
    @Test fun idleLeaseExpiresAt300000AndClearsOriginWithoutFrames(){val t=Time();val b=t.budget();val p=LabEndpointPolicy(b);var closed=0;b.onInvalidated{_,_->closed++};p.confirm(origin());val epoch=b.epoch();t.advance(299999);assertTrue(b.valid(epoch));assertEquals(origin(),p.authorize(origin()));t.advance(300000);assertEquals(1,closed);assertFalse(b.valid(epoch));denied{p.authorize(origin())};assertFalse(b.runIfValid(epoch){fail("expired request")});assertEquals(1,closed)}
    @Test fun duplicateConfirmationAndReceiveDoNotRenewDeadline(){val t=Time();val b=t.budget();val p=LabEndpointPolicy(b);p.confirm(origin());val e=b.epoch();t.advance(299999);p.confirm(origin());assertEquals(e,b.epoch());assertTrue(b.inboundFrame(e,1));assertTrue(b.command(e,"command_one"));t.advance(300000);assertFalse(b.valid(e));denied{b.grant().also{b.grant()}}}
    @Test fun distinct33RejectedBeforeBodyOrEffectAndDuplicatesDoNotResetCounts(){val t=Time();val b=t.budget();val e=b.grant();val bodies=mutableListOf<String>();repeat(32){val id="command_$it";if(b.command(e,id))bodies.add(id)};repeat(50){assertTrue(b.inboundFrame(e,1));assertTrue(b.command(e,"command_0"))};assertEquals(32,bodies.size);assertFalse(b.command(e,"command_32"));assertFalse(b.valid(e));assertFalse(b.runIfValid(e){bodies.add("forbidden")});assertEquals(32,bodies.size)}
    @Test fun frame257IncludingDuplicatesClosesWithoutCounterReset(){val t=Time();val b=t.budget();val e=b.grant();repeat(256){assertTrue(b.inboundFrame(e,1))};assertFalse(b.inboundFrame(e,1));assertFalse(b.command(e,"command_new"))}
    @Test fun aggregateDecodedByteLimitNotJustIndividualFrameLimit(){val t=Time();val b=t.budget();val e=b.grant();repeat(16){assertTrue(b.inboundFrame(e,65536))};assertFalse(b.inboundFrame(e,1));val other=t.budget();val e2=other.grant();assertFalse(other.inboundFrame(e2,1048577));val third=t.budget();val e3=third.grant();assertFalse(third.inboundFrame(e3,-1))}
    @Test fun callback33RejectedAndFinallyReleaseIsIdempotent(){val t=Time();val b=t.budget();val e=b.grant();val slots=List(32){b.callback(e)!!};slots[0].close();slots[0].close();val reused=b.callback(e);assertNotNull(reused);assertNull(b.callback(e));assertFalse(b.valid(e));slots.forEach{it.close()};reused!!.close()}
    @Test fun oldEpochSlotReleaseCannotExpandNewQueueBudget(){val t=Time();val b=t.budget();val e=b.grant();val old=b.callback(e)!!;b.cancel();val next=b.grant();val slots=List(32){b.callback(next)!!};old.close();assertNull(b.callback(next));slots.forEach{it.close()}}
    @Test fun cancelledQueuedPairResumeAndDelayedResponseCannotAdmit(){val t=Time();val b=t.budget();val p=LabEndpointPolicy(b);p.confirm(origin());val e=b.epoch();val intent=ConnectionIntentGate();val stamp=intent.capture();b.onInvalidated{_,_->intent.invalidate()};val queue=mutableListOf<()->Unit>();var requests=0;var identities=0;queue.add{if(intent.current(stamp))b.runIfValid(e){requests++}};val slot=b.callback(e)!!;queue.add{try{b.runIfValid(e){identities++}}finally{slot.close()}};p.clearConsent();queue.forEach{it()};assertEquals(0,requests);assertEquals(0,identities);denied{p.authorize(origin())};p.confirm(origin());assertTrue(b.valid(b.epoch()));assertFalse(b.valid(e))}
    @Test fun expiryRejectsQueuedCallbacksBeforeBodyWork(){val t=Time();val b=t.budget();val e=b.grant();val slot=b.callback(e)!!;var effects=0;t.advance(300000);try{if(b.valid(e))effects++}finally{slot.close()};assertEquals(0,effects)}
    @Test fun notificationAndCommandDuplicateCannotCreateNewLease(){val t=Time();val b=t.budget();val e=b.grant();assertTrue(b.command(e,"command_one"));repeat(10){assertTrue(b.inboundFrame(e,24));assertTrue(b.command(e,"command_one"));assertEquals(e,b.epoch())};b.cancel();assertFalse(b.inboundFrame(e,24));assertFalse(b.command(e,"command_one"));assertNull(b.callback(e))}
    @Test fun schedulingFailureNeverLeavesAnActivePermit(){val b=LabClientBudget(clock={0L},timer=object:LeaseTimer{override fun schedule(delayMs:Long,work:()->Unit):AutoCloseable=throw IllegalStateException("synthetic timer failure")});denied{b.grant()};assertFalse(b.valid(b.epoch()))}

}
