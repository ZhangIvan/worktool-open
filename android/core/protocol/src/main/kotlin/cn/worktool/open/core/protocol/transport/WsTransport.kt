/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.transport

/** 可取消的调度句柄。 */
interface Cancellable {
    fun cancel()
}

/** 延时调度抽象（生产：单线程 ScheduledExecutorService；测试：手动推进虚拟时钟）。 */
interface Scheduler {
    fun schedule(delayMillis: Long, task: () -> Unit): Cancellable

    companion object {
        /** 生产实现（守护线程，进程退出不阻塞）。 */
        fun shared(): Scheduler = ScheduledExecutorScheduler()
    }
}

internal class ScheduledExecutorScheduler : Scheduler {
    private val executor = java.util.concurrent.Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "wt-protocol-scheduler").apply { isDaemon = true }
    }

    override fun schedule(delayMillis: Long, task: () -> Unit): Cancellable {
        val future = executor.schedule(task, delayMillis, java.util.concurrent.TimeUnit.MILLISECONDS)
        return object : Cancellable {
            override fun cancel() {
                future.cancel(false)
            }
        }
    }
}

/** WS 传输抽象（生产：OkHttp；测试：fake 通道）。 */
interface WsTransport {
    /** 建连（异步）；结果经 [TransportListener] 回调。 */
    fun connect(url: String, listener: TransportListener)

    /** 发送文本帧；false = 当前不可发送（未连接/已关闭）。 */
    fun send(text: String): Boolean

    fun close(code: Int, reason: String)

    interface Factory {
        fun create(): WsTransport
    }
}

interface TransportListener {
    fun onOpen()
    fun onMessage(text: String)
    fun onFailure(t: Throwable?)
    fun onClosed(code: Int, reason: String)
}
