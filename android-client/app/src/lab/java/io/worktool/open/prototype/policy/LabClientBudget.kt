package io.worktool.open.prototype.policy

import android.os.SystemClock
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/** LAB-only hard work permit. No grants on receive/reconnect; explicit foreground consent owns grant(). */
interface LeaseTimer { fun schedule(delayMs:Long,work:()->Unit):AutoCloseable }
class MonotonicLeaseTimer:LeaseTimer {
    private val executor=Executors.newSingleThreadScheduledExecutor { Thread(it,"lab-lease-deadline").apply{isDaemon=true} }
    override fun schedule(delayMs:Long,work:()->Unit):AutoCloseable {val future=executor.schedule(Runnable{work()},delayMs,TimeUnit.MILLISECONDS);return AutoCloseable{future.cancel(false)}}
}
class LabClientBudget(
    private val clock:()->Long=SystemClock::elapsedRealtime,
    private val timer:LeaseTimer=MonotonicLeaseTimer()
):ClientBudget {
    private val lock=Any()
    private var generation=0L
    private var active=false
    private var deadline=0L
    private var frames=0
    private var bytes=0L
    private var queued=0
    private val commands=mutableSetOf<String>()
    private val listeners=mutableListOf<(Long,String)->Unit>()
    private var scheduled:AutoCloseable?=null
    override fun epoch():Long=synchronized(lock){generation}
    override fun onInvalidated(listener:(Long,String)->Unit){synchronized(lock){listeners.add(listener)}}
    /** An already-active lease cannot be extended, including by a duplicate confirmation. */
    fun grant():Long {
        val epoch=synchronized(lock){check(!active){"LEASE_ALREADY_ACTIVE"};val end=Math.addExact(clock(),300000L);generation=Math.addExact(generation,1L);active=true;deadline=end;frames=0;bytes=0;queued=0;commands.clear();generation}
        try {arm(epoch,synchronized(lock){(deadline-clock()).coerceAtLeast(0)})} catch(e:Exception){invalidate(epoch,"LEASE_TIMER_FAILED");throw e};return epoch
    }
    private fun arm(epoch:Long,delay:Long){
        val task=timer.schedule(delay){
            val remaining=synchronized(lock){if(!active||epoch!=generation)return@schedule;deadline-clock()}
            if(remaining<=0)invalidate(epoch,"LEASE_EXPIRED") else arm(epoch,remaining)
        }
        synchronized(lock){if(active&&epoch==generation){scheduled?.close();scheduled=task}else task.close()}
    }
    fun cancel(reason:String="LEASE_CANCELLED"){invalidate(epoch(),reason)}
    private fun invalidate(epoch:Long,reason:String){
        val notify=synchronized(lock){if(epoch!=generation||!active)return;active=false;scheduled?.close();scheduled=null;listeners.toList()}
        notify.forEach{listener->runCatching{listener(epoch,reason)}} // never invoke consumers while holding the counter lock
    }
    private fun allow(epoch:Long,check:()->String?):Boolean {
        var failure:String?=null
        var notify:List<(Long,String)->Unit> = emptyList()
        val accepted=synchronized(lock){
            if(epoch!=generation||!active)return@synchronized false
            failure=if(clock()>=deadline)"LEASE_EXPIRED" else check()
            if(failure!=null){active=false;scheduled?.close();scheduled=null;notify=listeners.toList()}
            failure==null
        }
        failure?.let{reason->notify.forEach{listener->runCatching{listener(epoch,reason)}}}
        return accepted
    }
    override fun valid(epoch:Long)=allow(epoch){null}
    override fun runIfValid(epoch:Long,action:()->Unit):Boolean {
        if(!valid(epoch))return false
        return synchronized(lock){if(!active||generation!=epoch||clock()>=deadline)false else {action();true}}
    }
    override fun inboundFrame(epoch:Long,decodedBytes:Int)=allow(epoch){
        if(decodedBytes<0||frames>=256||bytes+decodedBytes>1048576L)"INBOUND_BUDGET_EXHAUSTED"
        else {frames++;bytes+=decodedBytes;null}
    }
    override fun command(epoch:Long,commandId:String)=allow(epoch){
        if(commandId in commands)null else if(commands.size>=32)"COMMAND_BUDGET_EXHAUSTED" else{commands.add(commandId);null}
    }
    override fun callback(epoch:Long):AutoCloseable? {
        if(!allow(epoch){if(queued>=32)"CALLBACK_BUDGET_EXHAUSTED" else{queued++;null}})return null
        val released=AtomicBoolean(false)
        return AutoCloseable{if(released.compareAndSet(false,true))synchronized(lock){if(epoch==generation){check(queued>0);queued--}}}
    }
}
