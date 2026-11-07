package io.worktool.open.prototype.policy

/** Generic permit/work boundary, with no origin, transport scheme or variant-specific constants. */
interface ClientBudget {
    fun epoch():Long
    fun valid(epoch:Long):Boolean
    /** Short, nonblocking admission only (enqueue/consume); never network or disk waits. */
    fun runIfValid(epoch:Long,action:()->Unit):Boolean
    fun inboundFrame(epoch:Long,decodedBytes:Int):Boolean
    fun command(epoch:Long,commandId:String):Boolean
    fun callback(epoch:Long):AutoCloseable?
    fun onInvalidated(listener:(Long,String)->Unit)
}
object UnrestrictedClientBudget:ClientBudget {
    override fun epoch()=0L
    override fun valid(epoch:Long)=true
    override fun runIfValid(epoch:Long,action:()->Unit):Boolean {action();return true}
    override fun inboundFrame(epoch:Long,decodedBytes:Int)=true
    override fun command(epoch:Long,commandId:String)=true
    override fun callback(epoch:Long):AutoCloseable=AutoCloseable{}
    override fun onInvalidated(listener:(Long,String)->Unit)=Unit
}
