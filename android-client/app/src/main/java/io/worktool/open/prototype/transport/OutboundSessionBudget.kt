package io.worktool.open.prototype.transport

/** Conservative local guard, not a claim about network/read timing. Includes HELLO.
 * Exhaustion closes for explicit manual resume, never silently buffers started.
 */
class OutboundSessionBudget(private val startedAt:Long) {
    companion object { const val MAX_FRAMES=240;const val MAX_BYTES=480*1024;const val MAX_FRAME_BYTES=65536 }
    private var frames=0
    private var bytes=0
    private var tokens=4.0
    private var byteTokens=131072.0
    private var last=startedAt
    fun take(now:Long,size:Int):Boolean {
        if(now<last || size !in 0..MAX_FRAME_BYTES || frames>=MAX_FRAMES || size>MAX_BYTES-bytes)return false
        val elapsed=(now-last).toDouble()
        tokens=minOf(4.0,tokens+elapsed/250.0)
        byteTokens=minOf(131072.0,byteTokens+elapsed*32.768);last=now
        if(tokens<1.0 || byteTokens<size.toDouble())return false
        tokens-=1.0;byteTokens-=size.toDouble();frames++;bytes+=size;return true
    }
}
